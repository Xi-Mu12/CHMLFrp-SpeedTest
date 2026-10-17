// ping.go —— 第一阶段：延迟粗筛（对标 CloudflareSpeedTest 的 TCPing 阶段）
// 逻辑：对每个候选地址 Dial N 次，统计平均延迟/丢包/抖动，按“丢包优先、延迟其次”排序过滤。
package main

import (
	"fmt"
	"net"
	"sort"
	"sync"
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

// PingAll 并发测全部候选，concurrency 对应 CFST 的 -n 参数
func PingAll(cands []Candidate, times int, timeout time.Duration, concurrency int) []PingResult {
	if concurrency <= 0 {
		concurrency = 50
	}
	if concurrency > 1000 {
		concurrency = 1000
	}
	results := make([]PingResult, len(cands))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, c := range cands {
		wg.Add(1)
		go func(idx int, cd Candidate) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[idx] = PingOne(cd.Name, cd.Addr, times, timeout)
			fmt.Printf("\r延迟测速中 %d/%d: %s avg=%v loss=%.2f", idx+1, len(cands), cd.Addr, results[idx].AvgDelay, results[idx].LossRate)
		}(i, c)
	}
	wg.Wait()
	fmt.Println()
	return results
}

// FilterAndSortPing 先过滤（延迟上限/丢包上限，对应 CFST -tl/-tlr），再按丢包→延迟排序
func FilterAndSortPing(results []PingResult, maxDelay time.Duration, maxLoss float64) []PingResult {
	var out []PingResult
	for _, r := range results {
		if r.Recv == 0 {
			continue // 超时/不通直接淘汰
		}
		if maxDelay > 0 && r.AvgDelay > maxDelay {
			continue
		}
		if r.LossRate > maxLoss {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LossRate != out[j].LossRate {
			return out[i].LossRate < out[j].LossRate
		}
		return out[i].AvgDelay < out[j].AvgDelay
	})
	return out
}
