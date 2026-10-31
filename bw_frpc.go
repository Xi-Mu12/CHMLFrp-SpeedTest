// bw_frpc.go —— 带宽测速用的 frpc 获取与进程管理
//
// 对标官方启动器：
//   - frpc 可执行文件不打包进仓库，运行时从官方下载表拉取并缓存
//     （src-tauri/src/commands/download.rs 的 frpc_info.json 逻辑）
//   - 按平台/架构映射下载项，下载后校验 sha256，缓存到用户缓存目录复用
//   - 用生成的 ini 启动 frpc，等待日志里的 "start proxy success" 表示隧道就绪
//   - 失败模式（token 错误 / 隧道数量超限 / 参数不匹配等）沿用官方启动器的
//     process_guard.rs STOP_GUARD_PATTERNS，命中即视为启动失败
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// frpcInfoURL 官方 frpc 下载表（与官方启动器一致）。
// 用 var 而不是 const，是为了让测试能把下载表指向本地 httptest 服务。
var frpcInfoURL = "https://cf-v1.uapis.cn/download/frpc/frpc_info.json"

type frpcAsset struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Platform string `json:"platform"`
	Link     string `json:"link"`
	Hash     string `json:"hash"`
	HashType string `json:"hash_type"`
	Size     int64  `json:"size"`
}

type frpcInfoResp struct {
	Msg   string `json:"msg"`
	State string `json:"state"`
	Code  int    `json:"code"`
	Data  struct {
		Downloads []frpcAsset `json:"downloads"`
	} `json:"data"`
}

func frpcBinaryName() string {
	if runtime.GOOS == "windows" {
		return "frpc.exe"
	}
	return "frpc"
}

// frpcPlatformName 把 GOOS/GOARCH 映射成官方下载表里的 platform 字段。
// 下载表实测（2026-10）：win_amd64.exe / win_386.exe / win_arm64.exe /
// linux_amd64 / linux_arm64 / linux_386 / linux_arm / darwin_amd64 / darwin_arm64 ...
func frpcPlatformName() (string, error) {
	switch runtime.GOOS {
	case "windows":
		switch runtime.GOARCH {
		case "amd64":
			return "win_amd64.exe", nil
		case "386":
			return "win_386.exe", nil
		case "arm64":
			return "win_arm64.exe", nil
		}
	case "linux":
		switch runtime.GOARCH {
		case "amd64":
			return "linux_amd64", nil
		case "386":
			return "linux_386", nil
		case "arm64":
			return "linux_arm64", nil
		case "arm":
			return "linux_arm", nil
		}
	case "darwin":
		switch runtime.GOARCH {
		case "amd64":
			return "darwin_amd64", nil
		case "arm64":
			return "darwin_arm64", nil
		}
	}
	return "", fmt.Errorf("官方下载表暂不支持当前平台 %s/%s", runtime.GOOS, runtime.GOARCH)
}

// frpcCacheDir 缓存目录：用户缓存目录/CHMLFrp-SpeedTest/frpc
func frpcCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "CHMLFrp-SpeedTest", "frpc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// frpcHashSuffix 缓存校验旁文件后缀：内容是下载时官方下载表给出的 sha256。
// 有它在，就能在复用时确认缓存文件没被截断或写坏，而不只是看大小非零。
const frpcHashSuffix = ".sha256"

// fileSHA256 计算文件内容的 sha256（十六进制小写）
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifyCachedFrpc 校验缓存目录里的 frpc 是否可用。
// 有旁文件时严格比对 sha256，不一致就删掉让上层重新下载；
// 没有旁文件（早于该机制下载的旧缓存）退回"大小非零"判断，避免每次启动都白下 14MB。
func verifyCachedFrpc(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() || st.Size() == 0 {
		return false
	}
	raw, err := os.ReadFile(path + frpcHashSuffix)
	if err != nil {
		return true
	}
	want := strings.TrimSpace(string(raw))
	if want == "" {
		return true
	}
	got, err := fileSHA256(path)
	if err != nil || !strings.EqualFold(got, want) {
		_ = os.Remove(path)
		_ = os.Remove(path + frpcHashSuffix)
		return false
	}
	return true
}

