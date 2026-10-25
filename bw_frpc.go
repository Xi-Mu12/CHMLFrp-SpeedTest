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

// frpcInfoURL 官方 frpc 下载表（与官方启动器一致）
const frpcInfoURL = "https://cf-v1.uapis.cn/download/frpc/frpc_info.json"

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

// findLocalFrpc 依次检查：程序同目录 → 程序目录/resources → 缓存目录。
// 用户如果已经自己放了 frpc（或从官方启动器复制过来），直接复用，不再下载。
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
	if dir, err := frpcCacheDir(); err == nil {
		candidates = append(candidates, filepath.Join(dir, name))
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Size() > 0 {
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
	dlResp, err := dlClient.Get(asset.Link)
	if err != nil {
		return fmt.Errorf("下载 frpc 失败: %w", err)
	}
	defer dlResp.Body.Close()
	if dlResp.StatusCode/100 != 2 {
		return fmt.Errorf("下载 frpc 失败(HTTP %d)", dlResp.StatusCode)
	}

	tmp := dest + ".download"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("创建 frpc 临时文件失败: %w", err)
	}
	hasher := sha256.New()
	total := asset.Size
	if total <= 0 && dlResp.ContentLength > 0 {
		total = dlResp.ContentLength
	}
	var written int64
	lastPct := -1
	buf := make([]byte, 64*1024)
	for {
		n, rerr := dlResp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				os.Remove(tmp)
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
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("下载 frpc 中断: %w", rerr)
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("写入 frpc 失败: %w", err)
	}

	if asset.Hash != "" && (asset.HashType == "" || strings.EqualFold(asset.HashType, "sha256")) {
		sum := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(sum, asset.Hash) {
			os.Remove(tmp)
			return fmt.Errorf("frpc 校验失败（sha256 不匹配）")
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("保存 frpc 失败: %w", err)
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
