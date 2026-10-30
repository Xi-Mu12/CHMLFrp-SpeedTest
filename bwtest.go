// bwtest.go —— 带宽测速引擎（真实 frpc 隧道 + 本地回环）
//
// 每个选中节点走一遍：
//  1. POST /create_tunnel 建一条临时 tcp 隧道（本地回环数据源为 local_port）
//  2. GET /tunnel 回查拿到 node_ip / server_port / dorp / node_token
//  3. 生成 ini、启动 frpc、等 "start proxy success"
//  4. TCPing node_ip:server_port 取延迟
//  5. 连 节点公网地址:remote_port 双向打流，100ms 出一次速率
//  6. 杀 frpc、删除临时隧道（defer 保证，异常也不残留）
//
// 事件契约（前端 main.js 消费）：
//
//	speed             实时一帧（含 nodeId/nodeName/nodeIndex/nodeCount/phase）
//	speedNode         单节点延迟
//	speedNodeProgress 单节点圆环进度 0~1
//	speedNodeDone     单节点最终结果（成功或失败）
//	speedLog          进度日志 {level: info|ok|warn|err, msg}
//	speedDownload     frpc 下载进度 {progress: 0~1}
//	speedDone         整轮结束汇总
package main

import (
	"fmt"
	"math"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	bwRemotePortMin = 20000
	bwRemotePortMax = 60000
	// bwParallel 单方向并发连接数：单连接在长 RTT 下容易被窗口限制，
	// 4 条并发能更接近真实可用带宽。
	bwParallel     = 4
	bwChunkSize    = 128 * 1024
	bwTickInterval = 100 * time.Millisecond
	bwDialTimeout  = 8 * time.Second
	bwTunnelWait   = 15 * time.Second
	bwFrpcReady    = 25 * time.Second
)

// SpeedNodeDone 单节点测速结果（失败时 Error 非空，其余字段为 0）
type SpeedNodeDone struct {
	ID       int     `json:"id"`
	Name     string  `json:"name"`
	Delay    float64 `json:"delay"`
	DownAvg  float64 `json:"downAvg"`
	UpAvg    float64 `json:"upAvg"`
	DownPeak float64 `json:"downPeak"`
	UpPeak   float64 `json:"upPeak"`
	Seconds  int     `json:"seconds"`
	Error    string  `json:"error"`
}

// SpeedLogEvent 测速过程日志（前端直接写进运行日志面板）
type SpeedLogEvent struct {
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

func (a *App) emitSpeedLog(level, msg string) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "speedLog", SpeedLogEvent{Level: level, Msg: msg})
}

func (a *App) emitNodeProgress(node NodeBasic, p float64) {
	if a.ctx == nil {
		return
	}
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	runtime.EventsEmit(a.ctx, "speedNodeProgress", SpeedNodeProgressEvent{
		ID: node.ID, Name: node.Name, Progress: p,
	})
}

func (a *App) speedCancelled(runID int64) bool {
	return atomic.LoadInt64(&a.speedRun) != runID
}

// StartSpeedTest 开始带宽测速（签名保持不变：addr 仅用于日志展示，真实目标由节点决定）
func (a *App) StartSpeedTest(addr string, seconds int, ids []int) {
	if seconds <= 0 || seconds > 120 {
		seconds = 10
	}
	// 防重入：连点“开始测试”会让两轮同时建隧道、起 frpc，而 bwProc/bwTunnelID
	// 只有单槽，后一轮会覆盖前一轮 → 旧的 frpc 杀不掉、临时隧道残留吃账号配额。
	if !atomic.CompareAndSwapInt32(&a.bwBusy, 0, 1) {
		a.emitSpeedLog("warn", "已有带宽测速任务在运行，请先停止或等它结束后再开始")
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "speedDone", SpeedDone{Addr: addr, Cancelled: true})
		}
		return
	}
	runID := atomic.AddInt64(&a.speedRun, 1)
	atomic.StoreInt32(&a.speedPaused, 0)
	go func() {
		defer atomic.StoreInt32(&a.bwBusy, 0)
		a.runBandwidthTest(runID, addr, seconds, ids)
	}()
}

