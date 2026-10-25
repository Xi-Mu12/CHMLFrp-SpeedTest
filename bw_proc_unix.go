//go:build !windows

package main

import "syscall"

// hiddenProcAttr 非 Windows 平台无隐藏窗口概念
func hiddenProcAttr() *syscall.SysProcAttr {
	return nil
}
