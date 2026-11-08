// nodes.go —— ChmlFrp 节点列表拉取 / 分组过滤 / 节点真实 IP 获取
// 设计说明：
//
//	ChmlFrp 是多个离散节点（不同机房/域名/端口），节点元信息走公开接口 /node，
//	节点的真实 IP 走受保护接口 /nodeinfo（需 Bearer access_token）。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ApiBase v2 统一调用地址
const ApiBase = "https://cf-v2.uapis.cn"

// apiHTTPClient 全局复用的 HTTP 客户端。
// 早先每个请求各 new 一个 client，连接池随对象一起丢掉：/nodeinfo 要给 30 个
// 节点各发一次请求，等于 30 次独立 TLS 握手，刷新周期被握手占掉大半。
var apiHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	},
}

// nodeCacheTTL 节点列表与节点 IP 的缓存有效期。
// 节点是机房固定 IP，短期重复拉取没有意义；用户点「刷新」时可传 force 绕过。
const nodeCacheTTL = 5 * time.Minute

// nodeIPEntry 单个节点的 IP 缓存项，带各自的写入时间，
// 避免不同批次的节点共用同一个全局时间戳导致误判过期。
type nodeIPEntry struct {
	ip string
	at time.Time
}

var (
	nodeCacheMu   sync.Mutex
	nodeListCache []NodeBasic
	nodeListAt    time.Time
	nodeIPCache   = map[string]nodeIPEntry{}
)

// invalidateNodeCache 清空节点缓存，「刷新」按钮和登录态切换时调用。
func invalidateNodeCache() {
	nodeCacheMu.Lock()
	nodeListCache = nil
	nodeListAt = time.Time{}
	nodeIPCache = map[string]nodeIPEntry{}
	nodeCacheMu.Unlock()
}

// flexBool 兼容文档与实际返回不一致的情况：
// 文档写 bool，实际 /node 返回的是 "true"/"false" 字符串。
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `" `)
	switch strings.ToLower(s) {
	case "true", "1":
		*b = true
		return nil
	case "false", "0", "null", "":
		*b = false
		return nil
	}
	// 原生 bool
	var v bool
	if err := json.Unmarshal(data, &v); err == nil {
		*b = flexBool(v)
		return nil
	}
	return fmt.Errorf("无法解析 bool: %s", string(data))
}

// NodeBasic 对应 GET /node 的 data[]（免 Token）
type NodeBasic struct {
	ID        int      `json:"id"`
	Name      string   `json:"name"`
	Area      string   `json:"area"`
	NodeGroup string   `json:"nodegroup"` // user=免费/普通, vip=会员
	China     string   `json:"china"`
	Web       string   `json:"web"`
	UDP       flexBool `json:"udp"`
	Fangyu    flexBool `json:"fangyu"`
	Notes     string   `json:"notes"`
}

type nodeListResp struct {
	Msg   string      `json:"msg"`
	Code  int         `json:"code"`
	State string      `json:"state"`
	Data  []NodeBasic `json:"data"`
}

// NodeInfo 是官方启动器 /nodeinfo 接口中测速所需的最小字段。
// 官方接口返回的 ip 是节点对外提供服务时应连接的真实地址。
type NodeInfo struct {
	Name   string `json:"name"`
	IP     string `json:"ip"`
	RealIP string `json:"real_IP"`
	RealIp string `json:"realIp"`
	IPv6   string `json:"ipv6"`
	// RPort 是节点允许的远程端口范围，形如 "8002-9000"（也可能只给单个端口）。
	// 建隧道时用的 remote_port 必须落在该范围内，否则服务端拒绝：
	// “指定的端口不在允许的范围内”。
	RPort string `json:"rport"`
}

type nodeInfoResp struct {
	Msg   string   `json:"msg"`
	Code  int      `json:"code"`
	State string   `json:"state"`
	Data  NodeInfo `json:"data"`
}

// ChmlUser 对应 GET/POST /userinfo 返回的 data。
type ChmlUser struct {
	ID            int    `json:"id"`
	Username      string `json:"username"`
	Email         string `json:"email"`
	QQ            string `json:"qq"`
	UserGroup     string `json:"usergroup"`
	Term          string `json:"term"`
	Realname      string `json:"realname"`
	Tunnel        int    `json:"tunnel"`
	TunnelCount   int    `json:"tunnnelCount"`
	CurrentConns  int    `json:"totalCurConns"`
	TotalUpload   int64  `json:"total_upload"`
	TotalDownload int64  `json:"total_download"`
	Bandwidth     int    `json:"bandwidth"`
	Integral      int    `json:"integral"`
	UserImage     string `json:"userimg"`
	RegisterTime  string `json:"regtime"`
	// UserToken 旧版用户令牌，frpc 配置里的 user 字段用它（不是 OAuth access_token）
	UserToken string `json:"usertoken"`
}