// runBandwidthTest 整轮流程：登录校验 → 准备 frpc → 逐节点测速 → 汇总
func (a *App) runBandwidthTest(runID int64, addr string, seconds int, ids []int) {
	fail := func(msg string) {
		a.emitSpeedLog("err", msg)
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "speedDone", SpeedDone{Addr: addr, Cancelled: true})
		}
	}

	token := a.currentToken()
	if strings.TrimSpace(token) == "" {
		// 单发一个 dedicated 事件，前端据此弹出登录引导（而不是只显示“测速已停止”）
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "speedNeedLogin")
		}
		fail("未登录：带宽测速需要在账号里创建临时隧道，请先登录 CHMLFrp")
		return
	}
	a.emitSpeedLog("info", "校验账号信息…")
	user, err := FetchUserInfo(token)
	if err != nil {
		if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403") {
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, "speedNeedLogin")
			}
			fail("登录已过期，请重新登录 CHMLFrp")
			return
		}
		fail("获取账号信息失败：" + err.Error())
		return
	}
	if strings.TrimSpace(user.UserToken) == "" {
		fail("账号信息缺少用户令牌（usertoken），无法启动 frpc")
		return
	}
	a.emitSpeedLog("ok", fmt.Sprintf("已登录：%s · 隧道配额 %d/%d", user.Username, user.Tunnel, user.TunnelCount))

	allNodes, err := FetchNodes()
	if err != nil {
		fail("拉取节点列表失败：" + err.Error())
		return
	}
	sel := allNodes
	if len(ids) > 0 {
		want := make(map[int]bool, len(ids))
		for _, v := range ids {
			want[v] = true
		}
		sel = nil
		for _, n := range allNodes {
			if want[n.ID] {
				sel = append(sel, n)
			}
		}
	}
	if len(sel) == 0 {
		fail("没有可测节点")
		return
	}
	a.emitSpeedLog("info", fmt.Sprintf("待测节点 %d 个，单节点 %d 秒（每节点含建隧道/起 frpc 约 5~10 秒）", len(sel), seconds))

	a.emitSpeedLog("info", "检查 frpc 客户端…")
	frpcBin, err := ensureFrpc(func(pct float64, msg string) {
		a.emitSpeedLog("info", msg)
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "speedDownload", map[string]float64{"progress": pct})
		}
	})
	if err != nil {
		fail("准备 frpc 失败：" + err.Error())
		return
	}
	a.emitSpeedLog("ok", "frpc 就绪："+frpcBin)

	srv, err := startBWServer()
	if err != nil {
		fail("本地数据源启动失败：" + err.Error())
		return
	}
	defer srv.Close()

	done := make([]SpeedNodeDone, 0, len(sel))
	cancelled := false
	for i, n := range sel {
		if a.speedCancelled(runID) {
			cancelled = true
			break
		}
		// 多节点 × 数十秒可能跨过 access_token 有效期，每个节点重新取一次（必要时自动续期）
		token = a.currentToken()
		res := a.testNodeBandwidth(runID, token, user.UserToken, frpcBin, srv, n, i, len(sel), seconds)
		if a.speedCancelled(runID) {
			cancelled = true
			break
		}
		done = append(done, res)
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "speedNodeDone", res)
		}
		if res.Error != "" {
			a.emitSpeedLog("err", fmt.Sprintf("#%d %s 失败：%s", res.ID, res.Name, res.Error))
			// 失败节点不会有自然 tick，补一帧把进度条推到节点边界
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, "speed", SpeedTick{
					Progress:  float64(i+1) / float64(len(sel)),
					Elapsed:   float64(i+1) * float64(seconds),
					Total:     float64(len(sel)) * float64(seconds),
					NodeID:    n.ID,
					NodeName:  n.Name,
					NodeIndex: i + 1,
					NodeCount: len(sel),
					Phase:     "prepare",
				})
			}
		} else {
			a.emitSpeedLog("ok", fmt.Sprintf("#%d %s 完成：↓%.1f / ↑%.1f Mbps", res.ID, res.Name, res.DownAvg, res.UpAvg))
		}
	}

	var downSum, upSum, downPeak, upPeak float64
	ok := 0
	for _, r := range done {
		if r.Error != "" {
			continue
		}
		ok++
		downSum += r.DownAvg
		upSum += r.UpAvg
		if r.DownPeak > downPeak {
			downPeak = r.DownPeak
		}
		if r.UpPeak > upPeak {
			upPeak = r.UpPeak
		}
	}
	var downAvg, upAvg float64
	if ok > 0 {
		downAvg = downSum / float64(ok)
		upAvg = upSum / float64(ok)
	}
	if !cancelled {
		if ok == 0 {
			a.emitSpeedLog("err", "本轮没有节点测速成功，请查看上方错误日志")
		} else {
			a.emitSpeedLog("ok", fmt.Sprintf("本轮完成：%d 个节点成功，平均 ↓%.1f / ↑%.1f Mbps", ok, downAvg, upAvg))
		}
	}
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "speedDone", SpeedDone{
			Addr: addr, UpAvg: upAvg, DownAvg: downAvg,
			UpPeak: upPeak, DownPeak: downPeak, Cancelled: cancelled,
		})
	}
}

