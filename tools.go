// tools.go —— 本机 IP：网卡地址枚举 + 公网 IP 归属地查询
// 归属地查询走 ipwho.is（免费、无需密钥、1000 次/天、lang=zh-CN 中文返回），
// 打开页面即自动查询当前出口 IP。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// LocalIP 一个网卡地址（工具台「本机 IP」列表项）
type LocalIP struct {
	Iface    string `json:"iface"`
	IP       string `json:"ip"`
	Version  int    `json:"version"`  // 4 / 6
	Loopback bool   `json:"loopback"` // 回环地址
	Private  bool   `json:"private"`  // 内网地址（RFC1918 / CGNAT / 链路本地）
}

// isPrivateIPv4 判断是否内网/保留地址。IPv6 只做回环与链路本地区分。
func isPrivateIPv4(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	if v4[0] == 10 {
		return true
	}
	if v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31 {
		return true
	}
	if v4[0] == 192 && v4[1] == 168 {
		return true
	}
	if v4[0] == 169 && v4[1] == 254 { // 链路本地
		return true
	}
	if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 { // 运营商级 NAT
		return true
	}
	return false
}

// GetLocalIPs 枚举所有已启用网卡的 IP 地址。
// 排序：IPv4 优先 → 非回环优先 → 内网优先 → 网卡名，让最常用的地址排最前。
func (a *App) GetLocalIPs() ([]LocalIP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]LocalIP, 0, 8)
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue // 网卡未启用
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			default:
				continue
			}
			if ip == nil {
				continue
			}
			version := 4
			if ip.To4() == nil {
				version = 6
				if ip.IsLinkLocalUnicast() {
					continue // fe80:: 链路本地，对用户没有参考价值
				}
			}
			out = append(out, LocalIP{
				Iface:    iface.Name,
				IP:       ip.String(),
				Version:  version,
				Loopback: ip.IsLoopback(),
				Private:  isPrivateIPv4(ip),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Version != out[j].Version {
			return out[i].Version < out[j].Version
		}
		if out[i].Loopback != out[j].Loopback {
			return !out[i].Loopback
		}
		if out[i].Private != out[j].Private {
			return out[i].Private
		}
		return out[i].Iface < out[j].Iface
	})
	return out, nil
}

// IPGeo IP 地理位置与网络归属。数据来自 ipwho.is（lang=zh-CN 中文返回），无需密钥。
type IPGeo struct {
	IP            string  `json:"ip"`
	Version       int     `json:"version"` // 4 / 6
	Type          string  `json:"type"`    // 接口原样返回的 IPv4 / IPv6
	Continent     string  `json:"continent"`
	ContinentCode string  `json:"continentCode"`
	Country       string  `json:"country"`
	CountryCode   string  `json:"countryCode"`
	Region        string  `json:"region"`
	RegionCode    string  `json:"regionCode"`
	City          string  `json:"city"`
	Zip           string  `json:"zip"`
	Latitude      float64 `json:"latitude"`
	Longitude     float64 `json:"longitude"`
	IsEU          bool    `json:"isEU"`
	CallingCode   string  `json:"callingCode"`
	Capital       string  `json:"capital"`
	Borders       string  `json:"borders"`
	FlagEmoji     string  `json:"flagEmoji"`
	FlagImg       string  `json:"flagImg"`
	ASN           string  `json:"asn"`       // 形如 AS9808
	ASNNumber     int     `json:"asnNumber"` // 数字形式，便于排序或跳转
	ISP           string  `json:"isp"`
	Org           string  `json:"org"`
	Domain        string  `json:"domain"`
	TimeZone      string  `json:"timeZone"`     // 时区 ID，如 Asia/Shanghai
	TimeZoneAbbr  string  `json:"timeZoneAbbr"` // 时区缩写，如 CST
	UTCOffset     string  `json:"utcOffset"`    // 相对 UTC 偏移，如 +08:00
	IsDST         bool    `json:"isDST"`        // 是否处于夏令时
}

// ipWhoResp ipwho.is 原始返回。字段层级：顶级是地理信息，
// 网络与时间信息分别嵌套在 connection / timezone 里，flag 是旗帜资源。
// 失败时 success=false + message，HTTP 仍可能是 200。
type ipWhoResp struct {
	IP            string  `json:"ip"`
	Success       bool    `json:"success"`
	Message       string  `json:"message"`
	Type          string  `json:"type"`
	Continent     string  `json:"continent"`
	ContinentCode string  `json:"continent_code"`
	Country       string  `json:"country"`
	CountryCode   string  `json:"country_code"`
	Region        string  `json:"region"`
	RegionCode    string  `json:"region_code"`
	City          string  `json:"city"`
	Latitude      float64 `json:"latitude"`
	Longitude     float64 `json:"longitude"`
	IsEU          bool    `json:"is_eu"`
	Postal        string  `json:"postal"`
	CallingCode   string  `json:"calling_code"`
	Capital       string  `json:"capital"`
	Borders       string  `json:"borders"`
	Flag          struct {
		Img   string `json:"img"`
		Emoji string `json:"emoji"`
	} `json:"flag"`
	Connection struct {
		ASN    int    `json:"asn"`
		Org    string `json:"org"`
		ISP    string `json:"isp"`
		Domain string `json:"domain"`
	} `json:"connection"`
	TimeZone struct {
		ID     string `json:"id"`
		Abbr   string `json:"abbr"`
		IsDST  bool   `json:"is_dst"`
		Offset int    `json:"offset"`
		UTC    string `json:"utc"`
	} `json:"timezone"`
}

