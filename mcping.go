// mcping.go —— Minecraft 服务器状态查询
// Java 版走 Server List Ping（TCP + JSON status），基岩版走 RakNet Unconnected Ping（UDP）。
// 两者都是纯协议实现，不依赖任何第三方库，也不走系统代理（直连）。
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	mcKindJava = "java"
	mcKindBE   = "be"

	mcJavaDefaultPort = 25565
	mcBEDefaultPort   = 19132

	// 单次查询的硬超时。Java 握手只有一次往返，基岩是单包 UDP，
	// 6 秒足够跨洋；再长用户只会以为卡死。
	mcPingTimeout = 6 * time.Second
	// 服务端返回的包长度上限，防止恶意/损坏数据让我们分配几个 G。
	mcMaxPacket = 2 << 20
	// 玩家示例最多展示这么多个，避免大服返回几百个把界面撑爆。
	mcMaxSamplePlayers = 12
)

// RakNet 未连接 ping/pong 的魔数，所有基岩服务端都认这个固定值。
var raknetMagic = []byte{
	0x00, 0xff, 0xff, 0x00, 0xfe, 0xfe, 0xfe, 0xfe,
	0xfd, 0xfd, 0xfd, 0xfd, 0x12, 0x34, 0x56, 0x78,
}

// MCServerInfo 一次服务器查询的结果。
type MCServerInfo struct {
	Kind       string   `json:"kind"`     // java | be
	Address    string   `json:"address"`  // 用户原始输入
	Host       string   `json:"host"`     // 实际连接的主机（SRV 解析后可能不同）
	Port       int      `json:"port"`     // 实际连接的端口
	SRV        bool     `json:"srv"`      // 是否通过 SRV 记录解析到
	Motd       string   `json:"motd"`     // 传统 § 格式文本，可能含换行
	Version    string   `json:"version"`  // 版本名，如 1.21.4 / Paper 1.20.1
	Protocol   int      `json:"protocol"` // 协议号
	Online     int      `json:"online"`
	Max        int      `json:"max"`
	Latency    int      `json:"latencyMs"` // 往返耗时（含建连）
	Favicon    string   `json:"favicon"`   // Java 版图标，data:image/png;base64,...
	Map        string   `json:"map,omitempty"`
	GameMode   string   `json:"gameMode,omitempty"` // 基岩版游戏模式
	Software   string   `json:"software,omitempty"` // 基岩版引擎标识（如 MCPE）
	Players    []string `json:"players,omitempty"`  // Java 版玩家示例
	SecureChat bool     `json:"secureChat,omitempty"`
}

// PingMCServer 查询服务器状态。kind 取 "java" 或 "be"，其他值按 java 处理。
func (a *App) PingMCServer(address, kind string) (*MCServerInfo, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != mcKindBE {
		kind = mcKindJava
	}
	host, port, explicitPort, err := parseMCAddress(address, kind)
	if err != nil {
		return nil, err
	}

	info := &MCServerInfo{
		Kind:    kind,
		Address: strings.TrimSpace(address),
		Host:    host,
		Port:    port,
	}

	// Java 版：用户没写端口时，官方客户端会先查 _minecraft._tcp SRV 记录。
	// 不查的话，很多 "play.example.com" 这种只配了 SRV 的服会连到 25565 失败。
	if kind == mcKindJava && !explicitPort && net.ParseIP(host) == nil {
		if srvHost, srvPort, ok := lookupMinecraftSRV(host); ok {
			info.Host = srvHost
			info.Port = srvPort
			info.SRV = true
		}
	}

	if kind == mcKindBE {
		err = fillBedrockStatus(info)
	} else {
		err = fillJavaStatus(info)
	}
	if err != nil {
		return nil, err
	}
	return info, nil
}

// parseMCAddress 解析 "host" / "host:port" / "[::1]:port" / "minecraft://host:port/path"。
// explicitPort 表示用户自己写了端口（此时不查 SRV）。
func parseMCAddress(raw, kind string) (host string, port int, explicitPort bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0, false, errors.New("请先填写服务器地址")
	}
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
	}
	if i := strings.IndexByte(raw, '/'); i >= 0 {
		raw = raw[:i]
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0, false, errors.New("请先填写服务器地址")
	}

	if h, p, splitErr := net.SplitHostPort(raw); splitErr == nil {
		if strings.TrimSpace(p) == "" {
			return h, defaultMCPort(kind), false, nil
		}
		n, convErr := strconv.Atoi(strings.TrimSpace(p))
		if convErr != nil || n <= 0 || n > 65535 {
			return "", 0, false, fmt.Errorf("端口 %q 不合法，应为 1~65535", p)
		}
		return h, n, true, nil
	}

	// 裸 IPv6 字面量（如 ::1）会走到这里，SplitHostPort 拆不开
	if strings.Count(raw, ":") > 1 {
		return strings.Trim(raw, "[]"), defaultMCPort(kind), false, nil
	}
	return raw, defaultMCPort(kind), false, nil
}