// bwTunnelName 生成符合 CHMLFrp 规范的隧道名：
// 只允许数字、字母、下划线（之前用连字符会被服务端拒绝：“隧道名不符合规范”）。
// 形如 bw_123_456789012，长度可控（<=20 字符）。
func bwTunnelName(nodeID int) string {
	return fmt.Sprintf("bw_%d_%d", nodeID, rand.Int63n(1000000000))
}

// testNodeBandwidth 单节点：建隧道 → 起 frpc → 测延迟 → 打流 → 清理
func (a *App) testNodeBandwidth(runID int64, token, userToken, frpcBin string, srv *bwServer, node NodeBasic, idx, total, seconds int) SpeedNodeDone {
	res := SpeedNodeDone{ID: node.ID, Name: node.Name, Seconds: seconds}
	proxyName := bwTunnelName(node.ID)

	a.emitSpeedLog("info", fmt.Sprintf("(%d/%d) #%d %s · 创建临时隧道…", idx+1, total, node.ID, node.Name))
	a.emitNodeProgress(node, 0.04)

	var tun *TunnelAPI
	created := false
	tracked := false
	defer func() {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "speedNodeProgress", SpeedNodeProgressEvent{ID: node.ID, Name: node.Name, Progress: 1})
		}
		if tracked {
			a.clearBWResource()
		}
		// 无论成功失败，都不留临时隧道
		if tun != nil {
			a.deleteTunnelWithRetry(token, tun.ID)
		} else if created {
			if t := findTunnelByName(token, proxyName); t != nil {
				a.deleteTunnelWithRetry(token, t.ID)
			}
		}
	}()

	for attempt := 1; attempt <= 4; attempt++ {
		if a.speedCancelled(runID) {
			res.Error = "已取消"
			return res
		}
		remotePort := bwRemotePortMin + rand.Intn(bwRemotePortMax-bwRemotePortMin)
		err := CreateTunnelAPI(token, CreateTunnelParams{
			TunnelName: proxyName,
			Node:       node.Name,
			LocalIP:    "127.0.0.1",
			PortType:   "tcp",
			LocalPort:  srv.Port(),
			RemotePort: remotePort,
		})
		if err == nil {
			created = true
			break
		}
		msg := err.Error()
		if isFatalTunnelErr(msg) {
			res.Error = "创建隧道被拒绝：" + msg
			return res
		}
		a.emitSpeedLog("warn", fmt.Sprintf("#%d %s 建隧道失败（第 %d 次，端口 %d）：%s", node.ID, node.Name, attempt, remotePort, msg))
		if attempt == 4 {
			res.Error = "创建临时隧道失败：" + msg
			return res
		}
		proxyName = bwTunnelName(node.ID)
		time.Sleep(500 * time.Millisecond)
	}

	tun, err := waitTunnelByName(token, proxyName, bwTunnelWait, func() bool { return a.speedCancelled(runID) })
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if tun.Dorp == "" {
		res.Error = "隧道未分配远程端口"
		return res
	}
	if strings.TrimSpace(tun.IP) == "" {
		res.Error = "隧道缺少公网连接地址"
		return res
	}
	remotePort, convErr := strconv.Atoi(strings.TrimSpace(tun.Dorp))
	if convErr != nil || remotePort <= 0 {
		res.Error = "隧道远程端口无效：" + tun.Dorp
		return res
	}
	serverAddr := tun.NodeIP
	if serverAddr == "" {
		serverAddr = tun.IP
	}
	serverPort := tun.ServerPort
	if serverPort <= 0 {
		serverPort = 7000 // 老数据的兜底（frpc 默认 server_port）
	}
	a.emitNodeProgress(node, 0.12)

	// frpc 配置与进程
	iniPath := filepath.Join(os.TempDir(), fmt.Sprintf("chmlfrp-bw-%d.ini", node.ID))
	defer os.Remove(iniPath)
	nodeToken := strings.TrimSpace(tun.NodeToken)
	if nodeToken == "" {
		nodeToken = "ChmlFrpToken"
	}
	if err := writeFrpcIni(iniPath, serverAddr, serverPort, userToken, nodeToken, "127.0.0.1", srv.Port(), remotePort, proxyName); err != nil {
		res.Error = "生成 frpc 配置失败：" + err.Error()
		return res
	}
	a.emitSpeedLog("info", fmt.Sprintf("#%d %s · 启动 frpc → %s:%d", node.ID, node.Name, serverAddr, serverPort))
	proc, err := startFrpcProc(frpcBin, iniPath)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer proc.Stop()
	a.trackBWResource(proc, tun.ID, token)
	tracked = true
	if err := proc.WaitReady(bwFrpcReady); err != nil {
		res.Error = "frpc 未就绪：" + err.Error()
		return res
	}
	a.emitNodeProgress(node, 0.22)

	// 打流目标：节点公网地址 + 远程端口（frps 会把这个端口转发到本机回环）
	target := net.JoinHostPort(tun.IP, tun.Dorp)

	// 延迟：优先测公网隧道地址（已就绪，建连会被节点转发回来，RTT 即节点延迟）；
	// 部分节点 frps 端口（7000）被安全组封掉，所以再做一次 server_port 兜底。
	ping := PingOne(node.Name, target, 3, 2*time.Second)
	if ping.Recv == 0 && serverAddr != "" {
		ping = PingOne(node.Name, net.JoinHostPort(serverAddr, strconv.Itoa(serverPort)), 4, 2*time.Second)
	}
	if ping.Recv > 0 {
		res.Delay = math.Round(float64(ping.AvgDelay.Milliseconds())*10) / 10
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "speedNode", SpeedNodeEvent{ID: node.ID, Name: node.Name, Delay: res.Delay})
		}
	}

	a.emitSpeedLog("info", fmt.Sprintf("#%d %s · 开始打流 %d 秒 → %s", node.ID, node.Name, seconds, target))
	dAvg, uAvg, dPeak, uPeak, cancelled, err := a.runThroughput(runID, node, idx, total, seconds, target)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if cancelled {
		res.Error = "已取消"
		return res
	}
	res.DownAvg = math.Round(dAvg*10) / 10
	res.UpAvg = math.Round(uAvg*10) / 10
	res.DownPeak = math.Round(dPeak*10) / 10
	res.UpPeak = math.Round(uPeak*10) / 10
	return res
}

