package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseMCAddress(t *testing.T) {
	cases := []struct {
		raw  string
		kind string
		host string
		port int
		exp  bool
	}{
		{"play.example.com", mcKindJava, "play.example.com", 25565, false},
		{"play.example.com:25566", mcKindJava, "play.example.com", 25566, true},
		{"play.example.com", mcKindBE, "play.example.com", 19132, false},
		{"minecraft://mc.example.com:25565/some/path", mcKindJava, "mc.example.com", 25565, true},
		{"  mc.example.com:19132  ", mcKindBE, "mc.example.com", 19132, true},
		{"[::1]:25565", mcKindJava, "::1", 25565, true},
		{"::1", mcKindJava, "::1", 25565, false},
		{"mc.example.com:", mcKindBE, "mc.example.com", 19132, false},
	}
	for _, c := range cases {
		host, port, exp, err := parseMCAddress(c.raw, c.kind)
		if err != nil {
			t.Fatalf("%q 解析失败：%v", c.raw, err)
		}
		if host != c.host || port != c.port || exp != c.exp {
			t.Fatalf("%q => %s:%d exp=%v，期望 %s:%d exp=%v", c.raw, host, port, exp, c.host, c.port, c.exp)
		}
	}

	if _, _, _, err := parseMCAddress("   ", mcKindJava); err == nil {
		t.Fatal("空地址应报错")
	}
	if _, _, _, err := parseMCAddress("host:99999", mcKindJava); err == nil {
		t.Fatal("越界端口应报错")
	}
	if _, _, _, err := parseMCAddress("host:abc", mcKindJava); err == nil {
		t.Fatal("非数字端口应报错")
	}
}

// encodeJavaStatus 按协议组一个 status 响应包。
func encodeJavaStatus(payload string) []byte {
	var p bytes.Buffer
	writeMCVarInt(&p, 0x00)
	writeMCString(&p, payload)
	var out bytes.Buffer
	writeMCVarInt(&out, p.Len())
	out.Write(p.Bytes())
	return out.Bytes()
}

func TestFillJavaStatus(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	status := `{
		"version": {"name": "1.21.4", "protocol": 769},
		"players": {"max": 100, "online": 7, "sample": [{"name": "Steve"}, {"name": "Alex"}]},
		"description": {"text": "Hello ", "color": "gold", "extra": [{"text": "World", "color": "red", "bold": true}]},
		"favicon": "data:image/png;base64,AAAA",
		"enforcesSecureChat": true
	}`
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 1024)
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = conn.Read(buf)
		_, _ = conn.Write(encodeJavaStatus(status))
	}()

	addr := ln.Addr().(*net.TCPAddr)
	info := &MCServerInfo{Host: "127.0.0.1", Port: addr.Port}
	if err := fillJavaStatus(info); err != nil {
		t.Fatal(err)
	}

	if info.Version != "1.21.4" || info.Protocol != 769 {
		t.Fatalf("版本信息不对：%+v", info)
	}
	if info.Online != 7 || info.Max != 100 {
		t.Fatalf("人数不对：%d/%d", info.Online, info.Max)
	}
	if len(info.Players) != 2 || info.Players[0] != "Steve" {
		t.Fatalf("玩家示例不对：%v", info.Players)
	}
	if !strings.Contains(info.Motd, "Hello ") || !strings.Contains(info.Motd, "World") {
		t.Fatalf("MOTD 文本丢失：%q", info.Motd)
	}
	// gold = §6，red = §c，bold = §l
	if !strings.Contains(info.Motd, "\u00a76") || !strings.Contains(info.Motd, "\u00a7c\u00a7l") {
		t.Fatalf("MOTD 样式码不对：%q", info.Motd)
	}
	if info.Favicon != "data:image/png;base64,AAAA" {
		t.Fatalf("图标未保留：%q", info.Favicon)
	}
	if !info.SecureChat {
		t.Fatal("enforcesSecureChat 未解析")
	}
	// 本地回环往返不足 1ms，按毫秒取整就是 0，只断言不是负数
	if info.Latency < 0 {
		t.Fatalf("延迟异常：%d", info.Latency)
	}
}

func TestFillJavaStatusRejectsGarbage(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 1024)
		_, _ = conn.Read(buf)
		_, _ = conn.Write(encodeJavaStatus("not-a-json"))
	}()

	addr := ln.Addr().(*net.TCPAddr)
	info := &MCServerInfo{Host: "127.0.0.1", Port: addr.Port}
	if err := fillJavaStatus(info); err == nil {
		t.Fatal("非 JSON 响应应该报错")
	}
}

