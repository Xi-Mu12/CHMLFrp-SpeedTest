//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Linux 开机自启：写入 ~/.config/autostart/<name>.desktop，由 XDG 桌面环境在登录时拉起。
const desktopFileName = "chmlfrp-speedtest.desktop"

func autostartDesktopPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "autostart", desktopFileName), nil
}

// GetAutostart 读取 autostart desktop 文件是否存在
func (a *App) GetAutostart() (bool, error) {
	p, err := autostartDesktopPath()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// SetAutostart 写入 / 删除 autostart desktop 文件
func (a *App) SetAutostart(enable bool) error {
	p, err := autostartDesktopPath()
	if err != nil {
		return err
	}
	if !enable {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("无法定位程序路径: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// desktop Exec 字段：路径含空格时需用引号包裹
	execField := exe
	if strings.ContainsAny(execField, " \t") {
		execField = `"` + execField + `"`
	}
	content := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=CHMLFrp-SpeedTest\n" +
		"Comment=CHMLFrp 网络测速工具\n" +
		"Exec=" + execField + "\n" +
		"Terminal=false\n" +
		"X-GNOME-Autostart-enabled=true\n"
	return os.WriteFile(p, []byte(content), 0o644)
}
