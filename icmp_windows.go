//go:build windows

// icmp_windows.go —— ICMP 活性探测（Windows）
// TCPing 端口被防火墙 DROP 时，用 ICMP 区分"节点离线"与"节点在线但端口被拦"。
// 走 iphlpapi/icmp.dll 的 IcmpSendEcho 系统调用：无需管理员权限（raw socket 在
// Windows 上要 SIO_RCVALL 特权），也不依赖解析系统 ping 的本地化输出。
package main

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"
	"unsafe"
)

var (
	modIcmp             = syscall.NewLazyDLL("icmp.dll")
	procIcmpCreateFile  = modIcmp.NewProc("IcmpCreateFile")
	procIcmpCloseHandle = modIcmp.NewProc("IcmpCloseHandle")
	procIcmpSendEcho    = modIcmp.NewProc("IcmpSendEcho")
)

// icmpPing 发一次 ICMP Echo，返回 RTT。
// ICMP_ECHO_REPLY 结构按 x64 对齐很微妙，这里不整体映射结构体，只读前 12 字节的
// 稳定布局：offset 0 Address(uint32) / 4 Status(uint32) / 8 RoundTripTime(uint32, ms)。
// 官方要求 replySize >= sizeof(ICMP_ECHO_REPLY)+payload+8，给足余量即可。
func icmpPing(host string, timeout time.Duration) (time.Duration, error) {
	// 目标必须是 IPv4；传入域名时先解析（一般到不了这里，预解析已把域名转成 IP）
	var v4 net.IP
	if ip := net.ParseIP(host); ip != nil {
		v4 = ip.To4()
	} else if addrs, err := net.LookupHost(host); err == nil {
		for _, a := range addrs {
			if p := net.ParseIP(a).To4(); p != nil {
				v4 = p
				break
			}
		}
	}
	if v4 == nil {
		return 0, errors.New("目标没有 IPv4 地址，无法 ICMP")
	}

	h, _, err := procIcmpCreateFile.Call()
	if h == 0 || h == ^uintptr(0) { // INVALID_HANDLE_VALUE
		return 0, fmt.Errorf("IcmpCreateFile 失败: %v", err)
	}
	defer procIcmpCloseHandle.Call(h)

	payload := []byte("chmlfrp-speedtest")
	// IPAddr：网络字节序的 ULONG。v4 的 4 字节本身就是网络序（大端排列），
	// 直接按内存原样拷进 uint32 即可；若用 BigEndian.Uint32 取"数值"再传，
	// 小端机器会把 4 字节前后颠倒（58.35.129.62 变成 62.129.35.58）。
	var ipAddr uint32
	copy((*[4]byte)(unsafe.Pointer(&ipAddr))[:], v4)
	buf := make([]byte, 200)
	start := time.Now()
	ret, _, callErr := procIcmpSendEcho.Call(
		h,
		uintptr(ipAddr),
		uintptr(unsafe.Pointer(&payload[0])),
		uintptr(len(payload)),
		0, // IP_OPTION_INFORMATION，用默认 TTL
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		uintptr(timeout.Milliseconds()),
	)
	if ret == 0 {
		if code := syscall.Errno(callErr.(syscall.Errno)); code != 0 {
			return 0, fmt.Errorf("IcmpSendEcho 失败(winerr %d): %v", int(code), code)
		}
		return 0, errors.New("ICMP 超时")
	}
	status := *(*uint32)(unsafe.Pointer(&buf[4]))
	if status != 0 { // 0 = IP_SUCCESS
		return 0, fmt.Errorf("ICMP 状态码 %d", status)
	}
	rttMs := *(*uint32)(unsafe.Pointer(&buf[8]))
	if rttMs == 0 {
		// 同网段可能取整成 0ms，用本地计时兜底，避免前端显示 0 引误解
		return time.Since(start), nil
	}
	return time.Duration(rttMs) * time.Millisecond, nil
}
