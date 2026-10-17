//go:build windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows/registry"
)

const autostartName = "CHMLFrp-SpeedTest"

// GetAutostart 读取开机自启状态（HKCU\Run）
func (a *App) GetAutostart() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()
	v, _, err := k.GetStringValue(autostartName)
	if err == registry.ErrNotExist {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return v != "", nil
}

// SetAutostart 设置开机自启（写入/删除 HKCU\Run）
func (a *App) SetAutostart(enable bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !enable {
		if err := k.DeleteValue(autostartName); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("无法定位程序路径: %w", err)
	}
	return k.SetStringValue(autostartName, fmt.Sprintf(`"%s"`, exe))
}
