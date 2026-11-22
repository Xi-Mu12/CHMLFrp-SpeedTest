package main

import (
	"net"
	"testing"
)

// TestIsPrivateIP 私网判定必须覆盖 IPv6：
// 漏判 ULA(fc00::/7) 会让界面允许去查一个根本没有公网归属地的地址，
// 用户只能拿到一条查询失败，而不是「这是内网地址」的明确提示。
func TestIsPrivateIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		// IPv4 私网 / 保留
		{"192.168.1.1", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"172.31.255.254", true},
		{"169.254.1.1", true},   // 链路本地
		{"100.64.0.1", true},    // 运营商级 NAT
		{"100.127.255.1", true}, // CGNAT 上界
		// IPv4 公网
		{"8.8.8.8", false},
		{"172.32.0.1", false},  // 出 172.16/12 的边界
		{"100.63.0.1", false},  // CGNAT 下界之外
		{"100.128.0.1", false}, // CGNAT 上界之外
		// IPv6 私网 / 保留
		{"fd00::1", true}, // ULA
		{"fc00::1", true}, // ULA
		{"fe80::1", true}, // 链路本地
		// IPv6 公网
		{"2408:8000::1", false},
		{"2001:4860:4860::8888", false},
		// 回环由 Loopback 字段单独表达，这里不视为 private
		{"127.0.0.1", false},
		{"::1", false},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("测试用例 IP 不合法：%s", c.ip)
		}
		if got := isPrivateIP(ip); got != c.want {
			t.Errorf("isPrivateIP(%s) = %v，期望 %v", c.ip, got, c.want)
		}
	}
	if isPrivateIP(nil) {
		t.Error("nil 不应判为内网地址")
	}
}
