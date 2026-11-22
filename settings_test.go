package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func withTempSettingsDir(t *testing.T) string {
	t.Helper()
	oldDir := settingsConfigDir
	dir := t.TempDir()
	settingsConfigDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() {
		settingsConfigDir = oldDir
		settingsMu.Lock()
		appSettings = AppSettings{TestSeconds: defaultTestSeconds, BWParallel: defaultBWParallel}
		settingsMu.Unlock()
	})
	return dir
}

func TestSettingsDefaultsAndClamp(t *testing.T) {
	withTempSettingsDir(t)

	a := &App{}
	a.loadSettings()
	got := a.GetSettings()
	if got.TestSeconds != defaultTestSeconds || got.BWParallel != defaultBWParallel || got.BWTunnelConcurrency != 2 {
		t.Fatalf("默认值不对: %+v", got)
	}
	if got.CloseAction != "" {
		t.Fatalf("首次启动 closeAction 应为空，got %q", got.CloseAction)
	}

	saved, err := a.SaveSettings(AppSettings{TestSeconds: 1, BWParallel: 99, CloseAction: "tray"})
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if saved.TestSeconds != minTestSeconds || saved.BWParallel != maxBWParallel {
		t.Fatalf("clamp 结果不对: %+v", saved)
	}
	if saved.CloseAction != "tray" {
		t.Fatalf("closeAction 应保留 tray，got %q", saved.CloseAction)
	}

	// 模拟重启：新 App 从同一个临时目录读回。
	b := &App{}
	b.loadSettings()
	got = b.GetSettings()
	if got.TestSeconds != minTestSeconds || got.BWParallel != maxBWParallel || got.CloseAction != "tray" {
		t.Fatalf("重载结果不对: %+v", got)
	}
}

func TestParallelSettingsAndDurationLimit(t *testing.T) {
	withTempSettingsDir(t)
	a := NewApp()
	a.loadSettings()
	saved, err := a.SaveSettings(AppSettings{TestSeconds: 60, BWTunnelConcurrency: 3})
	if err != nil {
		t.Fatal(err)
	}
	if saved.TestSeconds != 30 || saved.BWTunnelConcurrency != 3 {
		t.Fatalf("unexpected settings: %+v", saved)
	}
	a.loadSettings()
	if got := a.GetSettings(); got.TestSeconds != 30 || got.BWTunnelConcurrency != 3 {
		t.Fatalf("settings not persisted: %+v", got)
	}
	if normalizeTunnelConcurrency(-1) != 1 || normalizeTunnelConcurrency(0) != 2 {
		t.Fatal("invalid concurrency normalization")
	}
}

func TestSettingsMigratesLegacyPrefs(t *testing.T) {
	dir := withTempSettingsDir(t)
	// 没有 settings.json，只有 A 组版本留下的 prefs.json。
	legacyDir := filepath.Join(dir, "CHMLFrp-SpeedTest")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "prefs.json"), []byte(`{"closeAction":"tray"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	a := &App{}
	a.loadSettings()
	got := a.GetSettings()
	if got.CloseAction != "tray" {
		t.Fatalf("应从 prefs.json 迁移 tray，got %q", got.CloseAction)
	}
	if got.TestSeconds != defaultTestSeconds || got.BWParallel != defaultBWParallel {
		t.Fatalf("迁移时测速参数应使用默认值: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(legacyDir, "settings.json")); err != nil {
		t.Fatalf("迁移后应生成 settings.json: %v", err)
	}
}

func TestSettingsBOMAndSetCloseAction(t *testing.T) {
	dir := withTempSettingsDir(t)

	a := &App{}
	a.loadSettings()
	if _, err := a.SaveSettings(AppSettings{TestSeconds: 30, BWParallel: 4, CloseAction: "ask"}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	// 手工编辑/编辑器可能加 UTF-8 BOM。
	path := filepath.Join(dir, "CHMLFrp-SpeedTest", "settings.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append([]byte{0xEF, 0xBB, 0xBF}, raw...), 0o600); err != nil {
		t.Fatal(err)
	}

	b := &App{}
	b.loadSettings()
	if got := b.GetSettings(); got.TestSeconds != 30 || got.BWParallel != 4 {
		t.Fatalf("BOM 文件应按原值读回: %+v", got)
	}
	if err := b.SetCloseAction("tray"); err != nil {
		t.Fatalf("SetCloseAction 失败: %v", err)
	}
	got := b.GetSettings()
	if got.CloseAction != "tray" || got.TestSeconds != 30 || got.BWParallel != 4 {
		t.Fatalf("SetCloseAction 不应改动测速参数: %+v", got)
	}
	if err := b.SetCloseAction("delete-everything"); err == nil {
		t.Fatal("非法关闭行为应被拒绝")
	}
}

// TestSaveSettingsKeepsUpdatePrefs 模拟「更新偏好刚写入 → 前端接着保存测速参数」。
// 旧实现里 SaveSettings 用锁外 GetSettings 快照合并，会把 SkippedVersion 等
// 覆盖回旧值，且内存与磁盘都可能丢；这里同时校验内存和落盘结果。
func TestSaveSettingsKeepsUpdatePrefs(t *testing.T) {
	dir := withTempSettingsDir(t)

	a := &App{}
	a.loadSettings()
	if err := a.mutateSettings(func(s *AppSettings) {
		s.SkippedVersion = "v9.9.9"
		s.DisableAutoUpdate = true
	}); err != nil {
		t.Fatalf("mutateSettings 失败: %v", err)
	}

	// 前端保存设置时传的是它自己那份快照（不含更新偏好）
	if _, err := a.SaveSettings(AppSettings{TestSeconds: 15, BWParallel: 4, CloseAction: "tray"}); err != nil {
		t.Fatalf("SaveSettings 失败: %v", err)
	}

	got := a.GetSettings()
	if got.SkippedVersion != "v9.9.9" || !got.DisableAutoUpdate {
		t.Fatalf("更新偏好被覆盖: %+v", got)
	}
	if got.TestSeconds != 15 || got.BWParallel != 4 || got.CloseAction != "tray" {
		t.Fatalf("测速参数未生效: %+v", got)
	}

	// 落盘内容必须与内存一致，否则重启后又回到旧值
	raw, err := os.ReadFile(filepath.Join(dir, "CHMLFrp-SpeedTest", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk AppSettings
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatalf("配置文件损坏: %v", err)
	}
	if disk.SkippedVersion != "v9.9.9" || !disk.DisableAutoUpdate || disk.TestSeconds != 15 {
		t.Fatalf("磁盘内容与内存不一致: %+v", disk)
	}
}

// TestMutateSettingsKeepsTestParams 反向也要成立：只改更新偏好时不能动测速参数。
func TestMutateSettingsKeepsTestParams(t *testing.T) {
	withTempSettingsDir(t)

	a := &App{}
	a.loadSettings()
	if _, err := a.SaveSettings(AppSettings{TestSeconds: 20, BWParallel: 3, CloseAction: "ask"}); err != nil {
		t.Fatalf("SaveSettings 失败: %v", err)
	}
	if err := a.mutateSettings(func(s *AppSettings) { s.LastUpdateCheck = 1700000000 }); err != nil {
		t.Fatalf("mutateSettings 失败: %v", err)
	}
	got := a.GetSettings()
	if got.TestSeconds != 20 || got.BWParallel != 3 || got.CloseAction != "ask" || got.LastUpdateCheck != 1700000000 {
		t.Fatalf("互改字段后应各自保留: %+v", got)
	}
}
