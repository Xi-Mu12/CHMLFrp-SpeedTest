package main

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"runtime"
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

// TestSessionSealRoundtrip 登录态加解密往返（Windows 走 DPAPI，其他平台透传）
func TestSessionSealRoundtrip(t *testing.T) {
	plain := []byte(`{"access_token":"a.b.c","refresh_token":"r.e.f","expires_at":1767225600}`)
	sealed, err := sealBytes(plain)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	back, err := openBytes(sealed)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if !bytes.Equal(back, plain) {
		t.Fatalf("往返数据不一致:\n got %s\nwant %s", back, plain)
	}
	if runtime.GOOS == "windows" && bytes.Equal(sealed, plain) {
		t.Fatal("Windows 上登录态未被加密")
	}
}

// TestSessionFileRoundtrip 落盘/读取往返（写到真实配置目录，用独立文件名避免污染）
func TestSessionFileRoundtrip(t *testing.T) {
	path, err := sessionFilePath()
	if err != nil {
		t.Skipf("无法定位配置目录: %v", err)
	}
	backup, backupErr := os.ReadFile(path)
	t.Cleanup(func() {
		if backupErr == nil {
			_ = os.WriteFile(path, backup, 0o600)
			return
		}
		_ = os.Remove(path)
	})
	_ = os.Remove(path)

	want := authSession{
		AccessToken: "access-1", RefreshToken: "refresh-1",
		ExpiresAt: 1767225600, Username: "tester", Email: "t@example.com",
	}
	if err := saveSession(want); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	got, err := loadSession()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got != want {
		t.Fatalf("往返不一致:\n got %+v\nwant %+v", got, want)
	}
	removeSessionFile()
	if _, err := loadSession(); err == nil {
		t.Fatal("删除后仍能读到登录态")
	}
}
