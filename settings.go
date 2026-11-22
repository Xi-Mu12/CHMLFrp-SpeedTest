// settings.go —— 应用配置中心
//
// 关闭行为、带宽测速参数都放在这里，落盘到
// <用户配置目录>/CHMLFrp-SpeedTest/settings.json。
//
// 为什么要放 Go 侧：Alt+F4 不经过前端；带宽测速的秒数/连接数也需要后端
// 再校验一次，不能只信 localStorage 或前端输入。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	defaultTestSeconds = 10
	minTestSeconds     = 5
	maxTestSeconds     = 30

	defaultBWParallel = 6
	minBWParallel     = 1
	maxBWParallel     = 8
)

// AppSettings 是落盘配置。数值字段在读取/保存时都会 clamp，
// 旧版本/手工编辑写坏的值不会让测速跑出异常参数。
type AppSettings struct {
	TestSeconds         int    `json:"testSeconds"`
	BWParallel          int    `json:"bwParallel"`
	BWTunnelConcurrency int    `json:"bwTunnelConcurrency"`
	CloseAction         string `json:"closeAction,omitempty"` // ask | tray | quit

	// 更新偏好。DisableAutoUpdate 用反向布尔：旧配置里没有该字段时
	// 反序列化为 false，即默认开启自动检查，符合新装用户的预期。
	DisableAutoUpdate bool   `json:"disableAutoUpdate,omitempty"`
	SkippedVersion    string `json:"skippedVersion,omitempty"`
	LastUpdateCheck   int64  `json:"lastUpdateCheck,omitempty"`
}

var (
	// settingsConfigDir 便于测试注入临时目录。
	settingsConfigDir = os.UserConfigDir
	settingsMu        sync.RWMutex
	appSettings       = AppSettings{TestSeconds: defaultTestSeconds, BWParallel: defaultBWParallel}
)

func normalizeTestSeconds(v int) int {
	if v <= 0 {
		return defaultTestSeconds
	}
	if v < minTestSeconds {
		return minTestSeconds
	}
	if v > maxTestSeconds {
		return maxTestSeconds
	}
	return v
}

func normalizeTunnelConcurrency(v int) int {
	if v == 0 {
		return 2
	}
	if v < 1 {
		return 1
	}
	return v
}

func normalizeBWParallel(v int) int {
	if v <= 0 {
		return defaultBWParallel
	}
	if v < minBWParallel {
		return minBWParallel
	}
	if v > maxBWParallel {
		return maxBWParallel
	}
	return v
}

func normalizeCloseAction(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "ask", "tray", "quit":
		return strings.ToLower(strings.TrimSpace(v))
	}
	return ""
}

func settingsFilePath() (string, error) {
	base, err := settingsConfigDir()
	if err != nil || base == "" {
		return "", fmt.Errorf("无法定位配置目录: %w", err)
	}
	dir := filepath.Join(base, "CHMLFrp-SpeedTest")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建配置目录失败: %w", err)
	}
	return filepath.Join(dir, "settings.json"), nil
}

