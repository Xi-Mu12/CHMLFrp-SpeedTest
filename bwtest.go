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
	// 单方向并发连接数由设置页控制（1~8，默认 6），见 settings.go。
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
	a.bwMu.Lock()
	if _, ok := a.bwProgress[node.ID]; ok {
		a.bwProgress[node.ID] = math.Max(a.bwProgress[node.ID], p)
	}
	a.emitRunProgressLocked()
	a.bwMu.Unlock()
}

func (a *App) emitRunProgressLocked() {
	if a.ctx == nil || len(a.bwProgress) == 0 {
		return
	}
	var sum float64
	for _, p := range a.bwProgress {
		sum += p
	}
	runtime.EventsEmit(a.ctx, "speedRunProgress", map[string]interface{}{
		"progress": sum / float64(len(a.bwProgress)), "active": a.bwActive,
		"completed": a.bwCompleted, "total": len(a.bwProgress),
	})
}

func (a *App) speedCancelled(runID int64) bool {
	return atomic.LoadInt64(&a.speedRun) != runID
}

// StartSpeedTest 开始带宽测速。
// seconds/parallel 来自设置页，后端再 clamp 一次，避免旧配置或手工调用传入非法值。
func (a *App) StartSpeedTest(seconds, parallel int, ids []int) {
	seconds = normalizeTestSeconds(seconds)
	parallel = normalizeBWParallel(parallel)
	// Keep admission and shutdown coordinated until the run has a completion channel.
	a.bwMu.Lock()
	if a.quitting.Load() {
		a.bwMu.Unlock()
		return
	}
	if !atomic.CompareAndSwapInt32(&a.bwBusy, 0, 1) {
		a.bwMu.Unlock()
		a.emitSpeedLog("warn", "已有带宽测速任务在运行，请先停止或等它结束后再开始")
		return
	}
	runID := atomic.AddInt64(&a.speedRun, 1)
	a.bwFinished = make(chan struct{})
	finished := a.bwFinished
	a.bwMu.Unlock()
	atomic.StoreInt32(&a.speedPaused, 0)
	go func() {
		defer close(finished)
		defer atomic.StoreInt32(&a.bwBusy, 0)
		a.runBandwidthTest(runID, seconds, parallel, ids)
	}()
}

// runBandwidthTest 整轮流程：登录校验 → 准备 frpc → 逐节点测速 → 汇总
func (a *App) runBandwidthTest(runID int64, seconds, parallel int, ids []int) {
	fail := func(msg string) {
		a.emitSpeedLog("err", msg)
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "speedDone", SpeedDone{Cancelled: true})
		}
	}

	// 带宽测速会真实创建临时隧道、占用配额并消耗流量，不能像延迟测速那样
	// “不选就测全部”。前端已经禁用按钮，这里再做一次后端兜底。
	if len(ids) == 0 {
		fail("带宽测速需要至少选择一个节点，请先在节点卡片上选择目标")
		return
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
	a.emitSpeedLog("ok", fmt.Sprintf("已登录：%s", user.Username))

	allNodes, err := FetchNodes()
	if err != nil {
		fail("拉取节点列表失败：" + err.Error())
		return
	}
	sel := make([]NodeBasic, 0, len(ids))
	want := make(map[int]bool, len(ids))
	for _, v := range ids {
		want[v] = true
	}
	for _, n := range allNodes {
		if want[n.ID] {
			sel = append(sel, n)
		}
	}
	if len(sel) == 0 {
		fail("没有可测节点")
		return
	}
	workers := bandwidthWorkers(a.GetSettings().BWTunnelConcurrency, len(sel))
	a.emitSpeedLog("info", fmt.Sprintf("本轮最多同时测试 %d 条隧道，创建结果以服务端返回为准", workers))
	a.emitSpeedLog("info", fmt.Sprintf("待测节点 %d 个，单节点 %d 秒，单方向 %d 条连接（每节点含建隧道/起 frpc 约 5~10 秒）", len(sel), seconds, parallel))

	a.emitSpeedLog("info", "检查 frpc 客户端…")
	frpcBin, ready := findLocalFrpc()
	if !ready {
		fail("尚未下载 frpc，请确认下载测速组件后再开始测速")
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
	a.bwMu.Lock()
	a.bwProgress = make(map[int]float64, len(sel))
	for _, node := range sel {
		a.bwProgress[node.ID] = 0
	}
	a.bwActive, a.bwCompleted = 0, 0
	a.emitRunProgressLocked()
	a.bwMu.Unlock()
	var resultMu sync.Mutex
	var wg sync.WaitGroup
	jobs := make(chan int)
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				for atomic.LoadInt32(&a.speedPaused) == 1 && !a.speedCancelled(runID) {
					time.Sleep(bwTickInterval)
				}
				if a.speedCancelled(runID) {
					continue
				}
				taskToken := a.currentToken()
				a.bwMu.Lock()
				a.bwActive++
				a.emitRunProgressLocked()
				a.bwMu.Unlock()
				res := a.testNodeBandwidth(runID, taskToken, user.UserToken, frpcBin, srv, sel[i], i, len(sel), seconds, parallel)
				resultMu.Lock()
				done = append(done, res)
				resultMu.Unlock()
				a.bwMu.Lock()
				a.bwActive--
				a.bwCompleted++
				a.bwProgress[sel[i].ID] = 1
				a.emitRunProgressLocked()
				a.bwMu.Unlock()
				if a.ctx != nil {
					runtime.EventsEmit(a.ctx, "speedNodeDone", res)
				}
				if res.Error != "" {
					a.emitSpeedLog("err", fmt.Sprintf("#%d %s 失败：%s", res.ID, res.Name, res.Error))
				}
			}
		}()
	}
	for i := range sel {
		if a.speedCancelled(runID) {
			break
		}
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	cancelled := a.speedCancelled(runID)

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
			UpAvg: upAvg, DownAvg: downAvg,
			UpPeak: upPeak, DownPeak: downPeak, Cancelled: cancelled,
		})
	}
}

