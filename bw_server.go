// bw_server.go —— 带宽测速的本地回环数据源
//
// 思路：frpc 把节点上的 remote_port 映射到本机 127.0.0.1:<port>，本文件就是那个
// 本地端点。测速客户端连 节点公网地址:remote_port 后，数据实际是
//   客户端(本机) → 节点 → frpc → 本机回环服务
// 所以上下行各穿一次互联网链路，测出来的就是这条节点通路的真实吞吐。
//
// 协议（一行文本头，随后是裸数据）：
//   DN <毫秒>   服务端按指定时长持续发送数据，然后关闭连接
//   UP <毫秒>   客户端持续发送数据，服务端读取丢弃，结束后回 "OK\n"
package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	bwServeWriteChunk = 128 * 1024
	bwHeaderTimeout   = 8 * time.Second
)

type bwServer struct {
	ln     net.Listener
	closed sync.Once

	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func startBWServer() (*bwServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &bwServer{ln: ln, conns: make(map[net.Conn]struct{})}
	go s.acceptLoop()
	return s, nil
}

func (s *bwServer) Port() int {
	if addr, ok := s.ln.Addr().(*net.TCPAddr); ok {
		return addr.Port
	}
	return 0
}

func (s *bwServer) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetNoDelay(true)
		}
		s.mu.Lock()
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		go s.handle(conn)
	}
}

func (s *bwServer) handle(conn net.Conn) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		_ = conn.Close()
	}()

	_ = conn.SetReadDeadline(time.Now().Add(bwHeaderTimeout))
	r := bufio.NewReaderSize(conn, 8*1024)
	line, err := r.ReadString('\n')
	if err != nil {
		return
	}
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 2 {
		return
	}
	durMs, err := strconv.Atoi(fields[1])
	if err != nil || durMs <= 0 || durMs > 300000 {
		return
	}
	dur := time.Duration(durMs) * time.Millisecond
	deadline := time.Now().Add(dur)

	switch strings.ToUpper(fields[0]) {
	case "DN":
		_ = conn.SetWriteDeadline(time.Now().Add(dur + 5*time.Second))
		buf := make([]byte, bwServeWriteChunk)
		// 填点非零数据，避免极端情况下被中间设备过度优化
		for i := 0; i < len(buf); i += 4096 {
			buf[i] = 0x5a
		}
		for time.Now().Before(deadline) {
			if _, werr := conn.Write(buf); werr != nil {
				return
			}
		}
	case "UP":
		_ = conn.SetReadDeadline(time.Now().Add(dur + 5*time.Second))
		_, _ = io.Copy(io.Discard, r)
		_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_, _ = conn.Write([]byte("OK\n"))
	default:
		_, _ = fmt.Fprintf(conn, "ERR unknown mode\n")
	}
}

// Close 关闭监听并断开所有活动连接（测速结束/取消时调用）
func (s *bwServer) Close() {
	if s == nil {
		return
	}
	s.closed.Do(func() {
		_ = s.ln.Close()
		s.mu.Lock()
		for c := range s.conns {
			_ = c.Close()
		}
		s.conns = map[net.Conn]struct{}{}
		s.mu.Unlock()
	})
}
