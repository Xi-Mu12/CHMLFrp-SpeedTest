package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestBWServerThroughput 不经过隧道，直接对本地数据源打流，
// 验证 DN/UP 协议、双向计数与平均速率计算（回环带宽应远高于 50Mbps）。
func TestBWServerThroughput(t *testing.T) {
	srv, err := startBWServer()
	if err != nil {
		t.Fatalf("启动本地数据源失败: %v", err)
	}
	defer srv.Close()

	target := fmt.Sprintf("127.0.0.1:%d", srv.Port())
	a := &App{}
	start := time.Now()
	down, up, dPeak, uPeak, cancelled, err := a.runThroughput(0, NodeBasic{ID: 1, Name: "loopback"}, 0, 1, 2, target)
	if err != nil {
		t.Fatalf("打流失败: %v", err)
	}
	if cancelled {
		t.Fatalf("不应被取消")
	}
	elapsed := time.Since(start)
	t.Logf("耗时 %v 下行 %.1f Mbps(峰值 %.1f) 上行 %.1f Mbps(峰值 %.1f)", elapsed.Round(time.Millisecond), down, dPeak, up, uPeak)
	if down < 50 || up < 50 {
		t.Fatalf("回环带宽异常偏低: down=%.1f up=%.1f", down, up)
	}
	if elapsed < 1900*time.Millisecond {
		t.Fatalf("测试时长不足: %v", elapsed)
	}
}

// TestWriteFrpcIni 校验 ini 关键字段
func TestWriteFrpcIni(t *testing.T) {
	path := t.TempDir() + "/frpc.ini"
	if err := writeFrpcIni(path, "1.2.3.4", 7000, "usertoken", "nodetoken", "127.0.0.1", 50000, 23456, "bw1-1"); err != nil {
		t.Fatalf("写入 ini 失败: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"server_addr = 1.2.3.4", "server_port = 7000", "user = usertoken", "token = nodetoken", "local_port = 50000", "remote_port = 23456", "[bw1-1]"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("ini 缺少 %q:\n%s", want, data)
		}
	}
}

// TestBwTunnelName CHMLFrp 隧道名只允许数字/字母/下划线
func TestBwTunnelName(t *testing.T) {
	valid := regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)
	for _, id := range []int{1, 83, 12345} {
		name := bwTunnelName(id)
		if !valid.MatchString(name) {
			t.Fatalf("隧道名 %q 不符合 [A-Za-z0-9_] 规范", name)
		}
		t.Logf("节点 %d -> %s", id, name)
	}
}

// TestFrpcPlatformName 平台映射（本机应为 windows/amd64）
func TestFrpcPlatformName(t *testing.T) {
	name, err := frpcPlatformName()
	if err != nil {
		t.Skipf("当前平台不在官方下载表: %v", err)
	}
	t.Logf("当前平台 frpc: %s", name)
	if name == "" {
		t.Fatal("平台名称为空")
	}
}