// runThroughput 双向打流，逐 tick 上报速率。支持暂停（暂停时断开连接、冻结计时）。
func (a *App) runThroughput(runID int64, node NodeBasic, idx, total, seconds int, target string) (downAvg, upAvg, downPeak, upPeak float64, cancelled bool, err error) {
	var downBytes, upBytes int64
	stopCh := make(chan struct{})
	var stopOnce sync.Once
	var mu sync.Mutex
	var conns []net.Conn
	var wg sync.WaitGroup
	workers := false

	closeAll := func() {
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		conns = nil
		mu.Unlock()
		wg.Wait()
	}

	startWorkers := func(remaining time.Duration) {
		for i := 0; i < bwParallel*2; i++ {
			mode := "DN"
			if i >= bwParallel {
				mode = "UP"
			}
			wg.Add(1)
			go func(mode string) {
				defer wg.Done()
				conn, derr := net.DialTimeout("tcp", target, bwDialTimeout)
				if derr != nil {
					return
				}
				if tc, ok := conn.(*net.TCPConn); ok {
					_ = tc.SetNoDelay(true)
				}
				mu.Lock()
				conns = append(conns, conn)
				mu.Unlock()
				defer conn.Close()

				deadline := time.Now().Add(remaining)
				_ = conn.SetDeadline(deadline)
				if _, werr := fmt.Fprintf(conn, "%s %d\n", mode, remaining.Milliseconds()); werr != nil {
					return
				}
				buf := make([]byte, bwChunkSize)
				for time.Now().Before(deadline) {
					select {
					case <-stopCh:
						return
					default:
					}
					if mode == "DN" {
						n, rerr := conn.Read(buf)
						if n > 0 {
							atomic.AddInt64(&downBytes, int64(n))
						}
						if rerr != nil {
							return
						}
					} else {
						n, werr := conn.Write(buf)
						if n > 0 {
							atomic.AddInt64(&upBytes, int64(n))
						}
						if werr != nil {
							return
						}
					}
				}
			}(mode)
		}
	}

	totalDur := time.Duration(seconds) * time.Second
	started := time.Now()
	var pausedFor time.Duration
	var runSeconds float64
	var lastDown, lastUp int64

	for {
		if a.speedCancelled(runID) {
			cancelled = true
			break
		}
		if atomic.LoadInt32(&a.speedPaused) == 1 {
			if workers {
				closeAll()
				workers = false
			}
			pStart := time.Now()
			for atomic.LoadInt32(&a.speedPaused) == 1 && !a.speedCancelled(runID) {
				time.Sleep(bwTickInterval)
			}
			pausedFor += time.Since(pStart)
			continue
		}
		elapsed := time.Since(started) - pausedFor
		if elapsed >= totalDur {
			break
		}
		if !workers {
			startWorkers(totalDur - elapsed)
			workers = true
		}
		time.Sleep(bwTickInterval)

		elapsed = time.Since(started) - pausedFor
		if elapsed > totalDur {
			elapsed = totalDur
		}
		runSeconds = elapsed.Seconds()
		d := atomic.LoadInt64(&downBytes)
		u := atomic.LoadInt64(&upBytes)
		dd := float64(d-lastDown) * 8 / 1e6 / bwTickInterval.Seconds()
		du := float64(u-lastUp) * 8 / 1e6 / bwTickInterval.Seconds()
		lastDown, lastUp = d, u
		if dd < 0 {
			dd = 0
		}
		if du < 0 {
			du = 0
		}
		if dd > downPeak {
			downPeak = dd
		}
		if du > upPeak {
			upPeak = du
		}
		frac := runSeconds / float64(seconds)
		if frac > 1 {
			frac = 1
		}
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "speed", SpeedTick{
				Up: du, Down: dd,
				Progress: (float64(idx) + frac) / float64(total),
				Elapsed:  float64(idx)*float64(seconds) + runSeconds,
				Total:    float64(total) * float64(seconds),
				NodeID:   node.ID, NodeName: node.Name,
				NodeIndex: idx + 1, NodeCount: total, Phase: "test",
			})
		}
		a.emitNodeProgress(node, 0.22+0.78*frac)
	}

	stopOnce.Do(func() { close(stopCh) })
	closeAll()
	if runSeconds <= 0.2 {
		runSeconds = 0.2
	}
	downAvg = float64(atomic.LoadInt64(&downBytes)) * 8 / 1e6 / runSeconds
	upAvg = float64(atomic.LoadInt64(&upBytes)) * 8 / 1e6 / runSeconds
	return
}

