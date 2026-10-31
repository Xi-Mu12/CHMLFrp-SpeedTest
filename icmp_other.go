//go:build !windows

// icmp_other.go —— ICMP 活性探测（非 Windows 降级实现）
// 无 raw socket 特权时直接调系统 ping 命令（macOS/Linux 自带，输出解析 time=xx ms；
// 中英文 locale 都兼容，正则只匹配 ASCII 的 time= 前缀——macOS/常见 Linux 即使
// 中文 locale，耗时字段也保留 time=）。失败就如实报错，上层按"ICMP 不通"处理。
package main

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var icmpRTTRe = regexp.MustCompile(`time[=:]\s*([\d.]+)\s*ms`)

func icmpPing(host string, timeout time.Duration) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// macOS 的 -W 单位是毫秒，Linux iputils 是秒；这里不带 -W，统一用 ctx 控超时
	cmd := exec.CommandContext(ctx, "ping", "-c", "1", host)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return 0, fmt.Errorf("ICMP 超时")
		}
		return 0, fmt.Errorf("ping 失败: %v", err)
	}
	m := icmpRTTRe.FindSubmatch(out)
	if m == nil {
		return 0, fmt.Errorf("无法解析 ping 输出: %s", strings.TrimSpace(string(out)))
	}
	ms, perr := strconv.ParseFloat(string(m[1]), 64)
	if perr != nil {
		return 0, fmt.Errorf("解析 RTT 失败: %v", perr)
	}
	return time.Duration(ms * float64(time.Millisecond)), nil
}
