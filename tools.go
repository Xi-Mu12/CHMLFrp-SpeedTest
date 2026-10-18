// tools.go —— 本机 IP：网卡地址枚举 + 公网 IP 归属地查询
// 归属地查询走 ip-api.com（免费、无需密钥、约 45 次/分钟、中文返回），
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

// IPGeo IP 地理位置与网络归属。数据来自 ip-api.com（中文返回），无需密钥。
type IPGeo struct {
	IP          string  `json:"ip"`
	Version     int     `json:"version"` // 4 / 6
	Country     string  `json:"country"`
	CountryCode string  `json:"countryCode"`
	Region      string  `json:"region"`
	City        string  `json:"city"`
	Zip         string  `json:"zip"`
	ISP         string  `json:"isp"`
	Org         string  `json:"org"`
	AS          string  `json:"as"`
	TimeZone    string  `json:"timeZone"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
}

// ipAPIResp ip-api.com 原始返回。失败时 status=fail + message，HTTP 仍为 200。
type ipAPIResp struct {
	Status      string  `json:"status"`
	Message     string  `json:"message"`
	Country     string  `json:"country"`
	CountryCode string  `json:"countryCode"`
	Region      string  `json:"region"`
	RegionName  string  `json:"regionName"`
	City        string  `json:"city"`
	Zip         string  `json:"zip"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	TimeZone    string  `json:"timezone"`
	ISP         string  `json:"isp"`
	Org         string  `json:"org"`
	AS          string  `json:"as"`
	Query       string  `json:"query"`
}

// ipAPIEndpoint 免费版入口。https 需付费，免费版只提供 http；
// lang=zh-CN 让国家/省市返回中文。额度约 45 次/分钟，无密钥。
const ipAPIEndpoint = "http://ip-api.com/json/"

// ipAPIFailMsg 把接口的英文失败原因翻成中文。
func ipAPIFailMsg(msg string) string {
	switch strings.ToLower(strings.TrimSpace(msg)) {
	case "private range":
		return "该地址属于内网（私有）网段，没有公网归属地"
	case "reserved range":
		return "该地址属于保留网段，没有公网归属地"
	case "invalid query":
		return "IP 地址格式不正确"
	case "quota exceeded":
		return "已达到免费额度上限（约 45 次/分钟），请稍后再试"
	default:
		if msg == "" {
			return "接口未返回可用的归属地数据"
		}
		return "查询失败：" + msg
	}
}

// fetchIPGeo 请求 ip-api.com；path 为空表示查当前出口 IP，否则为 "{ip}"。
func (a *App) fetchIPGeo(path string) (IPGeo, error) {
	ctx := context.Background()
	if a != nil && a.ctx != nil {
		ctx = a.ctx
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ipAPIEndpoint+path+"?lang=zh-CN", nil)
	if err != nil {
		return IPGeo{}, err
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return IPGeo{}, fmt.Errorf("请求 ip-api.com 失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return IPGeo{}, fmt.Errorf("已达到免费额度上限（约 45 次/分钟），请稍后再试")
	}
	if resp.StatusCode != http.StatusOK {
		return IPGeo{}, fmt.Errorf("ip-api.com 返回 HTTP %d", resp.StatusCode)
	}

	var r ipAPIResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return IPGeo{}, fmt.Errorf("解析 ip-api.com 返回失败：%v", err)
	}
	if r.Status != "success" || r.Query == "" {
		return IPGeo{}, fmt.Errorf("%s", ipAPIFailMsg(r.Message))
	}

	version := 4
	if parsed := net.ParseIP(r.Query); parsed != nil && parsed.To4() == nil {
		version = 6
	}
	return IPGeo{
		IP:          r.Query,
		Version:     version,
		Country:     r.Country,
		CountryCode: r.CountryCode,
		Region:      r.RegionName,
		City:        r.City,
		Zip:         r.Zip,
		ISP:         r.ISP,
		Org:         r.Org,
		AS:          r.AS,
		TimeZone:    r.TimeZone,
		Latitude:    r.Lat,
		Longitude:   r.Lon,
	}, nil
}

// GetMyIPInfo 自动查询当前出口公网 IP 的信息（中文地理位置 / 运营商 / ASN）。
func (a *App) GetMyIPInfo() (IPGeo, error) {
	return a.fetchIPGeo("")
}

// QueryIPInfo 查询指定 IP 的信息，数据源同为 ip-api.com（无需密钥）。
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
	// 注意不能带尾斜杠：ip-api 会把 "1.2.3.4/" 当成非法查询串
	return a.fetchIPGeo(url.PathEscape(ip))
}