// readLegacyCloseAction 读取 A 组版本使用的 prefs.json，用于无损迁移。
func readLegacyCloseAction(settingsPath string) string {
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(settingsPath), "prefs.json"))
	if err != nil {
		return ""
	}
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	var p struct {
		CloseAction string `json:"closeAction"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return ""
	}
	return normalizeCloseAction(p.CloseAction)
}

// loadSettings 在 startup 调用一次。文件不存在/损坏/字段缺失都回落到默认值。
func (a *App) loadSettings() {
	next := AppSettings{TestSeconds: defaultTestSeconds, BWParallel: defaultBWParallel}
	needSave := false
	path, err := settingsFilePath()
	if err == nil {
		raw, readErr := os.ReadFile(path)
		if readErr == nil {
			raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
			_ = json.Unmarshal(raw, &next)
		}
		if next.CloseAction == "" {
			// settings.json 还没有 closeAction（首次升级）时，从旧 prefs.json 迁移。
			if legacy := readLegacyCloseAction(path); legacy != "" {
				next.CloseAction = legacy
				needSave = true
			}
		}
	}
	next.TestSeconds = normalizeTestSeconds(next.TestSeconds)
	next.BWParallel = normalizeBWParallel(next.BWParallel)
	next.BWTunnelConcurrency = normalizeTunnelConcurrency(next.BWTunnelConcurrency)
	next.CloseAction = normalizeCloseAction(next.CloseAction)

	settingsMu.Lock()
	appSettings = next
	if needSave {
		// 迁移结果立即落盘，之后不再依赖旧 prefs.json。
		// 与改内存同锁，避免与首次 SaveSettings 交错时把迁移结果丢掉。
		_ = saveSettings(next)
	}
	settingsMu.Unlock()
}

func saveSettings(s AppSettings) error {
	path, err := settingsFilePath()
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d-%d.tmp", path, os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		// Windows 上目标文件被占用/已存在时 Rename 可能失败；删旧再试一次。
		_ = os.Remove(path)
		if err2 := os.Rename(tmp, path); err2 != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("保存配置失败: %w", err2)
		}
	}
	return nil
}

// mutateSettings 只修改指定字段并落盘，避免整结构体覆盖。
// 更新偏好（跳过版本、最近检查时间）走这里，不经过前端传来的 SaveSettings。
//
// 「改内存 + 落盘」必须在同一把锁内完成：否则下面 SaveSettings 会拿着更早
// 取到的快照把这里刚写进去的偏好覆盖掉（反过来也一样），且两者共用同一个
// 临时文件名时还可能互相截断。
func (a *App) mutateSettings(fn func(*AppSettings)) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	s := appSettings
	fn(&s)
	if err := saveSettings(s); err != nil {
		return err
	}
	appSettings = s
	return nil
}

// GetSettings 返回当前配置快照。
func (a *App) GetSettings() AppSettings {
	settingsMu.RLock()
	s := appSettings
	settingsMu.RUnlock()
	s.TestSeconds = normalizeTestSeconds(s.TestSeconds)
	s.BWParallel = normalizeBWParallel(s.BWParallel)
	s.BWTunnelConcurrency = normalizeTunnelConcurrency(s.BWTunnelConcurrency)
	s.CloseAction = normalizeCloseAction(s.CloseAction)
	return s
}

// SaveSettings 供设置页调用；返回后端实际生效的值（可能被 clamp）。
func (a *App) SaveSettings(s AppSettings) (AppSettings, error) {
	s.BWTunnelConcurrency = normalizeTunnelConcurrency(s.BWTunnelConcurrency)
	s.TestSeconds = normalizeTestSeconds(s.TestSeconds)
	s.BWParallel = normalizeBWParallel(s.BWParallel)
	action := normalizeCloseAction(s.CloseAction)
	if s.CloseAction != "" && action == "" {
		return AppSettings{}, fmt.Errorf("不支持的关闭行为: %q", s.CloseAction)
	}

	// 读-改-写整段持锁。锁内直接读 appSettings，不能再用 GetSettings()：
	// 那是锁外快照，正是「用旧值覆盖 mutateSettings 刚写的偏好」的来源。
	settingsMu.Lock()
	defer settingsMu.Unlock()
	if action == "" {
		// 前端只改测速参数时不要把已保存的关闭行为清空。
		action = normalizeCloseAction(appSettings.CloseAction)
	}
	s.CloseAction = action
	// 更新偏好由独立接口维护；前端保存测速参数时整体覆盖结构体会把它们清空。
	s.DisableAutoUpdate = appSettings.DisableAutoUpdate
	s.SkippedVersion = appSettings.SkippedVersion
	s.LastUpdateCheck = appSettings.LastUpdateCheck
	if err := saveSettings(s); err != nil {
		return AppSettings{}, err
	}
	appSettings = s
	return s, nil
}

// GetCloseAction 保留给现有标题栏/托盘逻辑使用。
func (a *App) GetCloseAction() string {
	return a.GetSettings().CloseAction
}

// SetCloseAction 只改关闭行为，不触碰测速参数。
func (a *App) SetCloseAction(action string) error {
	s := a.GetSettings()
	s.CloseAction = action
	_, err := a.SaveSettings(s)
	return err
}

// RequestClose 统一处理自定义标题栏 ×：托盘偏好直接隐藏，询问偏好交给前端弹窗。
func (a *App) RequestClose() {
	if a.ctx == nil {
		return
	}
	switch a.GetCloseAction() {
	case "quit":
		a.QuitApp()
	case "tray":
		if trayReady.Load() {
			a.hideWindow()
			return
		}
		// 托盘没起来时不能把窗口藏掉，否则用户找不回来，退回询问。
		runtime.EventsEmit(a.ctx, "closeRequested")
	default:
		runtime.EventsEmit(a.ctx, "closeRequested")
	}
}

// handleBeforeClose 由 Wails 在窗口真正关闭前调用。
// 返回 true 表示拦截；此时窗口仍然存在，前端可以弹“最小化/退出”询问框。
func (a *App) handleBeforeClose(ctx context.Context) bool {
	if a.quitting.Load() {
		return false
	}
	switch a.GetCloseAction() {
	case "quit":
		return false
	case "tray":
		if trayReady.Load() {
			a.hideWindow()
			return true
		}
	}
	if ctx != nil {
		runtime.EventsEmit(ctx, "closeRequested")
	}
	return true
}
