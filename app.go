package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App Wails 后端：节点查询走真实 /node 接口（见 nodes.go），
// 测速事件流架子阶段为模拟数据，第二步换成 prefer.go 的真实回显测速。
type App struct {
	ctx          context.Context
	speedRun     int64 // 测速轮次 id，+1 即取消上一轮
	speedPaused  int32
	preferRun    int64 // 优选轮次 id，+1 即取消
	preferPaused int32
	authMu       sync.Mutex
	qzhuaUser    qzhuaUser
	authCancel   context.CancelFunc
}

// NewApp 创建 App 应用结构体
func NewApp() *App {
	return &App{}
}

// startup 在应用启动时调用，保存 context 以便调用运行时方法
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// NodeItem 前端用的简化节点
type NodeItem struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Area   string `json:"area"`
	Group  string `json:"group"`
	Web    string `json:"web"`
	China  string `json:"china"`
	Udp    bool   `json:"udp"`
	Fangyu bool   `json:"fangyu"`
}

// GetNodes 拉取节点列表并按权限组过滤（group: all/user/vip）
func (a *App) GetNodes(group string) ([]NodeItem, error) {
	nodes, err := FetchNodes()
	if err != nil {
		return nil, err
	}
	filtered := FilterNodesByGroup(nodes, group)
	out := make([]NodeItem, 0, len(filtered))
	for _, n := range filtered {
		out = append(out, NodeItem{
			ID:     n.ID,
			Name:   n.Name,
			Area:   n.Area,
			Group:  n.NodeGroup,
			Web:    n.Web,
			China:  n.China,
			Udp:    bool(n.UDP),
			Fangyu: bool(n.Fangyu),
		})
	}
	return out, nil
}

// SpeedTick 推送给表盘的一帧实时速度
type SpeedTick struct {
	Up       float64 `json:"up"`
	Down     float64 `json:"down"`
	Progress float64 `json:"progress"`
	Elapsed  float64 `json:"elapsed"`
	Total    float64 `json:"total"`
}

// SpeedDone 单次测速汇总
type SpeedDone struct {
	Addr      string  `json:"addr"`
	UpAvg     float64 `json:"upAvg"`
	DownAvg   float64 `json:"downAvg"`
	UpPeak    float64 `json:"upPeak"`
	DownPeak  float64 `json:"downPeak"`
	Cancelled bool    `json:"cancelled"`
}

// SpeedNodeEvent 测速过程中单个节点的延迟结果
type SpeedNodeEvent struct {
	ID    int     `json:"id"`
	Name  string  `json:"name"`
	Delay float64 `json:"delay"`
}

// SpeedNodeProgressEvent 当前正在测速节点的圆环进度（0~1）
type SpeedNodeProgressEvent struct {
	ID       int     `json:"id"`
	Name     string  `json:"name"`
	Progress float64 `json:"progress"`
}

