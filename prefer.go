// prefer.go —— 延迟测速（TCPing 粗筛阶段）
// 定位：快速 TCPing 粗筛，不做带宽测试。界面里这一页叫「延迟测速」，
// 与「带宽测速」并列——前者秒级出结果，后者靠 frpc 隧道 + 本地回环测真实吞吐。
// 目标是：在几秒内对全部节点完成 TCPing，按延迟/丢包排序，给出快排。
package main

import (
	"bufio"
	"net"
	"os"
	"regexp"
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
	// preferPingTimeout: 单次正式 TCPing 超时。2 秒覆盖国内/国外的正常建连。
	preferPingTimeout = 2 * time.Second
	// preferProbeTimeout: 端口探测超时，与正式测速一致。
	// 探测是并行的，所以最坏耗时 = 一个 timeout，不会因候选端口多而线性叠加。
	// 注意不能压太短：美国节点首连可能要 1~3s（TCP 重传），压到 800ms 会误杀。
	preferProbeTimeout = 2 * time.Second
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

// preferIPListPath 用户手抄的 chmlfrp 服务器 IP 列表（桌面 txt）。
// 解析规则见 parseChmlIPList。换机器/换路径时改这里即可。
var preferIPListPath = defaultPreferIPList()

func defaultPreferIPList() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return `C:\Users\Administrator\Desktop\chmlfrp服务器ip.txt`
	}
	return home + string(os.PathSeparator) + "Desktop" + string(os.PathSeparator) + "chmlfrp服务器ip.txt"
}

// rawIPEntry 从用户 txt 解析出的一行
type rawIPEntry struct {
	Name   string
	IP     string
	Domain string
}

// ipRegex 提取 IPv4。要求三段点分，避免误匹配节点名里的数字。
var ipRegex = regexp.MustCompile(`\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`)

// parseChmlIPList 解析手抄 IP 列表。
// 每行形如 "节点名 IP 域名"，含"会员"/"免费"分组标记行与"节点名 IP 域名"表头。
// 容错：当 IP 与域名粘连（如 "175.27.138.59n j.2.frp.one" 被空格误拆）时，
// 用正则定位 IP，IP 之前的文本为节点名，IP 之后的所有非空字符拼回域名。
func parseChmlIPList(path string) ([]rawIPEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []rawIPEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// 跳过表头与分组标记行
		low := strings.ToLower(line)
		if strings.HasPrefix(line, "节点名") || low == "会员" || low == "免费" {
			continue
		}
		loc := ipRegex.FindStringIndex(line)
		if loc == nil {
			continue // 没有合法 IP 的行直接跳过
		}
		ip := line[loc[0]:loc[1]]
		name := strings.TrimSpace(line[:loc[0]])
		// IP 之后的字符去掉所有空白拼回域名（修复 175.27.138.59n j.2.frp.one 这种错位）
		domain := strings.Join(strings.Fields(line[loc[1]:]), "")
		out = append(out, rawIPEntry{Name: name, IP: ip, Domain: domain})
	}
	return out, sc.Err()
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
	Result []struct {
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
	atomic.StoreInt32(&a.preferPaused, 0)
	go func(id int64) {
		// 1. 解析 txt 拿到 节点名 -> IP
		raws, err := parseChmlIPList(preferIPListPath)
		if err != nil {
			runtime.EventsEmit(a.ctx, "preferEvent", PreferEvent{Kind: "cancel"})
			return
		}
		ipByName := make(map[string]string, len(raws))
		for _, r := range raws {
			ipByName[r.Name] = r.IP
		}

		// 2. FetchNodes 拿节点列表（含 ID/China/Name，免 Token）
		nodes, err := FetchNodes()
		if err != nil {
			runtime.EventsEmit(a.ctx, "preferEvent", PreferEvent{Kind: "cancel"})
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

		// 4. 关联 txt 的 IP，跳过 txt 没有的节点
		var targets []preferTarget
		for _, n := range sel {
			ip, ok := ipByName[n.Name]
			if !ok {
				continue
			}
			targets = append(targets, preferTarget{ID: n.ID, Name: n.Name, China: n.China, IP: ip})
		}
		if len(targets) == 0 {
			runtime.EventsEmit(a.ctx, "preferEvent", PreferEvent{Kind: "cancel"})
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
			// 暂停则阻塞至恢复或取消
			if !a.waitPrefer(id, 0) {
				runtime.EventsEmit(a.ctx, "preferEvent", PreferEvent{Kind: "cancel"})
				return
			}
			wg.Add(1)
			go func(idx int, t preferTarget) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				// 真正能刹住车的是这里。循环本体只做 wg.Add + go，
				// 几十个 goroutine 几毫秒就全创建完了，循环里那处暂停检查
				// 一个都拦不住——用户点暂停时它们早就在跑了。
				// 所以拿到并发槽位后再确认一次暂停/取消。
				if !a.waitPrefer(id, 0) {
					return
				}
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
			res := oneResult{id: t.ID, name: t.Name, port: port}
			// TCPing 统计。端口全不通时按"发满次数、零响应、100% 丢包"上报，
			// 与 PingOne 全部超时的返回保持一致，前端卡片显示才有意义。
			sent, recv, loss := preferPingTimes, 0, 1.0
			if ok {
				pr := PingOne(t.Name, net.JoinHostPort(t.IP, strconv.Itoa(port)), preferPingTimes, preferPingTimeout)
				sent, recv, loss = pr.Sent, pr.Recv, pr.LossRate
				if pr.Recv > 0 {
					res.ok = true
					res.delay = float64(pr.AvgDelay.Microseconds()) / 1000.0
					res.loss = pr.LossRate
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
				Sent: sent, Recv: recv, Loss: loss,
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
	atomic.StoreInt32(&a.preferPaused, 0)
}

// PausePrefer 暂停延迟测速
func (a *App) PausePrefer() {
	atomic.StoreInt32(&a.preferPaused, 1)
}

// ResumePrefer 恢复延迟测速
func (a *App) ResumePrefer() {
	atomic.StoreInt32(&a.preferPaused, 0)
}

// waitPrefer 阻塞 duration，期间响应暂停/取消。
// duration<=0 时仅做一次暂停/取消检查（不等待）。
// 返回 false 表示已被取消。
func (a *App) waitPrefer(id int64, duration time.Duration) bool {
	if duration <= 0 {
		for atomic.LoadInt32(&a.preferPaused) == 1 {
			if atomic.LoadInt64(&a.preferRun) != id {
				return false
			}
			time.Sleep(50 * time.Millisecond)
		}
		return atomic.LoadInt64(&a.preferRun) == id
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	start := time.Now()
	pausedAt := time.Time{}
	pausedTotal := time.Duration(0)
	wasPaused := false
	for now := range ticker.C {
		if atomic.LoadInt64(&a.preferRun) != id {
			return false
		}
		if atomic.LoadInt32(&a.preferPaused) == 1 {
			if !wasPaused {
				pausedAt = now
				wasPaused = true
			}
			continue
		}
		if wasPaused {
			pausedTotal += now.Sub(pausedAt)
			wasPaused = false
		}
		if now.Sub(start)-pausedTotal >= duration {
			return true
		}
	}
	return false
}