type userInfoResp struct {
	Msg   string   `json:"msg"`
	Code  int      `json:"code"`
	State string   `json:"state"`
	Data  ChmlUser `json:"data"`
}

// FetchNodes 拉取全量节点列表（无需 Token），命中缓存时直接返回。
func FetchNodes() ([]NodeBasic, error) {
	return fetchNodes(false)
}

// fetchNodes force=true 绕过缓存重新拉取。
func fetchNodes(force bool) ([]NodeBasic, error) {
	if !force {
		nodeCacheMu.Lock()
		if len(nodeListCache) > 0 && time.Since(nodeListAt) < nodeCacheTTL {
			out := append([]NodeBasic(nil), nodeListCache...)
			nodeCacheMu.Unlock()
			return out, nil
		}
		nodeCacheMu.Unlock()
	}
	resp, err := apiHTTPClient.Get(ApiBase + "/node")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r nodeListResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	if r.State != "success" {
		return nil, fmt.Errorf("获取节点列表失败: %s", r.Msg)
	}
	nodeCacheMu.Lock()
	nodeListCache = append([]NodeBasic(nil), r.Data...)
	nodeListAt = time.Now()
	nodeCacheMu.Unlock()
	return r.Data, nil
}

// fetchNodeDetail 取 /nodeinfo 的原始字段，不强制要求返回 IP。
// 带宽测速只需要 rport（端口范围），部分节点可能没给 IP，这时不该整体失败。
func fetchNodeDetail(nodeName, token string) (NodeInfo, error) {
	var info NodeInfo
	nodeName = strings.TrimSpace(nodeName)
	token = strings.TrimSpace(token)
	if nodeName == "" {
		return info, fmt.Errorf("节点名称为空")
	}
	if token == "" {
		return info, fmt.Errorf("未登录，无法获取节点信息")
	}

	req, err := http.NewRequest(http.MethodGet, ApiBase+"/nodeinfo?node="+url.QueryEscape(nodeName), nil)
	if err != nil {
		return info, fmt.Errorf("创建节点信息请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := apiHTTPClient.Do(req)
	if err != nil {
		return info, fmt.Errorf("获取节点信息失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return info, fmt.Errorf("获取节点信息失败(%d)", resp.StatusCode)
	}
	var result nodeInfoResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return info, fmt.Errorf("解析节点信息失败: %w", err)
	}
	if strings.ToLower(strings.TrimSpace(result.State)) != "success" && result.Code != 200 {
		return info, fmt.Errorf("获取节点信息失败: %s", result.Msg)
	}
	if result.Data.IP == "" {
		result.Data.IP = result.Data.RealIP
	}
	if result.Data.IP == "" {
		result.Data.IP = result.Data.RealIp
	}
	return result.Data, nil
}

// FetchNodeInfo 按官方启动器的方式获取单个节点的真实 IP。
// /nodeinfo 是受保护接口，token 传入 OAuth access_token。
func FetchNodeInfo(nodeName, token string) (NodeInfo, error) {
	info, err := fetchNodeDetail(nodeName, token)
	if err != nil {
		return info, err
	}
	if info.IP == "" {
		return info, fmt.Errorf("节点信息未返回 IP")
	}
	return info, nil
}

// FetchNodePortRange 返回节点允许的远程端口范围，ok=false 表示接口没给出或无法解析。
// 失败不返回错误：拿不到范围时由调用方回退到默认范围，不应让测速直接失败。
func FetchNodePortRange(nodeName, token string) (int, int, bool) {
	info, err := fetchNodeDetail(nodeName, token)
	if err != nil {
		return 0, 0, false
	}
	return parsePortRange(info.RPort)
}

// parsePortRange 解析 "8002-9000"、"8002~9000"、"8002 至 9000" 这类端口范围，
// 也接受单个端口 "8002"。解析不出来或数值非法时返回 ok=false。
func parsePortRange(raw string) (int, int, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, 0, false
	}
	parse := func(v string) (int, bool) {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n <= 0 || n > 65535 {
			return 0, false
		}
		return n, true
	}
	for _, sep := range []string{"-", "~", "至", "—"} {
		if i := strings.Index(s, sep); i > 0 {
			lo, okLo := parse(s[:i])
			hi, okHi := parse(s[i+len(sep):])
			if okLo && okHi && lo <= hi {
				return lo, hi, true
			}
		}
	}
	if n, ok := parse(s); ok {
		return n, n, true
	}
	return 0, 0, false
}

// FetchNodeIPs 并发获取节点 IP，单个节点失败不会阻断其它节点。
// FetchNodeIPs 批量获取节点真实 IP，仍在有效期内的节点直接读缓存、不发请求。
func FetchNodeIPs(nodes []NodeBasic, token string) map[string]string {
	return fetchNodeIPs(nodes, token, false)
}

// fetchNodeIPs force=true 时忽略缓存全部重新请求。
func fetchNodeIPs(nodes []NodeBasic, token string, force bool) map[string]string {
	result := make(map[string]string)
	if strings.TrimSpace(token) == "" || len(nodes) == 0 {
		return result
	}

	// 先捞出仍然新鲜的缓存项，只对剩下的节点发请求
	miss := make([]NodeBasic, 0, len(nodes))
	nodeCacheMu.Lock()
	for _, n := range nodes {
		if !force {
			if e, ok := nodeIPCache[n.Name]; ok && time.Since(e.at) < nodeCacheTTL {
				result[n.Name] = e.ip
				continue
			}
		}
		miss = append(miss, n)
	}
	nodeCacheMu.Unlock()
	if len(miss) == 0 {
		return result
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, node := range miss {
		node := node
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			info, err := FetchNodeInfo(node.Name, token)
			if err != nil || info.IP == "" {
				return
			}
			mu.Lock()
			result[node.Name] = info.IP
			mu.Unlock()
		}()
	}
	wg.Wait()

	// 只把本轮真正请求到的节点写回缓存：缓存命中的项不能跟着续期，
	// 否则反复进页面会让它们永不过期；请求失败的节点也不缓存，下次仍会重试。
	nodeCacheMu.Lock()
	now := time.Now()
	for _, n := range miss {
		if ip, ok := result[n.Name]; ok {
			nodeIPCache[n.Name] = nodeIPEntry{ip: ip, at: now}
		}
	}
	nodeCacheMu.Unlock()
	return result
}

// FetchUserInfo 使用 QZhua OAuth access_token 调用 CHMLFrp /userinfo。
// 官方启动器通过 Authorization: Bearer <access_token> 传递登录态，不能将
// OAuth access_token 当作旧版 token 表单字段提交。
func FetchUserInfo(token string) (ChmlUser, error) {
	var user ChmlUser
	token = strings.TrimSpace(token)
	if token == "" {
		return user, fmt.Errorf("用户令牌为空")
	}

	req, err := http.NewRequest(http.MethodGet, ApiBase+"/userinfo", nil)
	if err != nil {
		return user, fmt.Errorf("创建用户信息请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := apiHTTPClient.Do(req)
	if err != nil {
		return user, fmt.Errorf("请求用户信息失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return user, fmt.Errorf("请求用户信息失败(%d)", resp.StatusCode)
	}

	var result userInfoResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return user, fmt.Errorf("解析用户信息失败: %w", err)
	}
	if strings.ToLower(strings.TrimSpace(result.State)) != "success" {
		if result.Msg == "" {
			result.Msg = "接口未返回成功状态"
		}
		return user, fmt.Errorf("获取用户信息失败: %s", result.Msg)
	}
	return result.Data, nil
}

// FilterNodesByGroup 按权限组过滤，group 支持: all / user / vip / user,vip（大小写不敏感）
// 对应你选的“按权限组过滤”。
func FilterNodesByGroup(nodes []NodeBasic, group string) []NodeBasic {
	g := strings.ToLower(strings.TrimSpace(group))
	if g == "" || g == "all" || g == "全部" {
		return nodes
	}
	allow := map[string]bool{}
	for _, p := range strings.Split(g, ",") {
		allow[strings.TrimSpace(p)] = true
	}
	var out []NodeBasic
	for _, n := range nodes {
		if allow[strings.ToLower(n.NodeGroup)] {
			out = append(out, n)
		}
	}
	return out
}
