// bw_tunnel.go —— CHMLFrp 隧道 API（带宽测速用临时隧道）
//
// 对标官方启动器 src/services/api.ts：
//   - 列表：GET  /tunnel
//   - 创建：POST /create_tunnel（body 为 CreateTunnelParams）
//   - 删除：GET  /delete_tunnel?tunnelid=xx
//
// 鉴权统一用 OAuth access_token 的 Authorization: Bearer 头，
// 与 nodes.go 里 /nodeinfo、/userinfo 的做法一致。
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// TunnelAPI GET /tunnel 返回的单条隧道记录，字段对齐官方启动器 Tunnel 接口
type TunnelAPI struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Node       string `json:"node"`
	IP         string `json:"ip"`          // 公网连接地址（测速目标 host）
	NodeIP     string `json:"node_ip"`     // frpc 的 server_addr
	ServerPort int    `json:"server_port"` // frpc 的 server_port
	NodeToken  string `json:"node_token"`  // frpc 的 token
	Dorp       string `json:"dorp"`        // 远程端口（tcp/udp）
	LocalIP    string `json:"localip"`
	LocalPort  int    `json:"nport"`
	NodeState  string `json:"nodestate"`
}

// CreateTunnelParams 对齐官方启动器 CreateTunnelParams
type CreateTunnelParams struct {
	TunnelName  string `json:"tunnelname"`
	Node        string `json:"node"`
	LocalIP     string `json:"localip"`
	PortType    string `json:"porttype"`
	LocalPort   int    `json:"localport"`
	Encryption  bool   `json:"encryption"`
	Compression bool   `json:"compression"`
	ExtraParams string `json:"extraparams"`
	RemotePort  int    `json:"remoteport,omitempty"`
	BandDomain  string `json:"banddomain,omitempty"`
}

type chmlEnvelope struct {
	Msg   string `json:"msg"`
	Code  int    `json:"code"`
	State string `json:"state"`
}

type tunnelListResp struct {
	Msg   string      `json:"msg"`
	Code  int         `json:"code"`
	State string      `json:"state"`
	Data  []TunnelAPI `json:"data"`
}

var chmlHTTPClient = &http.Client{Timeout: 20 * time.Second}

func chmlDo(method, path, token string, body []byte) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, ApiBase+path, reader)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := chmlHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 CHMLFrp 接口失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("读取 CHMLFrp 响应失败: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return raw, fmt.Errorf("接口返回 HTTP %d: %s", resp.StatusCode, msg)
	}
	return raw, nil
}

// checkEnvelope 校验 {code,state,msg} 信封；失败时尽量带上服务端的 msg
func checkEnvelope(raw []byte) error {
	var env chmlEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("响应解析失败: %s", msg)
	}
	if strings.EqualFold(strings.TrimSpace(env.State), "success") || env.Code == 200 {
		return nil
	}
	msg := strings.TrimSpace(env.Msg)
	if msg == "" {
		msg = strings.TrimSpace(string(raw))
		if len(msg) > 200 {
			msg = msg[:200]
		}
	}
	if msg == "" {
		msg = "接口未返回成功状态"
	}
	return fmt.Errorf("%s", msg)
}

// ListTunnels 拉取账号下全部隧道
func ListTunnels(token string) ([]TunnelAPI, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("未登录")
	}
	raw, err := chmlDo(http.MethodGet, "/tunnel", token, nil)
	if err != nil {
		return nil, err
	}
	var r tunnelListResp
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("解析隧道列表失败: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(r.State), "success") && r.Code != 200 {
		msg := r.Msg
		if msg == "" {
			msg = "接口未返回成功状态"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return r.Data, nil
}

// CreateTunnelAPI 创建一条隧道
func CreateTunnelAPI(token string, p CreateTunnelParams) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("未登录")
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	raw, err := chmlDo(http.MethodPost, "/create_tunnel", token, body)
	if err != nil {
		return err
	}
	return checkEnvelope(raw)
}

// DeleteTunnelAPI 删除一条隧道
func DeleteTunnelAPI(token string, id int) error {
	if id <= 0 {
		return fmt.Errorf("隧道 id 无效")
	}
	raw, err := chmlDo(http.MethodGet, fmt.Sprintf("/delete_tunnel?tunnelid=%d", id), token, nil)
	if err != nil {
		return err
	}
	return checkEnvelope(raw)
}

// findTunnelByName 在账号隧道里按名字查找（创建接口不返回 id，只能回查）
func findTunnelByName(token, name string) *TunnelAPI {
	list, err := ListTunnels(token)
	if err != nil {
		return nil
	}
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}

// waitTunnelByName 轮询等待新隧道出现在列表里，返回完整记录
func waitTunnelByName(token, name string, timeout time.Duration, cancelled func() bool) (*TunnelAPI, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if cancelled != nil && cancelled() {
			return nil, fmt.Errorf("已取消")
		}
		if tun := findTunnelByName(token, name); tun != nil {
			return tun, nil
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return nil, fmt.Errorf("等待隧道出现超时: %w", lastErr)
			}
			return nil, fmt.Errorf("等待隧道出现超时")
		}
		time.Sleep(600 * time.Millisecond)
	}
}

// isFatalTunnelErr 判断创建失败的报错是否重试也没用（配额/权限类）。
// 关键词必须足够具体：早期版本用裸 "token"、"vip" 做子串匹配，
// 会把「nodetoken 校验失败」「vip 节点繁忙」这类可重试错误误判为致命，
// 直接放弃剩余 3 次重试，是实打实的漏测来源。
func isFatalTunnelErr(msg string) bool {
	low := strings.ToLower(strings.TrimSpace(msg))
	if low == "" {
		return false
	}
	fatal := []string{
		// 配额 / 会员权限
		"上限", "超限", "配额", "会员", "仅限vip", "vip专享", "vip节点",
		// 登录态 / 鉴权
		"未登录", "请先登录", "登录已过期", "登录过期", "权限不足", "无权限",
		"unauthorized", "forbidden",
		"token无效", "token 无效", "invalid token", "token已过期", "token expired",
		// 明确的业务拒绝
		"不属于你", "被封禁", "已被封禁",
	}
	for _, f := range fatal {
		if strings.Contains(low, f) {
			return true
		}
	}
	return false
}
