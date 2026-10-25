//go:build windows

package main

import "syscall"

// hiddenProcAttr 让 frpc 子进程不弹控制台窗口（Windows）
func hiddenProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true}
}