// ipWhoEndpoint 免密钥入口，免费额度 1000 次/天（按出口 IP 计）。
// lang=zh-CN 让国家/省市/首都/大洲返回中文。
const ipWhoEndpoint = "https://ipwho.is/"

// ipWhoFailMsg 把接口的英文失败原因翻成中文。
func ipWhoFailMsg(msg string) string {
	switch strings.ToLower(strings.TrimSpace(msg)) {
	case "invalid ip address", "invalid query":
		return "IP 地址格式不正确"
	case "reserved range", "private range":
		return "该地址属于内网/保留网段，没有公网归属地"
	case "rate limit exceeded":
		return "已达到免费额度上限（1000 次/天），请稍后再试"
	default:
		if msg == "" {
			return "接口未返回可用的归属地数据"
		}
		return "查询失败：" + msg
	}
}

// fetchIPGeo 请求 ipwho.is；path 为空表示查当前出口 IP，否则为 "{ip}"。
func (a *App) fetchIPGeo(path string) (IPGeo, error) {
	ctx := context.Background()
	if a != nil && a.ctx != nil {
		ctx = a.ctx
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ipWhoEndpoint+path+"?lang=zh-CN", nil)
	if err != nil {
		return IPGeo{}, err
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return IPGeo{}, fmt.Errorf("请求 ipwho.is 失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return IPGeo{}, fmt.Errorf("已达到免费额度上限（1000 次/天，按出口 IP 计），请稍后再试")
	}
	if resp.StatusCode != http.StatusOK {
		return IPGeo{}, fmt.Errorf("ipwho.is 返回 HTTP %d", resp.StatusCode)
	}

	var r ipWhoResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return IPGeo{}, fmt.Errorf("解析 ipwho.is 返回失败：%v", err)
	}
	if !r.Success || r.IP == "" {
		return IPGeo{}, fmt.Errorf("%s", ipWhoFailMsg(r.Message))
	}

	version := 4
	if parsed := net.ParseIP(r.IP); parsed != nil && parsed.To4() == nil {
		version = 6
	}
	asn := ""
	if r.Connection.ASN > 0 {
		asn = fmt.Sprintf("AS%d", r.Connection.ASN)
	}
	return IPGeo{
		IP:            r.IP,
		Version:       version,
		Type:          r.Type,
		Continent:     r.Continent,
		ContinentCode: r.ContinentCode,
		Country:       r.Country,
		CountryCode:   r.CountryCode,
		Region:        r.Region,
		RegionCode:    r.RegionCode,
		City:          r.City,
		Zip:           r.Postal,
		Latitude:      r.Latitude,
		Longitude:     r.Longitude,
		IsEU:          r.IsEU,
		CallingCode:   r.CallingCode,
		Capital:       r.Capital,
		Borders:       r.Borders,
		FlagEmoji:     r.Flag.Emoji,
		FlagImg:       r.Flag.Img,
		ASN:           asn,
		ASNNumber:     r.Connection.ASN,
		ISP:           r.Connection.ISP,
		Org:           r.Connection.Org,
		Domain:        r.Connection.Domain,
		TimeZone:      r.TimeZone.ID,
		TimeZoneAbbr:  r.TimeZone.Abbr,
		UTCOffset:     r.TimeZone.UTC,
		IsDST:         r.TimeZone.IsDST,
	}, nil
}

// GetMyIPInfo 自动查询当前出口公网 IP 的信息（中文地理位置 / 运营商 / ASN）。
func (a *App) GetMyIPInfo() (IPGeo, error) {
	return a.fetchIPGeo("")
}

// QueryIPInfo 查询指定 IP 的信息，数据源同为 ipwho.is（无需密钥）。
func (a *App) QueryIPInfo(ip string) (IPGeo, error) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return IPGeo{}, fmt.Errorf("请输入要查询的 IP 地址")
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return IPGeo{}, fmt.Errorf("IP 格式不正确：%s", ip)
	}
	if parsed.IsLoopback() || (parsed.To4() != nil && isPrivateIPv4(parsed)) {
		return IPGeo{}, fmt.Errorf("%s 是内网/回环地址，没有公网归属地信息", ip)
	}
	// 注意不能带尾斜杠：ipwho.is 会把 "1.2.3.4/" 当成非法查询串
	return a.fetchIPGeo(url.PathEscape(ip))
}