// findLocalFrpc 依次检查：程序同目录 → 程序目录/resources → 缓存目录。
// 用户自己放的同目录 frpc 直接信任（不做哈希校验）；缓存目录里的文件必须过校验，
// 否则视为损坏并删除，由 ensureFrpc 重新下载。
func findLocalFrpc() (string, bool) {
	name := frpcBinaryName()
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, name),
			filepath.Join(dir, "resources", name),
		)
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Size() > 0 {
			return p, true
		}
	}
	if dir, err := frpcCacheDir(); err == nil {
		p := filepath.Join(dir, name)
		if verifyCachedFrpc(p) {
			return p, true
		}
	}
	return "", false
}

// ensureFrpc 保证 frpc 可用：有就直接用，没有就按官方下载表下载并校验。
// progress 回调用于把下载进度推给前端（pct 0~1，msg 为展示文案）。
func ensureFrpc(progress func(pct float64, msg string)) (string, error) {
	if path, ok := findLocalFrpc(); ok {
		return path, nil
	}
	dir, err := frpcCacheDir()
	if err != nil {
		return "", fmt.Errorf("创建缓存目录失败: %w", err)
	}
	dest := filepath.Join(dir, frpcBinaryName())
	if err := downloadFrpc(dest, progress); err != nil {
		return "", err
	}
	return dest, nil
}

// downloadFrpc 拉取官方下载表 + 下载对应平台 frpc + sha256 校验 + 原子落盘
func downloadFrpc(dest string, progress func(float64, string)) error {
	report := func(pct float64, msg string) {
		if progress != nil {
			progress(pct, msg)
		}
	}
	platform, err := frpcPlatformName()
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 60 * time.Second}
	report(0.02, "获取 frpc 下载信息…")
	resp, err := client.Get(frpcInfoURL)
	if err != nil {
		return fmt.Errorf("获取 frpc 下载信息失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("获取 frpc 下载信息失败(HTTP %d)", resp.StatusCode)
	}
	var info frpcInfoResp
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return fmt.Errorf("解析 frpc 下载信息失败: %w", err)
	}
	var asset *frpcAsset
	for i := range info.Data.Downloads {
		if info.Data.Downloads[i].Platform == platform {
			asset = &info.Data.Downloads[i]
			break
		}
	}
	if asset == nil {
		return fmt.Errorf("官方下载表未提供 %s 平台的 frpc", platform)
	}

	report(0.05, fmt.Sprintf("下载 frpc（%.1f MB）…", float64(asset.Size)/1024/1024))
	dlClient := &http.Client{Timeout: 10 * time.Minute}
	tmp := dest + ".part"

	// 断点续传：上一轮中断留下的 .part 从断点接着要，避免 14MB 每次从头再来
	var offset int64
	if st, err := os.Stat(tmp); err == nil && !st.IsDir() && st.Size() > 0 {
		offset = st.Size()
	}
	dlReq, err := http.NewRequest(http.MethodGet, asset.Link, nil)
	if err != nil {
		return fmt.Errorf("创建 frpc 下载请求失败: %w", err)
	}
	if offset > 0 {
		dlReq.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	dlResp, err := dlClient.Do(dlReq)
	if err != nil {
		return fmt.Errorf("下载 frpc 失败: %w", err)
	}
	defer dlResp.Body.Close()
	switch dlResp.StatusCode {
	case http.StatusPartialContent:
		// 服务器接受续传，offset 保持不变
	case http.StatusRequestedRangeNotSatisfiable:
		// .part 已不小于完整文件，说明断点作废，丢掉重来
		_ = os.Remove(tmp)
		return fmt.Errorf("frpc 断点已失效，已重置，请重试")
	case http.StatusOK:
		offset = 0 // 服务器不支持 Range，退回整文件下载
	default:
		if dlResp.StatusCode/100 != 2 {
			return fmt.Errorf("下载 frpc 失败(HTTP %d)", dlResp.StatusCode)
		}
	}

	var f *os.File
	if offset > 0 {
		f, err = os.OpenFile(tmp, os.O_WRONLY|os.O_APPEND, 0o644)
	} else {
		f, err = os.Create(tmp)
	}
	if err != nil {
		return fmt.Errorf("创建 frpc 临时文件失败: %w", err)
	}

	hasher := sha256.New()
	if offset > 0 {
		// 已落盘的部分也要计入校验，否则 sha256 只覆盖后半段
		pf, perr := os.Open(tmp)
		if perr != nil {
			f.Close()
			return fmt.Errorf("读取 frpc 断点文件失败: %w", perr)
		}
		_, cerr := io.Copy(hasher, pf)
		pf.Close()
		if cerr != nil {
			f.Close()
			return fmt.Errorf("读取 frpc 断点文件失败: %w", cerr)
		}
	}

	total := asset.Size
	if total <= 0 && dlResp.ContentLength > 0 {
		total = dlResp.ContentLength + offset
	}
	written := offset
	if offset > 0 {
		report(0.06, fmt.Sprintf("续传 frpc（已有 %.1f MB）…", float64(offset)/1024/1024))
	}
	lastPct := -1
	buf := make([]byte, 64*1024)
	for {
		n, rerr := dlResp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return fmt.Errorf("写入 frpc 失败: %w", werr)
			}
			hasher.Write(buf[:n])
			written += int64(n)
			if total > 0 {
				pct := 0.05 + 0.9*float64(written)/float64(total)
				if int(pct*100) != lastPct {
					lastPct = int(pct * 100)
					report(pct, fmt.Sprintf("下载 frpc… %d%%", lastPct))
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			// 保留 .part 供下次续传，不要在中断时删掉已下载的部分
			f.Close()
			return fmt.Errorf("下载 frpc 中断: %w（已保留断点，重试会续传）", rerr)
		}
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("写入 frpc 失败: %w", err)
	}

	sum := hex.EncodeToString(hasher.Sum(nil))
	hasHash := asset.Hash != "" && (asset.HashType == "" || strings.EqualFold(asset.HashType, "sha256"))
	if hasHash && !strings.EqualFold(sum, asset.Hash) {
		// 校验失败说明断点拼出来的内容不对，整份丢掉，不要在坏文件上继续续传
		_ = os.Remove(tmp)
		return fmt.Errorf("frpc 校验失败（sha256 不匹配），已删除损坏文件")
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("保存 frpc 失败: %w", err)
	}
	// 写旁文件供下次复用时校验；没有官方哈希就把残留的旧旁文件清掉
	if hasHash {
		if err := os.WriteFile(dest+frpcHashSuffix, []byte(strings.ToLower(strings.TrimSpace(asset.Hash))), 0o644); err != nil {
			_ = os.Remove(dest + frpcHashSuffix)
		}
	} else {
		_ = os.Remove(dest + frpcHashSuffix)
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(dest, 0o755)
	}
	report(0.96, "frpc 下载完成")
	return nil
}