// StartSpeedTest 开始测速。架子阶段推送模拟事件流；
// TODO(第二步): 换成 RunEchoSpeedTestBlocking 的真实数据，事件名保持不变，前端不用改。
func (a *App) StartSpeedTest(addr string, seconds int, ids []int) {
	if seconds <= 0 || seconds > 60 {
		seconds = 10
	}
	runID := atomic.AddInt64(&a.speedRun, 1)
	atomic.StoreInt32(&a.speedPaused, 0)
	go func(id int64) {
		total := float64(seconds)
		start := time.Now()
		pausedTotal := 0.0
		pauseStarted := time.Time{}
		wasPaused := false
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		// 本次测速覆盖的节点：随进度逐个输出延迟，卡片实时显示
		var targets []NodeBasic
		if fetched, err := FetchNodes(); err == nil {
			if len(ids) > 0 {
				want := make(map[int]bool, len(ids))
				for _, v := range ids {
					want[v] = true
				}
				for _, n := range fetched {
					if want[n.ID] {
						targets = append(targets, n)
					}
				}
			} else {
				targets = fetched
			}
		}
		emittedDelays := 0
		emitNodeDelays := func(upTo int) {
			if upTo > len(targets) {
				upTo = len(targets)
			}
			for emittedDelays < upTo {
				n := targets[emittedDelays]
				var d float64
				if n.China == "yes" {
					d = float64(18 + rand.Intn(78))
				} else {
					d = float64(95 + rand.Intn(286))
				}
				d = math.Round(d*10) / 10
				runtime.EventsEmit(a.ctx, "speedNode", SpeedNodeEvent{ID: n.ID, Name: n.Name, Delay: d})
				emittedDelays++
			}
		}

		// 模拟：爬升->波动->稳定的曲线，峰值随机
		downPeak := 40 + rand.Float64()*80
		upPeak := 15 + rand.Float64()*30
		var upSum, downSum float64
		var n float64
		var upMax, downMax float64

		emit := func(cancelled bool) {
			var upAvg, downAvg float64
			if n > 0 {
				upAvg, downAvg = upSum/n, downSum/n
			}
			runtime.EventsEmit(a.ctx, "speedDone", SpeedDone{
				Addr: addr, UpAvg: upAvg, DownAvg: downAvg,
				UpPeak: upMax, DownPeak: downMax, Cancelled: cancelled,
			})
		}

		for now := range ticker.C {
			if atomic.LoadInt64(&a.speedRun) != id {
				emit(true)
				return
			}
			if atomic.LoadInt32(&a.speedPaused) == 1 {
				if !wasPaused {
					pauseStarted = now
					wasPaused = true
				}
				continue
			}
			if wasPaused {
				pausedTotal += now.Sub(pauseStarted).Seconds()
				wasPaused = false
			}
			elapsed := now.Sub(start).Seconds() - pausedTotal
			if elapsed >= total {
				emitNodeDelays(len(targets))
				emit(false)
				return
			}
			// 爬升曲线 + 正弦波动 + 噪声
			ramp := 1 - math.Exp(-elapsed/1.5)
			wobble := 0.85 + 0.15*math.Sin(elapsed*2.1)
			noise := 0.9 + rand.Float64()*0.2
			down := downPeak * ramp * wobble * noise
			up := upPeak * ramp * (0.9 + 0.1*math.Sin(elapsed*1.7)) * noise
			upSum, downSum = upSum+up, downSum+down
			n++
			if up > upMax {
				upMax = up
			}
			if down > downMax {
				downMax = down
			}
			progress := elapsed / total
			if progress > 1 {
				progress = 1
			}
			runtime.EventsEmit(a.ctx, "speed", SpeedTick{
				Up: up, Down: down, Progress: progress,
				Elapsed: elapsed, Total: total,
			})
			pos := progress * float64(len(targets))
			if idx := int(pos); idx < len(targets) {
				runtime.EventsEmit(a.ctx, "speedNodeProgress", SpeedNodeProgressEvent{
					ID:       targets[idx].ID,
					Name:     targets[idx].Name,
					Progress: math.Min(1, math.Max(0, pos-float64(idx))),
				})
			}
			emitNodeDelays(int(pos))
		}
	}(runID)
}

// CancelSpeedTest 取消当前测速
func (a *App) CancelSpeedTest() {
	atomic.AddInt64(&a.speedRun, 1)
	atomic.StoreInt32(&a.speedPaused, 0)
}

// PauseSpeedTest 暂停当前测速任务（不取消）
func (a *App) PauseSpeedTest() {
	atomic.StoreInt32(&a.speedPaused, 1)
}

// ResumeSpeedTest 恢复已暂停的测速任务
func (a *App) ResumeSpeedTest() {
	atomic.StoreInt32(&a.speedPaused, 0)
}

// ===== 模拟优选流（架子阶段） =====
// 事件驱动：kind=start → 每个节点 start/done → finish。
// 第二步把 sleep+随机延迟换成真实 PingAll/吞吐测速，事件名与字段不变，前端无需改动。

// PreferEvent 优选进度事件
type PreferEvent struct {
	Kind   string  `json:"kind"`   // start / testing / node / finish / cancel
	NodeID int     `json:"nodeId"` // testing/node 事件：当前节点
	Name   string  `json:"name"`
	Index  int     `json:"index"` // testing/node 事件：当前序号 1-based
	Total  int     `json:"total"`
	Delay  float64 `json:"delay"` // node 事件：测得延迟 ms
	Result []struct {
		ID    int     `json:"id"`
		Name  string  `json:"name"`
		Delay float64 `json:"delay"`
	} `json:"result"` // finish 事件：按延迟升序
}

