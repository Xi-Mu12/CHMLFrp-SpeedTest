//go:build !windows && !darwin && !linux

package main

import "fmt"

// GetAutostart 非 win/mac/linux 平台（如 freebsd）暂不支持开机自启
func (a *App) GetAutostart() (bool, error) {
	return false, fmt.Errorf("当前系统暂不支持开机自启")
}

// SetAutostart 非 win/mac/linux 平台（如 freebsd）暂不支持开机自启
func (a *App) SetAutostart(enable bool) error {
	return fmt.Errorf("当前系统暂不支持开机自启")
}
