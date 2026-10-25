// nodes.go —— ChmlFrp 节点拉取 / 过滤 / 候选解析
// 设计说明（对标 CloudflareSpeedTest）：
//
//	CFST 是同一服务测一堆 AnyCast IP；ChmlFrp 是多个离散节点（不同机房/域名/端口），
//	所以第一步是先拿到“测谁”——即候选地址列表。
//	Token 留空时走“隧道地址模式”（你已经在用的方式）：每个节点建一条隧道，
//	把公网地址逐行写进 nodes.txt 来批量测。Token 填上后可升级为自动拉取 nodeinfo。
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// TODO: 在这里填你的 ChmlFrp 用户 Token，留空则跳过需要鉴权的接口（/nodeinfo）。
// 获取方式：官网管理面板 -> 用户信息 -> 令牌。
// 填上后可自动获取节点真实 IP/端口/在线状态，无需手写 nodes.txt。
var UserToken = ""

// ApiBase v2 统一调用地址
const ApiBase = "https://cf-v2.uapis.cn"

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
}

type userInfoResp struct {
	Msg   string   `json:"msg"`
	Code  int      `json:"code"`
	State string   `json:"state"`
	Data  ChmlUser `json:"data"`
}

// FetchNodes 拉取全量节点列表（无需 Token）
func FetchNodes() ([]NodeBasic, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(ApiBase + "/node")
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
	return r.Data, nil
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

	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, ApiBase+"/userinfo", nil)
	if err != nil {
		return user, fmt.Errorf("创建用户信息请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
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

// Candidate 一个待测地址（通常是你为该节点建的隧道的公网地址 host:port）
type Candidate struct {
	Name string // 节点名/备注
	Addr string // host:port，例如 cd.4.frp.one:35333
}

// ParseCandidatesFile 解析 nodes.txt，支持以下格式（# 开头为注释）：
//
//	cd.4.frp.one:35333
//	南京多线,cd.4.frp.one:35333
//	南京多线|cd.4.frp.one:35333
//	南京多线 cd.4.frp.one:35333
func ParseCandidatesFile(path string) ([]Candidate, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Candidate
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// 去掉行内注释 "addr # 备注"
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		var name, addr string
		switch {
		case strings.Contains(line, ","):
			p := strings.SplitN(line, ",", 2)
			name, addr = strings.TrimSpace(p[0]), strings.TrimSpace(p[1])
		case strings.Contains(line, "|"):
			p := strings.SplitN(line, "|", 2)
			name, addr = strings.TrimSpace(p[0]), strings.TrimSpace(p[1])
		case strings.Contains(line, " ") || strings.Contains(line, "\t"):
			p := strings.Fields(line)
			if len(p) >= 2 {
				name, addr = p[0], p[1]
			} else {
				addr = p[0]
			}
		default:
			addr = line
		}
		if addr == "" {
			return nil, fmt.Errorf("第 %d 行地址为空", lineNo)
		}
		if !strings.Contains(addr, ":") {
			return nil, fmt.Errorf("第 %d 行格式应为 host:port， got %q", lineNo, line)
		}
		if name == "" {
			name = addr
		}
		out = append(out, Candidate{Name: name, Addr: addr})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("候选文件为空: %s", path)
	}
	return out, nil
}