func defaultMCPort(kind string) int {
	if kind == mcKindBE {
		return mcBEDefaultPort
	}
	return mcJavaDefaultPort
}

// lookupMinecraftSRV 查 _minecraft._tcp.<host>，取优先级最高、权重最大的那条。
func lookupMinecraftSRV(host string) (string, int, bool) {
	_, recs, err := net.LookupSRV("minecraft", "tcp", host)
	if err != nil || len(recs) == 0 {
		return "", 0, false
	}
	best := recs[0]
	for _, r := range recs[1:] {
		if r.Priority < best.Priority || (r.Priority == best.Priority && r.Weight > best.Weight) {
			best = r
		}
	}
	target := strings.TrimSuffix(strings.TrimSpace(best.Target), ".")
	if target == "" || best.Port == 0 {
		return "", 0, false
	}
	return target, int(best.Port), true
}

// ---------- Java 版：Server List Ping ----------

func fillJavaStatus(info *MCServerInfo) error {
	addr := net.JoinHostPort(info.Host, strconv.Itoa(info.Port))
	start := time.Now()
	conn, err := (&net.Dialer{Timeout: mcPingTimeout}).Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("连接 %s 失败：%s", addr, mcNetErrText(err))
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(mcPingTimeout))

	// handshake（next state = 1 status）+ status request 一次写出，省一次往返
	var payload bytes.Buffer
	writeMCVarInt(&payload, 0x00)
	writeMCVarInt(&payload, -1) // 协议版本未知；status 阶段服务端不做校验
	writeMCString(&payload, info.Host)
	var portBuf [2]byte
	binary.BigEndian.PutUint16(portBuf[:], uint16(info.Port))
	payload.Write(portBuf[:])
	writeMCVarInt(&payload, 0x01)

	var packet bytes.Buffer
	writeMCVarInt(&packet, payload.Len())
	packet.Write(payload.Bytes())
	packet.Write([]byte{0x01, 0x00}) // status request：长度 1、包 ID 0

	if _, err := conn.Write(packet.Bytes()); err != nil {
		return fmt.Errorf("发送查询请求失败：%s", mcNetErrText(err))
	}

	r := bufio.NewReader(conn)
	length, err := readMCVarInt(r)
	if err != nil {
		return fmt.Errorf("读取响应失败：%s", mcNetErrText(err))
	}
	if length <= 0 || length > mcMaxPacket {
		return errors.New("服务器返回的包长度异常")
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return fmt.Errorf("响应内容不完整：%s", mcNetErrText(err))
	}
	latency := time.Since(start)

	br := bytes.NewReader(data)
	if _, err := readMCVarInt(br); err != nil {
		return errors.New("响应缺少包 ID")
	}
	strLen, err := readMCVarInt(br)
	if err != nil || strLen < 0 || strLen > mcMaxPacket {
		return errors.New("响应字符串长度异常")
	}
	body := make([]byte, strLen)
	if _, err := io.ReadFull(br, body); err != nil {
		return errors.New("响应内容被截断")
	}

	var raw struct {
		Version struct {
			Name     string `json:"name"`
			Protocol int    `json:"protocol"`
		} `json:"version"`
		Players struct {
			Max    int `json:"max"`
			Online int `json:"online"`
			Sample []struct {
				Name string `json:"name"`
			} `json:"sample"`
		} `json:"players"`
		Description json.RawMessage `json:"description"`
		Favicon     string          `json:"favicon"`
		SecureChat  bool            `json:"enforcesSecureChat"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return errors.New("服务器返回的 JSON 无法解析（可能不是 Minecraft Java 服务器）")
	}

	info.Version = stripMCCodes(strings.TrimSpace(raw.Version.Name))
	info.Protocol = raw.Version.Protocol
	info.Online = raw.Players.Online
	info.Max = raw.Players.Max
	info.Latency = int(latency.Milliseconds())
	info.Motd = chatToLegacy(raw.Description)
	info.SecureChat = raw.SecureChat
	if strings.HasPrefix(raw.Favicon, "data:image") {
		info.Favicon = raw.Favicon
	}
	for _, p := range raw.Players.Sample {
		if len(info.Players) >= mcMaxSamplePlayers {
			break
		}
		if name := stripMCCodes(strings.TrimSpace(p.Name)); name != "" {
			info.Players = append(info.Players, name)
		}
	}
	if info.Motd == "" {
		info.Motd = ""
	}
	return nil
}

func writeMCVarInt(buf *bytes.Buffer, v int) {
	u := uint32(v)
	for {
		b := byte(u & 0x7f)
		u >>= 7
		if u != 0 {
			buf.WriteByte(b | 0x80)
			continue
		}
		buf.WriteByte(b)
		return
	}
}

func writeMCString(buf *bytes.Buffer, s string) {
	writeMCVarInt(buf, len(s))
	buf.WriteString(s)
}

func readMCVarInt(r io.ByteReader) (int, error) {
	var value int
	var shift uint
	for i := 0; i < 5; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		value |= int(b&0x7f) << shift
		if b&0x80 == 0 {
			return value, nil
		}
		shift += 7
	}
	return 0, errors.New("VarInt 过长")
}

// ---------- 基岩版：RakNet Unconnected Ping ----------

func fillBedrockStatus(info *MCServerInfo) error {
	addr := net.JoinHostPort(info.Host, strconv.Itoa(info.Port))
	raddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return fmt.Errorf("地址无法解析：%s", err)
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return fmt.Errorf("连接 %s 失败：%s", addr, mcNetErrText(err))
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(mcPingTimeout))

	// 0x01 + 时间戳(8) + magic(16) + clientGUID(8)
	req := make([]byte, 33)
	req[0] = 0x01
	binary.BigEndian.PutUint64(req[1:9], uint64(time.Now().UnixMilli()))
	copy(req[9:25], raknetMagic)
	binary.BigEndian.PutUint64(req[25:33], uint64(time.Now().UnixNano()))

	start := time.Now()
	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("发送查询请求失败：%s", mcNetErrText(err))
	}

	buf := make([]byte, 8192)
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("未收到 %s 的响应：%s", addr, mcNetErrText(err))
	}
	latency := time.Since(start)

	// 0x1c + pingTime(8) + serverGUID(8) + magic(16) + 长度(2) + 字符串
	if n < 35 || buf[0] != 0x1c {
		return errors.New("响应不是 RakNet Pong（可能不是基岩服务器）")
	}
	if !bytes.Equal(buf[17:33], raknetMagic) {
		return errors.New("RakNet magic 校验失败")
	}
	strLen := int(binary.BigEndian.Uint16(buf[33:35]))
	if strLen <= 0 || 35+strLen > n {
		return errors.New("响应长度异常")
	}
	fields := strings.Split(string(buf[35:35+strLen]), ";")
	if len(fields) < 6 {
		return errors.New("服务器信息格式不认识")
	}

	if info.Software == "" {
		info.Software = stripMCCodes(strings.TrimSpace(fields[0])) // MCPE
	}
	info.Motd = strings.TrimSpace(fields[1])
	info.Protocol, _ = strconv.Atoi(strings.TrimSpace(fields[2]))
	info.Version = stripMCCodes(strings.TrimSpace(fields[3]))
	info.Online, _ = strconv.Atoi(strings.TrimSpace(fields[4]))
	info.Max, _ = strconv.Atoi(strings.TrimSpace(fields[5]))
	info.Latency = int(latency.Milliseconds())
	// 1.16.220 之后多了「次标题 / 游戏模式」两段；老服务端只有 6 段
	if len(fields) >= 12 {
		if sub := strings.TrimSpace(fields[7]); sub != "" {
			info.Motd = strings.TrimSpace(info.Motd + "\n" + sub)
		}
		info.GameMode = stripMCCodes(strings.TrimSpace(fields[8]))
	}
	return nil
}

// mcNetErrText 把常见网络错误翻成人话，其余原样返回。
func mcNetErrText(err error) string {
	if err == nil {
		return ""
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "连接超时（服务器未响应，可能已关闭或被防火墙拦截）"
	}
	if errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(strings.ToLower(err.Error()), "refused") {
		return "连接被拒绝（端口未开放或服务器未启动）"
	}
	if errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return "网络不可达（检查本机网络或目标地址）"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "域名解析失败（检查地址拼写）"
	}
	return err.Error()
}

// stripMCCodes 去掉传统 § 格式码，用于版本号、玩家名这类纯文本字段。
// 不少服务器会往这些字段里塞颜色码排版（CubeCraft 的版本名就是
// "§f§f§f§fWe support: 1.20-1.21"，玩家示例被当成公告板用），
// 不处理就会把 §f 原样显示给用户。MOTD 例外：它保留 § 码交给前端上色。
func stripMCCodes(s string) string {
	if !strings.ContainsRune(s, '\u00a7') {
		return s
	}
	r := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(r); i++ {
		if r[i] == '\u00a7' {
			// § 后面固定跟一个格式字符（§x 的十六进制写法也是逐字符，同样被吃掉）
			if i+1 < len(r) {
				i++
			}
			continue
		}
		b.WriteRune(r[i])
	}
	return strings.TrimSpace(b.String())
}

// ---------- MOTD：JSON 聊天组件 → 传统 § 格式 ----------

// mcChatStyle 是聊天组件里可能出现的样式组合。
type mcChatStyle struct {
	color     string
	bold      bool
	italic    bool
	underline bool
	strike    bool
	obf       bool
}

// chatToLegacy 把 description（字符串 / 对象 / 数组）压平成带 § 码的文本，
// 前端统一按 § 码渲染，Java 和基岩就能共用一套显示逻辑。
func chatToLegacy(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	var w legacyBuilder
	w.walk(v, mcChatStyle{})
	return strings.TrimRight(w.b.String(), " ")
}

type legacyBuilder struct {
	b   strings.Builder
	cur mcChatStyle
	set bool
}

func (w *legacyBuilder) walk(v any, st mcChatStyle) {
	switch t := v.(type) {
	case string:
		w.emit(st, t)
	case []any:
		for _, item := range t {
			w.walk(item, st)
		}
	case map[string]any:
		next := st
		if c, ok := t["color"].(string); ok {
			next.color = c
		}
		if b, ok := t["bold"].(bool); ok {
			next.bold = b
		}
		if b, ok := t["italic"].(bool); ok {
			next.italic = b
		}
		if b, ok := t["underlined"].(bool); ok {
			next.underline = b
		}
		if b, ok := t["strikethrough"].(bool); ok {
			next.strike = b
		}
		if b, ok := t["obfuscated"].(bool); ok {
			next.obf = b
		}
		if s, ok := t["text"].(string); ok {
			w.emit(next, s)
		}
		// translate 拿不到参数，只能退化成键名（如 "multiplayer.status.online"）
		if s, ok := t["translate"].(string); ok {
			w.emit(next, s)
		}
		if extra, ok := t["extra"].([]any); ok {
			for _, e := range extra {
				w.walk(e, next)
			}
		}
	}
}

// emit 在样式变化时先写 §r 再写新样式，避免上一段的颜色串到下一段。
func (w *legacyBuilder) emit(st mcChatStyle, text string) {
	if text == "" {
		return
	}
	if !w.set || st != w.cur {
		w.b.WriteString("\u00a7r")
		if code := legacyColorCode(st.color); code != "" {
			w.b.WriteString("\u00a7" + code)
		}
		if st.bold {
			w.b.WriteString("\u00a7l")
		}
		if st.italic {
			w.b.WriteString("\u00a7o")
		}
		if st.underline {
			w.b.WriteString("\u00a7n")
		}
		if st.strike {
			w.b.WriteString("\u00a7m")
		}
		if st.obf {
			w.b.WriteString("\u00a7k")
		}
		w.cur = st
		w.set = true
	}
	w.b.WriteString(text)
}

// legacyColorCode 映射 16 个基础色名，自定义 hex 颜色传统格式表示不了，直接丢弃。
func legacyColorCode(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "black":
		return "0"
	case "dark_blue":
		return "1"
	case "dark_green":
		return "2"
	case "dark_aqua":
		return "3"
	case "dark_red":
		return "4"
	case "dark_purple":
		return "5"
	case "gold":
		return "6"
	case "gray":
		return "7"
	case "dark_gray":
		return "8"
	case "blue":
		return "9"
	case "green":
		return "a"
	case "aqua":
		return "b"
	case "red":
		return "c"
	case "light_purple":
		return "d"
	case "yellow":
		return "e"
	case "white":
		return "f"
	}
	return ""
}
