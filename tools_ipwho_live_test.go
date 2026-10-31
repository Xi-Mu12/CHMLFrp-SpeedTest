package main

import (
	"os"
	"testing"
)

// TestIPWhoLive 联网验证 ipwho.is 字段映射（会消耗接口额度，默认跳过）。
// 需要时：IPWHO_LIVE=1 go test -tags desktop,production -run TestIPWhoLive -v .
func TestIPWhoLive(t *testing.T) {
	if os.Getenv("IPWHO_LIVE") == "" {
		t.Skip("跳过联网测试，设置 IPWHO_LIVE=1 开启")
	}
	var a App
	geo, err := a.QueryIPInfo("8.8.8.8")
	if err != nil {
		t.Fatalf("QueryIPInfo 失败: %v", err)
	}
	t.Logf("IP=%s Type=%s Country=%s/%s Region=%s(%s) City=%s Zip=%s",
		geo.IP, geo.Type, geo.Country, geo.CountryCode, geo.Region, geo.RegionCode, geo.City, geo.Zip)
	t.Logf("Continent=%s(%s) Capital=%s CallingCode=+%s IsEU=%v Borders=%s",
		geo.Continent, geo.ContinentCode, geo.Capital, geo.CallingCode, geo.IsEU, geo.Borders)
	t.Logf("ASN=%s(%d) ISP=%s Org=%s Domain=%s", geo.ASN, geo.ASNNumber, geo.ISP, geo.Org, geo.Domain)
	t.Logf("TZ=%s %s UTC%s DST=%v Flag=%s %s Lat=%f Lon=%f",
		geo.TimeZone, geo.TimeZoneAbbr, geo.UTCOffset, geo.IsDST, geo.FlagEmoji, geo.FlagImg, geo.Latitude, geo.Longitude)

	if geo.IP != "8.8.8.8" {
		t.Errorf("IP 不符: %s", geo.IP)
	}
	if geo.CountryCode != "US" {
		t.Errorf("国家代码应为 US，实际 %s", geo.CountryCode)
	}
	if geo.ASN != "AS15169" {
		t.Errorf("ASN 应为 AS15169，实际 %s", geo.ASN)
	}
	if geo.Domain != "google.com" {
		t.Errorf("域名应为 google.com，实际 %s", geo.Domain)
	}
	if geo.Version != 4 {
		t.Errorf("version 应为 4，实际 %d", geo.Version)
	}

	self, err := a.GetMyIPInfo()
	if err != nil {
		t.Fatalf("GetMyIPInfo 失败: %v", err)
	}
	t.Logf("当前出口: %s %s %s %s %s %s", self.IP, self.Country, self.Region, self.City, self.ISP, self.ASN)
	if self.IP == "" {
		t.Error("当前出口 IP 为空")
	}

	// IPv6 地址也要能正常解析
	if v6, err := a.QueryIPInfo("2400:3200::1"); err != nil {
		t.Logf("IPv6 查询失败（可接受）: %v", err)
	} else {
		t.Logf("IPv6: %s %s %s version=%d type=%s", v6.IP, v6.Country, v6.ISP, v6.Version, v6.Type)
		if v6.Version != 6 {
			t.Errorf("IPv6 version 应为 6，实际 %d", v6.Version)
		}
	}

	// 内网地址在本地就被拦下，不发请求
	if _, err := a.QueryIPInfo("192.168.1.1"); err == nil {
		t.Error("内网地址应该报错")
	} else {
		t.Logf("内网拦截正常: %v", err)
	}
}