// writeFrpcIni 生成测速专用配置。
// 键名与官方客户端 frpc.ini 保持一致：user 是账号的 usertoken（不是 OAuth access_token），
// token 是节点 token；隧道段只开一条 tcp 映射到本地回环数据源。
func writeFrpcIni(path, serverAddr string, serverPort int, userToken, nodeToken, localIP string, localPort, remotePort int, proxyName string) error {
	if serverPort <= 0 {
		return fmt.Errorf("节点 server_port 无效")
	}
	if remotePort <= 0 {
		return fmt.Errorf("隧道远程端口无效")
	}
	var b strings.Builder
	b.WriteString("[common]\n")
	fmt.Fprintf(&b, "server_addr = %s\n", serverAddr)
	fmt.Fprintf(&b, "server_port = %d\n", serverPort)
	b.WriteString("tcp_mux = true\n")
	b.WriteString("protocol = tcp\n")
	fmt.Fprintf(&b, "user = %s\n", userToken)
	fmt.Fprintf(&b, "token = %s\n", nodeToken)
	b.WriteString("dns_server = 223.6.6.6\n")
	b.WriteString("tls_enable = false\n")
	b.WriteString("login_fail_exit = true\n")
	b.WriteString("log_level = info\n")
	b.WriteString("log_max_days = 1\n")
	b.WriteString("\n")
	fmt.Fprintf(&b, "[%s]\n", proxyName)
	b.WriteString("type = tcp\n")
	fmt.Fprintf(&b, "local_ip = %s\n", localIP)
	fmt.Fprintf(&b, "local_port = %d\n", localPort)
	fmt.Fprintf(&b, "remote_port = %d\n", remotePort)
	b.WriteString("use_encryption = false\n")
	b.WriteString("use_compression = false\n")
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// frpcFailPatterns 认定“启动注定失败”的日志片段。
// 前一批来自官方启动器 process_guard.rs 的 STOP_GUARD_PATTERNS（服务端返回的业务错误），
// 后一批来自 chmlfrp 定制版 frpc 自身的中文日志（实测 frpc.exe 字符串）。
var frpcFailPatterns = []string{
	"token in login doesn't match token from configuration",
	"authorization failed",
	"invalid token",
	"错误的用户token，此用户不存在",
	"允许的隧道数量超出上限",
	"不属于你",
	"缺少用户token或隧道id参数",
	"您目前为免费会员",
	"客户端代理参数错误",
	"ChmlFrp API Error",
	// frpc 自身日志
	"启动失败",
	"无法连接至服务器",
	"重连已失败",
	"启动客户端失败",
	"start error",
	"login to server failed",
}

// frpcReadyMarkers 隧道就绪日志。标准 frpc 是英文，chmlfrp 定制版是中文
// （实测：`[bw1] 映射启动成功, 感谢您使用ChmlFrp!`），两种都认。
var frpcReadyMarkers = []string{
	"start proxy success",
	"映射启动成功",
}

func matchFrpcFail(line string) string {
	low := strings.ToLower(line)
	for _, p := range frpcFailPatterns {
		if strings.Contains(low, strings.ToLower(p)) {
			return p
		}
	}
	return ""
}

// frpcEnv 去掉代理相关的环境变量，保证 frpc 直连节点
func frpcEnv() []string {
	drop := map[string]bool{
		"http_proxy":  true,
		"https_proxy": true,
		"all_proxy":   true,
		"no_proxy":    true,
	}
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		key := kv
		if i := strings.Index(kv, "="); i > 0 {
			key = kv[:i]
		}
		if drop[strings.ToLower(key)] {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// frpcProc 一个运行中的 frpc 进程及其日志流
type frpcProc struct {
	cmd   *exec.Cmd
	lines chan string

	stopOnce sync.Once
}

func startFrpcProc(bin, iniPath string) (*frpcProc, error) {
	cmd := exec.Command(bin, "-c", iniPath)
	cmd.SysProcAttr = hiddenProcAttr()
	// 测速要的是节点直连的真实吞吐：清掉系统代理变量，
	// 否则 frpc 会走 http_proxy（日志里的 DialTcpByHttpProxy），测出来的就不是链路本身。
	cmd.Env = frpcEnv()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 frpc 失败: %w", err)
	}
	p := &frpcProc{cmd: cmd, lines: make(chan string, 256)}
	var wg sync.WaitGroup
	scan := func(r io.Reader) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 64*1024)
		for sc.Scan() {
			select {
			case p.lines <- sc.Text():
			default: // 没人消费时丢弃，避免阻塞日志读取
			}
		}
	}
	wg.Add(2)
	go scan(stdout)
	go scan(stderr)
	go func() {
		wg.Wait()
		close(p.lines)
	}()
	return p, nil
}

