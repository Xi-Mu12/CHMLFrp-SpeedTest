package main

import (
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
	if got.TestSeconds != defaultTestSeconds || got.BWParallel != defaultBWParallel {
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