func TestFillBedrockStatus(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()

	payload := "MCPE;Bedrock Test;712;1.21.0;5;20;123456;Second Line;Survival;1;19132;19133;"
	go func() {
		buf := make([]byte, 1024)
		pc.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, addr, err := pc.ReadFrom(buf)
		if err != nil || n < 33 {
			return
		}
		if buf[0] != 0x01 || !bytes.Equal(buf[9:25], raknetMagic) {
			return
		}
		resp := make([]byte, 35+len(payload))
		resp[0] = 0x1c
		copy(resp[9:17], buf[1:9])
		copy(resp[17:33], raknetMagic)
		binary.BigEndian.PutUint16(resp[33:35], uint16(len(payload)))
		copy(resp[35:], payload)
		_, _ = pc.WriteTo(resp, addr)
	}()

	port := pc.LocalAddr().(*net.UDPAddr).Port
	info := &MCServerInfo{Host: "127.0.0.1", Port: port}
	if err := fillBedrockStatus(info); err != nil {
		t.Fatal(err)
	}

	if info.Version != "1.21.0" || info.Protocol != 712 {
		t.Fatalf("版本信息不对：%+v", info)
	}
	if info.Online != 5 || info.Max != 20 {
		t.Fatalf("人数不对：%d/%d", info.Online, info.Max)
	}
	if info.GameMode != "Survival" {
		t.Fatalf("游戏模式不对：%q", info.GameMode)
	}
	if !strings.Contains(info.Motd, "Bedrock Test") || !strings.Contains(info.Motd, "Second Line") {
		t.Fatalf("MOTD 不对：%q", info.Motd)
	}
	if info.Software != "MCPE" {
		t.Fatalf("引擎标识不对：%q", info.Software)
	}
}

func TestChatToLegacy(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`"plain"`, "\u00a7rplain"},
		{`{"text":"a","color":"red"}`, "\u00a7r\u00a7ca"},
		{`{"text":"a","extra":[{"text":"b"}]}`, "\u00a7rab"}, // 子组件继承父样式，不需要重发样式码
		{`{"text":"a","extra":[{"text":"b","color":"red"}]}`, "\u00a7ra\u00a7r\u00a7cb"},
		{`["x","y"]`, "\u00a7rxy"}, // 同样式相邻片段合并，不重复写样式码
		{``, ""},
	}
	for _, c := range cases {
		if got := chatToLegacy(json.RawMessage(c.raw)); got != c.want {
			t.Fatalf("%s => %q，期望 %q", c.raw, got, c.want)
		}
	}
}

func TestParseMCAddressKeepsPort(t *testing.T) {
	host, port, explicit, err := parseMCAddress("mc.example.com:19132", mcKindBE)
	if err != nil {
		t.Fatal(err)
	}
	if !explicit {
		t.Fatal("写了端口应标记 explicit")
	}
	if host != "mc.example.com" || port != 19132 {
		t.Fatalf("解析结果不对：%s:%s", host, strconv.Itoa(port))
	}
}

func TestStripMCCodes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\u00a7f\u00a7f\u00a7f\u00a7fWe support: 1.20-1.21", "We support: 1.20-1.21"},
		{"\u00a74\u00a7k||\u00a79\u00a7lCubeCraft\u00a74\u00a7k||", "||CubeCraft||"},
		{"\u00a7x\u00a7F\u00a7F\u00a7A\u00a7A\u00a70\u00a70hex", "hex"},
		{"plain", "plain"},
		{"tail\u00a7", "tail"},
		{"  \u00a7a  ", ""},
	}
	for _, c := range cases {
		if got := stripMCCodes(c.in); got != c.want {
			t.Fatalf("stripMCCodes(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFillJavaStatusStripsCodesFromPlainFields(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// 复刻 CubeCraft：版本名和玩家示例里都塞了 § 码，MOTD 也该保留 § 码。
	status := `{
		"version": {"name": "\u00a7f\u00a7f\u00a7f\u00a7fWe support: 1.20-1.21", "protocol": 340},
		"players": {"max": 5000, "online": 656, "sample": [
			{"name": "\u00a74\u00a7k||\u00a79\u00a7lCubeCraft\u00a74\u00a7k||"},
			{"name": "\u00a76Forums \u00a7f> cubecraft.net"}
		]},
		"description": {"text": "CubeCraft", "color": "gold"}
	}`
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 1024)
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = conn.Read(buf)
		_, _ = conn.Write(encodeJavaStatus(status))
	}()

	addr := ln.Addr().(*net.TCPAddr)
	info := &MCServerInfo{Host: "127.0.0.1", Port: addr.Port}
	if err := fillJavaStatus(info); err != nil {
		t.Fatal(err)
	}

	if info.Version != "We support: 1.20-1.21" {
		t.Fatalf("版本名应剥掉格式码：%q", info.Version)
	}
	if len(info.Players) != 2 || info.Players[0] != "||CubeCraft||" || info.Players[1] != "Forums > cubecraft.net" {
		t.Fatalf("玩家示例应剥掉格式码：%v", info.Players)
	}
	if !strings.Contains(info.Motd, "\u00a76") {
		t.Fatalf("MOTD 必须保留格式码供前端上色：%q", info.Motd)
	}
}
