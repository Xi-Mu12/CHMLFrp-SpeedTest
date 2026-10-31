// ping.go —— 第一阶段：延迟粗筛（对标 CloudflareSpeedTest 的 TCPing 阶段）
// 逻辑：对每个候选地址 Dial N 次，统计平均延迟/丢包/抖动，按“丢包优先、延迟其次”排序过滤。
package main

import (
	"net"
	"time"
)

// PingResult 单个候选的延迟测速结果
type PingResult struct {
	Name     string
	Addr     string
	Sent     int
	Recv     int
	LossRate float64 // 0~1
	AvgDelay time.Duration
	MinDelay time.Duration
	MaxDelay time.Duration
	Jitter   time.Duration // 相邻成功样本差值均值，越大越抖
}

func tcpPingOnce(addr string, timeout time.Duration) (time.Duration, error) {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return 0, err
	}
	_ = conn.Close()
	return time.Since(start), nil
}

// PingOne 测一个地址 times 次
func PingOne(name, addr string, times int, timeout time.Duration) PingResult {
	r := PingResult{Name: name, Addr: addr, Sent: times}
	var samples []time.Duration
	for i := 0; i < times; i++ {
		d, err := tcpPingOnce(addr, timeout)
		if err == nil {
			samples = append(samples, d)
		}
	}
	r.Recv = len(samples)
	if r.Sent > 0 {
		r.LossRate = float64(r.Sent-r.Recv) / float64(r.Sent)
	}
	if len(samples) == 0 {
		return r
	}
	min, max := samples[0], samples[0]
	var sum time.Duration
	for _, d := range samples {
		if d < min {
			min = d
		}
		if d > max {
			max = d
		}
		sum += d
	}
	r.MinDelay, r.MaxDelay = min, max
	r.AvgDelay = time.Duration(int64(sum) / int64(len(samples)))
	if len(samples) > 1 {
		var jsum time.Duration
		for i := 1; i < len(samples); i++ {
			diff := samples[i] - samples[i-1]
			if diff < 0 {
				diff = -diff
			}
			jsum += diff
		}
		r.Jitter = time.Duration(int64(jsum) / int64(len(samples)-1))
	}
	return r
}