// deleteTunnelWithRetry 删除临时隧道，失败重试几次（frpc 刚杀，服务端状态可能滞后）
func (a *App) deleteTunnelWithRetry(token string, id int) {
	for i := 0; i < 3; i++ {
		if err := DeleteTunnelAPI(token, id); err == nil {
			return
		}
		time.Sleep(700 * time.Millisecond)
	}
	a.emitSpeedLog("warn", fmt.Sprintf("临时隧道 #%d 删除失败，请到面板手动删除", id))
}

// ---------- 退出时的兜底清理：避免残留 frpc 进程/临时隧道 ----------

func (a *App) trackBWResource(proc *frpcProc, tunnelID int, token string) {
	a.bwMu.Lock()
	defer a.bwMu.Unlock()
	a.bwProc = proc
	a.bwTunnelID = tunnelID
	a.bwToken = token
}

func (a *App) clearBWResource() {
	a.bwMu.Lock()
	defer a.bwMu.Unlock()
	a.bwProc = nil
	a.bwTunnelID = 0
	a.bwToken = ""
}

// CleanupBandwidth 供退出流程调用：杀 frpc + 删除临时隧道
func (a *App) CleanupBandwidth() {
	a.bwMu.Lock()
	proc := a.bwProc
	tunnelID := a.bwTunnelID
	token := a.bwToken
	a.bwProc = nil
	a.bwTunnelID = 0
	a.bwToken = ""
	a.bwMu.Unlock()

	if proc != nil {
		proc.Stop()
	}
	if tunnelID > 0 && token != "" {
		_ = DeleteTunnelAPI(token, tunnelID)
	}
}
