package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App Wails 后端：节点查询走真实 /node 接口（见 nodes.go），
// 延迟测速走 prefer.go 的真实 TCPing，带宽测速走 bwtest.go 的 frpc 隧道 + 本地回环。
type App struct {
	ctx          context.Context
	speedRun     int64 // 测速轮次 id，+1 即取消上一轮
	speedPaused  int32
	bwBusy       int32 // 带宽测速占用标志：0 空闲 / 1 正在跑，防止重复进入
	preferRun    int64 // 延迟测速轮次 id，+1 即取消
	preferPaused int32

	// 登录态。authMu 保护下面这些字段，refreshMu 只用于串行化令牌续期，
	// 分开是为了避免「续期中又去读 token」时自锁。
	authMu       sync.Mutex
	refreshMu    sync.Mutex
	qzhuaUser    qzhuaUser
	qzhuaToken   string
	refreshToken string
	authExpiry   time.Time
	authRemember bool // 是否把登录态持久化（对应登录框的“保持登录状态”）
	oidc         qzhuaOIDCConfig
	oidcAt       time.Time
	chmlUser     *ChmlUser
	authCancel   context.CancelFunc

	// 带宽测速期间的活跃 frpc 进程/临时隧道，退出时兜底清理
	bwMu       sync.Mutex
	bwProc     *frpcProc
	bwTunnelID int
	bwToken    string
}

// NewApp 创建 App 应用结构体
func NewApp() *App {
	// 默认记住登录：绝大多数用户不希望每次启动都重新授权。
	return &App{authRemember: true}
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
	IP     string `json:"ip"`
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
	// 走 currentToken：令牌快过期时会先静默续期，否则这里拿不到节点 IP 又没有任何提示
	token := a.currentToken()
	ipByName := FetchNodeIPs(filtered, token)
	out := make([]NodeItem, 0, len(filtered))
	for _, n := range filtered {
		out = append(out, NodeItem{
			ID:     n.ID,
			Name:   n.Name,
			Area:   n.Area,
			Group:  n.NodeGroup,
			Web:    n.Web,
			China:  n.China,
			IP:     ipByName[n.Name],
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
	// 多节点串行测速时的上下文，前端用来显示“当前测速节点/第几个”
	NodeID    int    `json:"nodeId"`
	NodeName  string `json:"nodeName"`
	NodeIndex int    `json:"nodeIndex"`
	NodeCount int    `json:"nodeCount"`
	Phase     string `json:"phase"` // prepare / DN（下行）/ UP（上行）
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
