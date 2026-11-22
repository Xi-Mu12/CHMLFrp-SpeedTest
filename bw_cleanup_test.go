package main

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestChmlDoHonoursCancel 隧道 API 必须响应 ctx：用户点「取消」或程序退出时，
// 在途请求应当立刻返回，而不是把 client 的 20 秒超时走完。
// 这里用已取消的 ctx，连 DNS 都不该发出去。
func TestChmlDoHonoursCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := chmlDo(ctx, http.MethodGet, "/tunnel", "token", nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("已取消的请求应当返回错误")
	}
	if !strings.Contains(err.Error(), "已取消") {
		t.Fatalf("错误信息应说明是被取消：%v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("取消后仍阻塞了 %v", elapsed)
	}
}

// TestWaitReadyHonoursCancel 取消时不能等满 25 秒超时。
// 用一个永不产出日志的 lines 通道模拟「frpc 卡在登录阶段」。
func TestWaitReadyHonoursCancel(t *testing.T) {
	p := &frpcProc{lines: make(chan string)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := p.WaitReady(ctx, 25*time.Second)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("ctx 已取消时 WaitReady 应当返回错误")
	}
	if !strings.Contains(err.Error(), "已取消") {
		t.Fatalf("错误信息应说明是被取消：%v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("取消后仍等待了 %v（旧实现会等满 25s）", elapsed)
	}
}

// TestCleanupBandwidthTimesOut 退出流程不能被卡住的 worker 拖死：
// bwFinished 永不关闭时，CleanupBandwidth 必须在 bwCleanupWait 附近返回，
// 并把还没确认删除的隧道写进补偿文件，留到下次启动补删。
func TestCleanupBandwidthTimesOut(t *testing.T) {
	dir := withTempSettingsDir(t)

	a := &App{}
	a.bwFinished = make(chan struct{}) // 永不关闭 = worker 卡住
	a.bwResources = map[int]bwResource{
		42: {tunnelID: 42, token: "token"},
	}

	start := time.Now()
	a.CleanupBandwidth()
	elapsed := time.Since(start)

	// 允许少量调度误差，但绝不允许回到「等到 HTTP 超时」的量级
	if elapsed > bwCleanupWait+2*time.Second {
		t.Fatalf("CleanupBandwidth 耗时 %v，超过 %.0fs 上限", elapsed, (bwCleanupWait + 2*time.Second).Seconds())
	}
	if elapsed < bwCleanupWait-time.Second {
		t.Fatalf("CleanupBandwidth 耗时 %v，说明根本没等 worker 收尾", elapsed)
	}

	path := filepath.Join(dir, "CHMLFrp-SpeedTest", pendingTunnelsFile)
	list := readPendingTunnels(path)
	if len(list) != 1 || list[0].ID != 42 {
		t.Fatalf("补偿文件未记录残留隧道：%+v", list)
	}
}

// TestCleanupBandwidthFastPath worker 正常收尾时不应产生补偿记录，
// 也不该无谓地等满超时。
func TestCleanupBandwidthFastPath(t *testing.T) {
	dir := withTempSettingsDir(t)

	a := &App{}
	done := make(chan struct{})
	close(done) // 已经收尾
	a.bwFinished = done
	a.bwResources = map[int]bwResource{}

	start := time.Now()
	a.CleanupBandwidth()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("已收尾的清理不该等这么久：%v", elapsed)
	}

	path := filepath.Join(dir, "CHMLFrp-SpeedTest", pendingTunnelsFile)
	if list := readPendingTunnels(path); len(list) != 0 {
		t.Fatalf("不应对已收尾的轮次写补偿文件：%+v", list)
	}
}

// TestCleanupBandwidthIsIdempotent QuitApp 与 OnShutdown 都会调一次清理，
// 第二次必须立即返回，否则退出会白等两轮超时。
func TestCleanupBandwidthIsIdempotent(t *testing.T) {
	withTempSettingsDir(t)

	a := &App{}
	a.bwFinished = make(chan struct{}) // 卡住，第一轮会等满超时
	a.bwResources = map[int]bwResource{}
	a.CleanupBandwidth()

	start := time.Now()
	a.CleanupBandwidth()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("重复清理又等了一轮：%v", elapsed)
	}
}

// TestIsTunnelGoneErr 只有"确实不存在"才算补偿项已完成；
// 网络错误不能误判，否则隧道会被悄悄漏掉。
func TestIsTunnelGoneErr(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"隧道不存在", true},
		{"该隧道已删除", true},
		{"tunnel not found", true},
		{"request failed: context deadline exceeded", false},
		{"请求 CHMLFrp 接口失败: dial tcp: i/o timeout", false},
		{"接口返回 HTTP 500: internal error", false},
	}
	for _, c := range cases {
		if got := isTunnelGoneErr(errors.New(c.msg)); got != c.want {
			t.Errorf("isTunnelGoneErr(%q) = %v，期望 %v", c.msg, got, c.want)
		}
	}
	if isTunnelGoneErr(nil) {
		t.Error("nil 不应判为已删除")
	}
}
