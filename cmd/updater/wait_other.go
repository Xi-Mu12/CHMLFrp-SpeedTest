//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// waitForPID 在类 Unix 上用 signal 0 探测进程是否存在。
// 进程退出后 kill 返回 ESRCH，即可确认已结束。
func waitForPID(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		p, err := os.FindProcess(pid)
		if err != nil {
			return nil
		}
		if err := p.Signal(syscall.Signal(0)); err != nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待主程序退出超时")
		}
		time.Sleep(300 * time.Millisecond)
	}
}