// WaitReady 等日志里出现 start proxy success。
// 命中失败模式立即返回错误，超时或进程提前退出也返回错误。
func (p *frpcProc) WaitReady(timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var tail string // 记录最后几条日志，方便报错
	for {
		select {
		case line, ok := <-p.lines:
			if !ok {
				if tail == "" {
					tail = "（无日志输出）"
				}
				return fmt.Errorf("frpc 已退出: %s", tail)
			}
			low := strings.ToLower(line)
			ready := false
			for _, marker := range frpcReadyMarkers {
				if strings.Contains(low, strings.ToLower(marker)) {
					ready = true
					break
				}
			}
			if ready {
				return nil
			}
			if pat := matchFrpcFail(line); pat != "" {
				return fmt.Errorf("%s", strings.TrimSpace(line))
			}
			if strings.Contains(low, "login to server success") || strings.Contains(line, "成功登录至服务器") {
				tail = "登录成功，等待隧道就绪"
			} else if t := strings.TrimSpace(line); t != "" {
				tail = t
			}
		case <-timer.C:
			if tail == "" {
				tail = "无有效日志"
			}
			return fmt.Errorf("等待 frpc 隧道就绪超时（%s）", tail)
		}
	}
}

// Stop 结束 frpc 进程（幂等）
func (p *frpcProc) Stop() {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	p.stopOnce.Do(func() {
		_ = p.cmd.Process.Kill()
		_, _ = p.cmd.Process.Wait()
	})
}
