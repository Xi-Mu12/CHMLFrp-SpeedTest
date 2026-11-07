//go:build windows

package main

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// waitForPID 用 WaitForSingleObject 等主进程退出。
// 相比轮询 IsProcessAlive，它不会漏掉"刚好在两次轮询之间退出"的情况。
func waitForPID(pid int, timeout time.Duration) error {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if err == windows.ERROR_INVALID_PARAMETER {
			return nil // 进程已经不存在
		}
		return fmt.Errorf("打开进程失败: %w", err)
	}
	defer windows.CloseHandle(h)

	result, err := windows.WaitForSingleObject(h, uint32(timeout/time.Millisecond))
	if err != nil {
		return fmt.Errorf("等待进程失败: %w", err)
	}
	if result == 0x00000102 { // WAIT_TIMEOUT
		return fmt.Errorf("等待主程序退出超时")
	}
	return nil
}
