package main

import (
	"os"
	"testing"
)

// 真实网络联调用例，默认跳过；MC_LIVE=1 go test -run TestLive -v .
func TestLiveJava(t *testing.T) {
	if os.Getenv("MC_LIVE") == "" {
		t.Skip("需要 MC_LIVE=1 才跑真实网络")
	}
	info, err := (&App{}).PingMCServer("mc.hypixel.net", "java")
	if err != nil {
		t.Fatalf("java: %v", err)
	}
	t.Logf("java => %s:%d srv=%v version=%q protocol=%d online=%d/%d latency=%dms motd=%q players=%v icon=%d bytes secure=%v",
		info.Host, info.Port, info.SRV, info.Version, info.Protocol, info.Online, info.Max, info.Latency, info.Motd, info.Players, len(info.Favicon), info.SecureChat)
}

func TestLiveBedrock(t *testing.T) {
	if os.Getenv("MC_LIVE") == "" {
		t.Skip("需要 MC_LIVE=1 才跑真实网络")
	}
	info, err := (&App{}).PingMCServer("play.nethergames.org:19132", "be")
	if err != nil {
		t.Fatalf("bedrock: %v", err)
	}
	t.Logf("be => %s:%d engine=%q version=%q protocol=%d online=%d/%d latency=%dms mode=%q motd=%q",
		info.Host, info.Port, info.Software, info.Version, info.Protocol, info.Online, info.Max, info.Latency, info.GameMode, info.Motd)
}
