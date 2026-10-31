// prefer.go —— 延迟测速（TCPing 粗筛阶段）
// 定位：快速 TCPing 粗筛，不做带宽测试。界面里这一页叫「延迟测速」，
// 与「带宽测速」并列——前者秒级出结果，后者靠 frpc 隧道 + 本地回环测真实吞吐。
// 目标是：在几秒内对全部节点完成 TCPing，按延迟/丢包排序，给出快排。
package main

import (
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// 延迟测速参数
const (
	// preferPingTimes: 每节点 TCPing 次数。4 次兼顾统计与速度。
	preferPingTimes = 4
	// preferPingTimeout: 单次正式 TCPing 超时。3 秒覆盖国内/国外的正常建连。
	// 2026-10-31 实测：美国洛杉矶节点 7000 端口 TCP 首连握手 2647ms（跨境丢包触发
	// SYN 重传），2s 超时会把"慢但通"的节点误判为超时，故放宽到 3s。
	preferPingTimeout = 3 * time.Second
	// preferProbeTimeout: 端口探测超时，与正式测速一致。
	// 探测是并行的，所以最坏耗时 = 一个 timeout，不会因候选端口多而线性叠加。
	// 注意不能压太短：美国节点首连实测可达 ~2.6s（TCP SYN 重传），压到 2s 会误杀。
	preferProbeTimeout = 3 * time.Second
	// preferConcurrency: 最大并发。过大易打满本地端口/带宽，20 是经验值。
	preferConcurrency = 20
	// preferMaxLoss: 丢包率阈值。> 50% 直接淘汰。
	preferMaxLoss = 0.5
)

// preferPorts 候选连接端口，并行探测后取最先连通的。
// chmlfrp 各节点开放的端口并不统一（2026-10 实测）：
//   - 7000：frpc 配置里的 server_port，部分节点通（中国香港/深圳多线/南京多线）
//   - 80  ：frps http vhost，仅 web="yes" 的节点开（中国广州/十堰多线/湖北武汉多线）
//   - 81  ：web="no" 节点的 vhost 端口（如 广州多线-2 8.134.162.189，80 根本不监听）
//
// 之前固定测 7000、失败后 fallback 80，导致 web="no" 的节点（80/7000 全关）恒超时，
// 广州多线-2 就是这么漏掉的。三个端口都在同一台机器上，RTT 差异 < 1ms，
// 对"相对快慢排序"这个目的没有影响。
var preferPorts = []int{7000, 80, 81}

// pickPort 并行探测候选端口，返回最先连通的端口号。
// 并行而非串行：某些节点单个端口首连要数秒（TCP 重传），串行会逐个等满超时，
// 既慢又会把"只是慢一点"的端口误判成不通。并行下最坏耗时只等于一个 timeout。
func pickPort(ip string, ports []int, timeout time.Duration) (int, bool) {
	ch := make(chan int, len(ports))
	for _, p := range ports {
		go func(p int) {
			if _, err := tcpPingOnce(net.JoinHostPort(ip, strconv.Itoa(p)), timeout); err == nil {
				ch <- p
			}
		}(p)
	}
	timer := time.NewTimer(timeout + 100*time.Millisecond)
	defer timer.Stop()
	for i := 0; i < len(ports); i++ {
		select {
		case p := <-ch:
			return p, true
		case <-timer.C:
			return 0, false
		}
	}
	return 0, false
}

// PreferEvent 延迟测速进度事件（前端契约，字段保持不变）
type PreferEvent struct {
	Kind   string  `json:"kind"`   // start / testing / node / finish / cancel
	NodeID int     `json:"nodeId"` // testing/node 事件：当前节点
	Name   string  `json:"name"`
	Index  int     `json:"index"` // testing/node 事件：当前序号 1-based
	Total  int     `json:"total"`
	Delay  float64 `json:"delay"` // node 事件：测得延迟 ms，-1 表示全部端口不通
	Port   int     `json:"port"`  // node 事件：实际测通用的端口（0 表示不通）
	Sent   int     `json:"sent"`  // node 事件：TCPing 发送次数（建连尝试）
	Recv   int     `json:"recv"`  // node 事件：TCPing 成功次数（收到 SYN-ACK）
	Loss   float64 `json:"loss"`  // node 事件：丢包率 0~1
	// IcmpDelay 仅在 TCPing 失败时有意义：TCPing 全超时后降级发一次 ICMP，
	// >=0 表示节点网络层在线（端口多半是被防火墙拦截），-1 表示连 ICMP 也不通
	// （节点真离线或 ICMP 也被禁）。ICMP 通不代表能建隧道，不参与排名。
	IcmpDelay float64 `json:"icmpDelay"`
	Result    []struct {
		ID    int     `json:"id"`
		Name  string  `json:"name"`
		Delay float64 `json:"delay"`
	} `json:"result"` // finish 事件：按延迟升序
}

// preferTarget 单个待测节点（合并 FetchNodes 的元信息与 txt 的 IP）
type preferTarget struct {
	ID    int
	Name  string
	China string
	IP    string
}

// StartPrefer 启动延迟测速：TCPing 粗筛，按延迟升序输出结果。
// ids 为前端选中节点 id，空则测 txt 全量。
// 流程：解析 txt 拿 IP → FetchNodes 拿 ID/China → 按名字关联 → 并发 TCPing → 排序 emit。
func (a *App) StartPrefer(ids []int) {
	runID := atomic.AddInt64(&a.preferRun, 1)
	go func(id int64) {
		// 未登录时 /nodeinfo 一个节点 IP 也拿不到，先在入口拦下，
		// 免得白拉一遍节点列表再给用户一句含糊的"测速已取消"。
		token := a.currentToken()
		if strings.TrimSpace(token) == "" {
			if a.ctx != nil && atomic.LoadInt64(&a.preferRun) == id {
				runtime.EventsEmit(a.ctx, "preferEvent", PreferEvent{Kind: "needLogin"})
			}
			return
		}

		// 1. FetchNodes 拿节点列表（含 ID/China/Name，免 Token）
		nodes, err := FetchNodes()
		if err != nil {
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, "preferEvent", PreferEvent{Kind: "cancel"})
			}
			return
		}

		// 3. 按 ids 过滤（前端选中的才测）
		sel := nodes
		if len(ids) > 0 {
			want := make(map[int]bool, len(ids))
			for _, v := range ids {
				want[v] = true
			}
			sel = nil
			for _, n := range nodes {
				if want[n.ID] {
					sel = append(sel, n)
				}
			}
		}

		// 官方启动器使用带 Bearer access_token 的 /nodeinfo 获取真实 IP。
		// 走 currentToken：过期会自动续期，避免拿旧令牌把所有节点都测成“不可达”。
		ipByName := FetchNodeIPs(sel, token)

		// 4. 关联 txt 的 IP，跳过 txt 没有的节点
		var targets []preferTarget
		for _, n := range sel {
			ip, ok := ipByName[n.Name]
			if !ok {
				continue
			}
			// /nodeinfo 返回的可能不是 IP 而是域名（实测 sh-2.frp.one 等）。
			// 预解析成 IP 字面量：后面 pickPort 3 个探测 + PingOne 4 次拨号全部
			// 复用同一个地址，既省 7 次 DNS 往返，也避免多线节点解析结果中途漂移
			// 导致探测和测速打到不同后端。
			if net.ParseIP(ip) == nil {
				if addrs, lerr := net.LookupHost(strings.TrimSpace(ip)); lerr == nil && len(addrs) > 0 {
					pick := ""
					for _, a := range addrs {
						if net.ParseIP(a).To4() != nil {
							pick = a
							break
						}
					}
					if pick == "" {
						pick = addrs[0] // 只有 v6 时退而求其次
					}
					ip = pick
				}
				// 解析失败保留原值，交给 DialTimeout 自行解析兜底
			}
			targets = append(targets, preferTarget{ID: n.ID, Name: n.Name, China: n.China, IP: ip})
		}
		if len(targets) == 0 {
			if a.ctx != nil {
				// 未登录拿不到节点真实 IP：这属于“需要登录”，不是“取消”，
				// 前端要显示登录引导而不是一句莫名其妙的“测速已取消”。
				switch {
				case strings.TrimSpace(token) == "":
					runtime.EventsEmit(a.ctx, "preferEvent", PreferEvent{Kind: "needLogin"})
				default:
					// 已登录却一个 IP 都没拿到：不是取消，把原因说清楚
					runtime.EventsEmit(a.ctx, "preferEvent", PreferEvent{
						Kind: "error",
						Name: "已登录，但没能取到任何节点的真实 IP。可能是登录已过期或节点接口暂时不可用，请重新登录后再试。",
					})
				}
			}
			return
		}
		total := len(targets)
		runtime.EventsEmit(a.ctx, "preferEvent", PreferEvent{Kind: "start", Total: total})

		// 5. 并发 TCPing，每完成一个就 emit node 事件驱动进度条
		type oneResult struct {
			id    int
			name  string
			delay float64
			loss  float64
			port  int
			ok    bool
		}
		results := make([]oneResult, total)
		sem := make(chan struct{}, preferConcurrency)
		var wg sync.WaitGroup
		var doneCount int32
		for i, t := range targets {
			if atomic.LoadInt64(&a.preferRun) != id {
				runtime.EventsEmit(a.ctx, "preferEvent", PreferEvent{Kind: "cancel"})
				return
			}
			wg.Add(1)
			go func(idx int, t preferTarget) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				// 真正能刹住车的是这里：循环本体只做 wg.Add + go，
				// 几十个 goroutine 几毫秒就全创建完了，循环里的取消检查
				// 拦不住已经在排队的任务，所以拿到并发槽位后再确认一次。
				if atomic.LoadInt64(&a.preferRun) != id {
					return
				}
				// testing 事件高亮当前节点（并发下会跳，但给了用户即时反馈）
				runtime.EventsEmit(a.ctx, "preferEvent", PreferEvent{
					Kind: "testing", NodeID: t.ID, Name: t.Name,
					Index: idx + 1, Total: total,
				})
				// 先选端口：各节点开放的端口不统一（7000/80/81），
				// 取第一个能连上的来测。全不通说明该节点当前没有 frps 在服务。
				port, ok := pickPort(t.IP, preferPorts, preferProbeTimeout)
				// 瞬态丢包（跨境链路 SYN 被丢、云安全组限流）很常见，探测失败
				// 不急着判死：歇一下重试一轮，第二次建连往往就通了。
				if !ok && atomic.LoadInt64(&a.preferRun) == id {
					time.Sleep(600 * time.Millisecond)
					if atomic.LoadInt64(&a.preferRun) == id {
						port, ok = pickPort(t.IP, preferPorts, preferProbeTimeout)
					}
				}
				res := oneResult{id: t.ID, name: t.Name, port: port}
				// TCPing 统计。端口全不通时按"发满次数、零响应、100% 丢包"上报，
				// 与 PingOne 全部超时的返回保持一致，前端卡片显示才有意义。
				sent, recv, loss := preferPingTimes, 0, 1.0
				if ok {
					pr := PingOne(t.Name, net.JoinHostPort(t.IP, strconv.Itoa(port)), preferPingTimes, preferPingTimeout)
					// 4 次全超时也可能是瞬态丢包撞上首连，同样给一次重测机会。
					if pr.Recv == 0 && atomic.LoadInt64(&a.preferRun) == id {
						time.Sleep(600 * time.Millisecond)
						if atomic.LoadInt64(&a.preferRun) == id {
							pr = PingOne(t.Name, net.JoinHostPort(t.IP, strconv.Itoa(port)), preferPingTimes, preferPingTimeout)
						}
					}
					sent, recv, loss = pr.Sent, pr.Recv, pr.LossRate
					if pr.Recv > 0 {
						res.ok = true
						res.delay = float64(pr.AvgDelay.Microseconds()) / 1000.0
						res.loss = pr.LossRate
					}
				}
				// TCPing 全失败时降级发一次 ICMP：区分"节点离线"与"端口被防火墙
				// 拦截但节点在线"（实测部分节点 ping 通、7000/80/81 全 DROP）。
				icmpDelay := -1.0
				if !res.ok && atomic.LoadInt64(&a.preferRun) == id {
					if d, err := icmpPing(t.IP, preferProbeTimeout); err == nil {
						icmpDelay = float64(d.Microseconds()) / 1000.0
					}
				}
				// 测量期间可能已被取消或被新一轮取代，此时不能再 emit，
				// 否则旧数据会写进新任务的进度条。
				if atomic.LoadInt64(&a.preferRun) != id {
					return
				}
				results[idx] = res
				done := int(atomic.AddInt32(&doneCount, 1))
				emitDelay := res.delay
				if !res.ok {
					emitDelay = -1 // 失败标记，前端显示"超时"且不纳入 bestDelay
				}
				runtime.EventsEmit(a.ctx, "preferEvent", PreferEvent{
					Kind: "node", NodeID: t.ID, Name: t.Name,
					Index: done, Total: total, Delay: emitDelay, Port: res.port,
					Sent: sent, Recv: recv, Loss: loss, IcmpDelay: icmpDelay,
				})
			}(i, t)
		}
		wg.Wait()

		if atomic.LoadInt64(&a.preferRun) != id {
			runtime.EventsEmit(a.ctx, "preferEvent", PreferEvent{Kind: "cancel"})
			return
		}

		// 6. 过滤不通/高丢包 + 排序（丢包升序 → 延迟升序）→ emit finish
		var ok []oneResult
		for _, r := range results {
			if r.ok && r.loss <= preferMaxLoss {
				ok = append(ok, r)
			}
		}
		sort.SliceStable(ok, func(i, j int) bool {
			if ok[i].loss != ok[j].loss {
				return ok[i].loss < ok[j].loss
			}
			return ok[i].delay < ok[j].delay
		})
		ev := PreferEvent{Kind: "finish", Total: total}
		for _, r := range ok {
			ev.Result = append(ev.Result, struct {
				ID    int     `json:"id"`
				Name  string  `json:"name"`
				Delay float64 `json:"delay"`
			}{ID: r.id, Name: r.name, Delay: r.delay})
		}
		runtime.EventsEmit(a.ctx, "preferEvent", ev)
	}(runID)
}

// CancelPrefer 取消延迟测速
func (a *App) CancelPrefer() {
	atomic.AddInt64(&a.preferRun, 1)
}