func bandwidthWorkers(desired, selected int) int {
	if selected <= 0 {
		return 0
	}
	return min(normalizeTunnelConcurrency(desired), selected)
}

// bwTunnelName 生成符合 CHMLFrp 规范的隧道名：
// 只允许数字、字母、下划线（之前用连字符会被服务端拒绝：“隧道名不符合规范”）。
// 形如 bw_123_456789012，长度可控（<=20 字符）。
func bwTunnelName(nodeID int) string {
	return fmt.Sprintf("bw_%d_%d", nodeID, rand.Int63n(1000000000))
}

// pickRemotePort 在 [minPort, maxPort] 内挑一个建隧道用的远程端口。
// 范围很窄（<=8 个）时按尝试次数轮换，避免 4 次重试都撞同一个被占用的端口。
func pickRemotePort(minPort, maxPort, attempt int) int {
	if maxPort <= minPort {
		return minPort
	}
	span := maxPort - minPort + 1
	if span <= 8 {
		return minPort + (attempt-1)%span
	}
	return minPort + rand.Intn(span)
}

// testNodeBandwidth 单节点：建隧道 → 起 frpc → 测延迟 → 打流 → 清理
func (a *App) testNodeBandwidth(runID int64, token, userToken, frpcBin string, srv *bwServer, node NodeBasic, idx, total, seconds, parallel int) SpeedNodeDone {
	res := SpeedNodeDone{ID: node.ID, Name: node.Name, Seconds: seconds}
	proxyName := bwTunnelName(node.ID)

	// 远程端口必须落在节点允许的范围内，否则服务端直接拒绝：
	// “指定的端口不在允许的范围内”。范围由 /nodeinfo 的 rport 给出
	// （例：#83 广州多线-2 限制 8002-9000），取不到时回退默认区间。
	portMin, portMax := bwRemotePortMin, bwRemotePortMax
	if lo, hi, ok := FetchNodePortRange(node.Name, token); ok {
		portMin, portMax = lo, hi
		if lo != bwRemotePortMin || hi != bwRemotePortMax {
			a.emitSpeedLog("info", fmt.Sprintf("#%d %s 端口限制 %d-%d", node.ID, node.Name, lo, hi))
		}
	}

	a.emitSpeedLog("info", fmt.Sprintf("(%d/%d) #%d %s · 创建临时隧道…", idx+1, total, node.ID, node.Name))
	a.emitNodeProgress(node, 0.04)

	var tun *TunnelAPI
	created := false
	tracked := false
	defer func() {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "speedNodeProgress", SpeedNodeProgressEvent{ID: node.ID, Name: node.Name, Progress: 1})
		}
		// 无论成功失败，都不留临时隧道
		if tun != nil {
			if !a.deleteTunnelWithRetry(token, tun.ID) {
				// A leaked tunnel still occupies quota: stop admitting more work.
				if !a.speedCancelled(runID) {
					atomic.CompareAndSwapInt64(&a.speedRun, runID, runID+1)
				}
			}
		} else if created {
			if t := findTunnelByName(token, proxyName); t != nil {
				if !a.deleteTunnelWithRetry(token, t.ID) {
					atomic.CompareAndSwapInt64(&a.speedRun, runID, runID+1)
				}
			}
		}
		if tracked {
			a.clearBWResource(tun.ID)
		}
	}()

	for attempt := 1; attempt <= 4; attempt++ {
		if a.speedCancelled(runID) {
			res.Error = "已取消"
			return res
		}
		remotePort := pickRemotePort(portMin, portMax, attempt)
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
			atomic.CompareAndSwapInt64(&a.speedRun, runID, runID+1)
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

	var err error
	tun, err = waitTunnelByName(token, proxyName, bwTunnelWait, func() bool { return a.speedCancelled(runID) })
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if tun.Dorp == "" {
		res.Error = "隧道未分配远程端口"
		return res
	}
	a.trackBWResource(nil, tun.ID, token)
	tracked = true
	if a.speedCancelled(runID) {
		res.Error = "已取消"
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
	ini, err := os.CreateTemp("", "chmlfrp-bw-*.ini")
	if err != nil {
		res.Error = "创建 frpc 配置失败：" + err.Error()
		return res
	}
	iniPath := ini.Name()
	ini.Close()
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
	if a.speedCancelled(runID) {
		res.Error = "已取消"
		return res
	}
	proc, err := startFrpcProc(frpcBin, iniPath)
	if err != nil {
		a.reportFrpcFailure(frpcBin, "启动 frpc", err)
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

	a.emitSpeedLog("info", fmt.Sprintf("#%d %s · 开始打流 %d 秒（下行/上行分时各半，前段预热不计入）→ %s", node.ID, node.Name, seconds, target))
	dAvg, uAvg, dPeak, uPeak, cancelled, err := a.runThroughput(runID, node, idx, total, seconds, parallel, target)
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

// 打流方向
const (
	bwModeDown = "DN"
	bwModeUp   = "UP"
)

// phaseResult 单个方向的打流结果
type phaseResult struct {
	avg  float64 // 平均速率 Mbps（已剔除预热段）
	peak float64 // 峰值速率 Mbps（只统计预热之后）
}

// runThroughput 分阶段打流：先下行，再上行。
//
// 为什么不双向同时打：frpc 隧道和节点出口是同一份带宽，4 条下行 + 4 条上行并发时
// 两个方向互相挤占，测出来的单向上限会明显偏低。分时测量才接近真实可用带宽。
//
// 每段前 20%（至少 500ms）作为预热丢弃：TCP 慢启动 + frp 建链在这段时间还没到稳态，
// 计入平均会把结果压得偏低。预热期间仍然上报瞬时速率，曲线不断档。
func (a *App) runThroughput(runID int64, node NodeBasic, idx, total, seconds, parallel int, target string) (downAvg, upAvg, downPeak, upPeak float64, cancelled bool, err error) {
	parallel = normalizeBWParallel(parallel)
	// 总时长对半分给两个方向，保证用户设置的“单节点时长”不变
	perDir := time.Duration(seconds) * time.Second / 2
	if perDir < 2*time.Second {
		perDir = 2 * time.Second
	}
	warmup := perDir / 5
	if warmup < 500*time.Millisecond {
		warmup = 500 * time.Millisecond
	}

	down, cancelled, err := a.runPhase(runID, node, idx, total, bwModeDown, seconds, perDir, warmup, 0, parallel, target)
	if err != nil || cancelled {
		return 0, 0, 0, 0, cancelled, err
	}
	up, cancelled, err := a.runPhase(runID, node, idx, total, bwModeUp, seconds, perDir, warmup, 0.5, parallel, target)
	if err != nil || cancelled {
		return 0, 0, 0, 0, cancelled, err
	}
	return down.avg, up.avg, down.peak, up.peak, false, nil
}

// runPhase 跑单个方向的打流。fracBase 是该阶段在“单节点总时长”里的起点（下行 0、上行 0.5）。
func (a *App) runPhase(runID int64, node NodeBasic, idx, total int, mode string, totalSeconds int, dur, warmup time.Duration, fracBase float64, parallel int, target string) (phaseResult, bool, error) {
	var res phaseResult
	var bytes int64
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

	// 关闭连接 + 停 worker，取消与正常结束共用
	shutdown := func() {
		stopOnce.Do(func() { close(stopCh) })
		closeAll()
	}

	startWorkers := func(remaining time.Duration) {
		for i := 0; i < parallel; i++ {
			wg.Add(1)
			go func() {
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
					if mode == bwModeDown {
						n, rerr := conn.Read(buf)
						if n > 0 {
							atomic.AddInt64(&bytes, int64(n))
						}
						if rerr != nil {
							return
						}
					} else {
						n, werr := conn.Write(buf)
						if n > 0 {
							atomic.AddInt64(&bytes, int64(n))
						}
						if werr != nil {
							return
						}
					}
				}
			}()
		}
	}

	started := time.Now()
	var pausedFor time.Duration
	var lastBytes int64
	// measuredFrom < 0 表示还没跨过预热；跨过时定格当时的字节数作为平均值基线
	var measuredFrom int64 = -1

	for {
		if a.speedCancelled(runID) {
			shutdown()
			return res, true, nil
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
		if elapsed >= dur {
			break
		}
		if !workers {
			startWorkers(dur - elapsed)
			workers = true
		}
		time.Sleep(bwTickInterval)

		elapsed = time.Since(started) - pausedFor
		if elapsed > dur {
			elapsed = dur
		}
		cur := atomic.LoadInt64(&bytes)
		if measuredFrom < 0 && elapsed >= warmup {
			measuredFrom = cur
		}
		instant := float64(cur-lastBytes) * 8 / 1e6 / bwTickInterval.Seconds()
		lastBytes = cur
		if instant < 0 {
			instant = 0
		}
		// 峰值只认预热之后的稳态值
		if measuredFrom >= 0 && instant > res.peak {
			res.peak = instant
		}

		// 只有当前方向有值，另一个方向补 0：曲线上能直接看出“先下行再上行”
		downRate, upRate := 0.0, 0.0
		if mode == bwModeDown {
			downRate = instant
		} else {
			upRate = instant
		}
		nodeFrac := fracBase + elapsed.Seconds()/float64(totalSeconds)
		if nodeFrac > 1 {
			nodeFrac = 1
		}
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "speed", SpeedTick{
				Up: upRate, Down: downRate,
				Progress: nodeFrac,
				Elapsed:  nodeFrac * float64(totalSeconds),
				Total:    float64(totalSeconds),
				NodeID:   node.ID, NodeName: node.Name,
				NodeIndex: idx + 1, NodeCount: total, Phase: mode,
			})
		}
		a.emitNodeProgress(node, 0.22+0.78*nodeFrac)
	}

	shutdown()

	finalElapsed := time.Since(started) - pausedFor
	if finalElapsed > dur {
		finalElapsed = dur
	}
	if measuredFrom < 0 {
		// 阶段短到没跨过预热：退化为全段统计，别把结果算成 0
		measuredFrom = 0
	}
	measuredBytes := atomic.LoadInt64(&bytes) - measuredFrom
	measuredDur := finalElapsed - warmup
	if measuredDur < 200*time.Millisecond {
		measuredDur = 200 * time.Millisecond
	}
	res.avg = float64(measuredBytes) * 8 / 1e6 / measuredDur.Seconds()
	return res, false, nil
}

// deleteTunnelWithRetry 删除临时隧道，失败重试几次（frpc 刚杀，服务端状态可能滞后）
func (a *App) deleteTunnelWithRetry(token string, id int) bool {
	for i := 0; i < 3; i++ {
		if err := DeleteTunnelAPI(token, id); err == nil {
			return true
		}
		time.Sleep(700 * time.Millisecond)
	}
	a.emitSpeedLog("warn", fmt.Sprintf("临时隧道 #%d 删除失败，请到面板手动删除", id))
	return false
}

// ---------- 退出时的兜底清理：避免残留 frpc 进程/临时隧道 ----------

func (a *App) trackBWResource(proc *frpcProc, tunnelID int, token string) {
	a.bwMu.Lock()
	defer a.bwMu.Unlock()
	if a.bwResources == nil {
		a.bwResources = make(map[int]bwResource)
	}
	a.bwResources[tunnelID] = bwResource{proc, tunnelID, token}
}

func (a *App) clearBWResource(id int) {
	a.bwMu.Lock()
	defer a.bwMu.Unlock()
	delete(a.bwResources, id)
}

type bwResource struct {
	proc     *frpcProc
	tunnelID int
	token    string
}

// CleanupBandwidth 供退出流程调用：杀 frpc + 删除临时隧道
func (a *App) CleanupBandwidth() {
	a.bwMu.Lock()
	atomic.AddInt64(&a.speedRun, 1)
	resources := make([]bwResource, 0, len(a.bwResources))
	for _, resource := range a.bwResources {
		resources = append(resources, resource)
	}
	finished := a.bwFinished
	a.bwMu.Unlock()
	for _, resource := range resources {
		if resource.proc != nil {
			resource.proc.Stop()
		}
	}
	if finished != nil {
		<-finished
	}
}
