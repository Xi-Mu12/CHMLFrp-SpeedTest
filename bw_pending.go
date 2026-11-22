// bw_pending.go —— 「没来得及删除的临时隧道」补偿清理
//
// 退出流程只等 3 秒（bwCleanupWait）。worker 如果正卡在隧道 API 请求上，
// defer 里的删除就没机会跑完，隧道会一直挂在账号里占配额——用户下次想测速
// 可能直接撞「隧道数量已达上限」。这里把 id 落盘，下次启动拿到 token 后补删。
//
// 只存 id 不存 token：补偿文件放在配置目录，落盘 token 等于多一个泄密面。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	pendingTunnelsFile = "pending-tunnels.json"
	// 保留上限与过期天数：服务端可能早已删掉（或用户手动删了），
	// 无限重试只会每次启动多几条无意义的失败日志。
	pendingTunnelsMax = 200
	pendingTunnelsTTL = 7 * 24 * time.Hour
	// 补删的总预算：启动后在后台跑，不能拖住登录与节点加载
	pendingTunnelsBudget = 20 * time.Second
)

type pendingTunnel struct {
	ID      int   `json:"id"`
	SavedAt int64 `json:"savedAt"` // Unix 秒，用于过期丢弃
}

var pendingMu sync.Mutex

func pendingTunnelsPath() (string, error) {
	path, err := settingsFilePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), pendingTunnelsFile), nil
}

func readPendingTunnels(path string) []pendingTunnel {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var list []pendingTunnel
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil
	}
	return list
}

func writePendingTunnels(path string, list []pendingTunnel) error {
	if len(list) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// recordPendingTunnels 追加待删隧道 id。退出流程调用，写失败不影响退出。
func (a *App) recordPendingTunnels(ids []int) {
	if len(ids) == 0 {
		return
	}
	path, err := pendingTunnelsPath()
	if err != nil {
		return
	}
	pendingMu.Lock()
	defer pendingMu.Unlock()
	list := readPendingTunnels(path)
	seen := make(map[int]bool, len(list))
	for _, p := range list {
		seen[p.ID] = true
	}
	now := time.Now().Unix()
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		list = append(list, pendingTunnel{ID: id, SavedAt: now})
		seen[id] = true
	}
	if len(list) > pendingTunnelsMax {
		list = list[len(list)-pendingTunnelsMax:]
	}
	_ = writePendingTunnels(path, list)
}

// flushPendingTunnels 用当前 token 补删上次遗留的临时隧道。
// 登录成功或恢复登录态后调用；没有 token 时直接返回，文件留到下次。
func (a *App) flushPendingTunnels() {
	path, err := pendingTunnelsPath()
	if err != nil {
		return
	}
	pendingMu.Lock()
	list := readPendingTunnels(path)
	pendingMu.Unlock()
	if len(list) == 0 {
		return
	}
	token := a.currentToken()
	if token == "" {
		return // 未登录：下次启动再试，别把记录丢了
	}

	ctx, cancel := context.WithTimeout(context.Background(), pendingTunnelsBudget)
	defer cancel()
	remain := make([]pendingTunnel, 0, len(list))
	deadline := time.Now().Add(-pendingTunnelsTTL).Unix()
	for _, p := range list {
		if p.SavedAt > 0 && p.SavedAt < deadline {
			continue // 过期：多半早就没了，别再刷失败日志
		}
		if ctx.Err() != nil {
			remain = append(remain, p)
			continue
		}
		if err := DeleteTunnelAPI(ctx, token, p.ID); err != nil {
			// 服务端说「不存在」= 已经在别处删掉了（例如退出时那次删除其实成功了），
			// 不该再无限重试，否则每次启动都多几条失败日志
			if isTunnelGoneErr(err) {
				continue
			}
			remain = append(remain, p)
		}
	}

	pendingMu.Lock()
	defer pendingMu.Unlock()
	_ = writePendingTunnels(path, remain)
}

// isTunnelGoneErr 判断"隧道已不存在"这类错误：说明补偿项已经没必要再留。
// 关键词取窄一点，别把网络错误误判成已删除。
func isTunnelGoneErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, k := range []string{"不存在", "无此隧道", "已删除", "not found", "does not exist", "no such"} {
		if strings.Contains(msg, k) {
			return true
		}
	}
	return false
}