// StartPrefer 启动模拟优选：逐节点显示进度，产出随机但合理的延迟结果。
// ids 为前端选中的节点 id，空表示全部节点。
func (a *App) StartPrefer(ids []int) {
	runID := atomic.AddInt64(&a.preferRun, 1)
	atomic.StoreInt32(&a.preferPaused, 0)
	go func(id int64) {
		nodes, err := FetchNodes()
		if err != nil {
			runtime.EventsEmit(a.ctx, "preferEvent", PreferEvent{Kind: "cancel"})
			return
		}
		if len(ids) > 0 {
			want := make(map[int]bool, len(ids))
			for _, v := range ids {
				want[v] = true
			}
			var sel []NodeBasic
			for _, n := range nodes {
				if want[n.ID] {
					sel = append(sel, n)
				}
			}
			nodes = sel
		}
		if len(nodes) == 0 {
			runtime.EventsEmit(a.ctx, "preferEvent", PreferEvent{Kind: "cancel"})
			return
		}
		total := len(nodes)
		runtime.EventsEmit(a.ctx, "preferEvent", PreferEvent{Kind: "start", Total: total})

		type item struct {
			ID    int
			Name  string
			Delay float64
		}
		var items []item
		for i, n := range nodes {
			if atomic.LoadInt64(&a.preferRun) != id {
				runtime.EventsEmit(a.ctx, "preferEvent", PreferEvent{Kind: "cancel"})
				return
			}
			// 该节点开始测试（前端高亮卡片 + 左下状态显示）
			runtime.EventsEmit(a.ctx, "preferEvent", PreferEvent{
				Kind: "testing", NodeID: n.ID, Name: n.Name, Index: i + 1, Total: total,
			})
			// 模拟耗时：每个节点 350~850ms，暂停期间不消耗时间。
			if !a.waitPrefer(id, time.Duration(350+rand.Intn(500))*time.Millisecond) {
				runtime.EventsEmit(a.ctx, "preferEvent", PreferEvent{Kind: "cancel"})
				return
			}
			// 模拟延迟：国内 18~95ms，国外 95~380ms
			var d float64
			if n.China == "yes" {
				d = float64(18 + rand.Intn(78))
			} else {
				d = float64(95 + rand.Intn(286))
			}
			d = math.Round(d*10) / 10
			items = append(items, item{ID: n.ID, Name: n.Name, Delay: d})
			runtime.EventsEmit(a.ctx, "preferEvent", PreferEvent{
				Kind: "node", NodeID: n.ID, Name: n.Name,
				Index: i + 1, Total: total, Delay: d,
			})
		}
		if atomic.LoadInt64(&a.preferRun) != id {
			runtime.EventsEmit(a.ctx, "preferEvent", PreferEvent{Kind: "cancel"})
			return
		}
		// 按延迟升序产出结果
		sort.Slice(items, func(x, y int) bool { return items[x].Delay < items[y].Delay })
		ev := PreferEvent{Kind: "finish", Total: total}
		for _, it := range items {
			ev.Result = append(ev.Result, struct {
				ID    int     `json:"id"`
				Name  string  `json:"name"`
				Delay float64 `json:"delay"`
			}{ID: it.ID, Name: it.Name, Delay: it.Delay})
		}
		runtime.EventsEmit(a.ctx, "preferEvent", ev)
	}(runID)
}

// CancelPrefer 取消优选
func (a *App) CancelPrefer() {
	atomic.AddInt64(&a.preferRun, 1)
	atomic.StoreInt32(&a.preferPaused, 0)
}

func (a *App) PausePrefer() {
	atomic.StoreInt32(&a.preferPaused, 1)
}

func (a *App) ResumePrefer() {
	atomic.StoreInt32(&a.preferPaused, 0)
}

func (a *App) waitPrefer(id int64, duration time.Duration) bool {
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

// SaveCSV 弹系统保存对话框并写入 CSV（content 前端拼好，含 UTF-8 BOM）
func (a *App) SaveCSV(content string) (string, error) {
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		DefaultFilename: "nodes.csv",
		Filters: []runtime.FileFilter{
			{DisplayName: "CSV 文件 (*.csv)", Pattern: "*.csv"},
		},
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// SaveImage 弹系统保存对话框并写入 PNG（base64Data 为 dataURL 去掉前缀的 base64）
func (a *App) SaveImage(base64Data string) (string, error) {
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		DefaultFilename: "speedtest.png",
		Filters: []runtime.FileFilter{
			{DisplayName: "PNG 图片 (*.png)", Pattern: "*.png"},
		},
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return "", fmt.Errorf("图片数据解码失败: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
