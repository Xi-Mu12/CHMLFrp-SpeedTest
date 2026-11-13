package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"gui/internal/updater"
)

// 更新状态机：
//
//	idle → checking → available → downloading → ready
//	                     ↘ uptodate        ↘ error
//
// 只有 ready 允许执行安装；下载中的取消会退回 available。
type UpdateInfo struct {
	Version        string `json:"version"`
	Name           string `json:"name"`
	Notes          string `json:"notes"`
	PublishedAt    string `json:"publishedAt"`
	PageURL        string `json:"pageUrl"`
	Size           int64  `json:"size"`
	CanAutoInstall bool   `json:"canAutoInstall"`
	// InstallMode 决定这次更新怎么落地：setup=运行安装包静默安装，zip=解压覆盖。
	// 前端据此显示不同文案。
	InstallMode string `json:"installMode,omitempty"`

	// 以下字段只供后端使用，不参与前端序列化。
	assetURL  string
	assetName string
	digest    string
}

// updateHelperName 是随包分发的更新辅助程序；安装版会把它一起装进安装目录，
// 由它在主程序退出后运行新版本的安装包。
const updateHelperName = "updater.exe"

// updateTargetDir 返回主程序所在目录，也就是安装目录（或绿色版解压目录）。
func updateTargetDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

// installedBySetup 判断当前跑的是安装版还是绿色版。
//
// 判据是安装目录里有没有 NSIS 留下的 uninstall.exe；绿色版解压出来没有这个文件。
// 同时要求 updater.exe 在场，否则静默升级没法执行，直接退回 zip 覆盖流程。
func installedBySetup(target string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	if _, err := os.Stat(filepath.Join(target, "uninstall.exe")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(target, updateHelperName)); err != nil {
		return false
	}
	return true
}

// chooseUpdatePayload 决定这次用哪个资产、走哪条落地路径。
//
// 安装版优先安装包：只有安装器才能一并更新卸载项、快捷方式和 WebView2 运行时；
// 绿色版必须用 zip，否则会被搬到 %LOCALAPPDATA% 留下一个孤儿目录。
// 老版本 Release 没有安装包时，安装版也会退化成 zip 覆盖（目录本来就是同一个）。
func chooseUpdatePayload(rel *updater.Release, installed bool) (string, updater.Asset) {
	if installed && rel.Installer.URL != "" {
		return "setup", rel.Installer
	}
	return "zip", rel.Asset
}

// UpdateState 是推给前端的完整快照，前端不需要自己拼状态。
type UpdateState struct {
	Status         string      `json:"status"`
	Info           *UpdateInfo `json:"info,omitempty"`
	Downloaded     int64       `json:"downloaded"`
	Total          int64       `json:"total"`
	Error          string      `json:"error,omitempty"`
	CurrentVersion string      `json:"currentVersion"`
}

// UpdatePrefs 是设置页需要的更新偏好。
type UpdatePrefs struct {
	CurrentVersion    string `json:"currentVersion"`
	DisableAutoUpdate bool   `json:"disableAutoUpdate"`
	SkippedVersion    string `json:"skippedVersion"`
	LastUpdateCheck   int64  `json:"lastUpdateCheck"`
}

// GetUpdateState 返回当前更新状态，供前端首次渲染。
func (a *App) GetUpdateState() UpdateState {
	a.updateMu.Lock()
	st := a.updateState
	a.updateMu.Unlock()
	st.CurrentVersion = version
	return st
}

func (a *App) emitUpdate() {
	a.updateMu.Lock()
	st := a.updateState
	a.updateMu.Unlock()
	st.CurrentVersion = version
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "updateState", st)
	}
}

// mutateUpdate 在锁内修改状态并广播，返回修改后的快照。
func (a *App) mutateUpdate(fn func(*UpdateState)) UpdateState {
	a.updateMu.Lock()
	fn(&a.updateState)
	st := a.updateState
	a.updateMu.Unlock()
	st.CurrentVersion = version
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "updateState", st)
	}
	return st
}

// CheckForUpdate 查询 GitHub 最新正式版。
//
// manual=false 供启动时静默检查：失败不弹错、被跳过的版本不再提示。
func (a *App) CheckForUpdate(manual bool) (UpdateState, error) {
	if !a.updateBusy.CompareAndSwap(false, true) {
		return a.GetUpdateState(), fmt.Errorf("正在检查或下载更新，请稍候")
	}
	defer a.updateBusy.Store(false)

	a.mutateUpdate(func(s *UpdateState) { s.Status = "checking"; s.Error = "" })

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	rel, err := updater.LatestRelease(ctx)
	if err != nil {
		if manual {
			a.mutateUpdate(func(s *UpdateState) { s.Status = "error"; s.Error = "检查更新失败：" + err.Error() })
			return a.GetUpdateState(), err
		}
		// 自动检查失败保持安静，不影响正常使用。
		a.mutateUpdate(func(s *UpdateState) { s.Status = "idle"; s.Error = "" })
		return a.GetUpdateState(), err
	}

	a.touchUpdateCheck()

	if !updater.IsNewer(version, rel.Tag) {
		a.mutateUpdate(func(s *UpdateState) { s.Status = "uptodate"; s.Info = nil; s.Error = "" })
		return a.GetUpdateState(), nil
	}
	if !manual {
		if s := a.GetSettings(); strings.EqualFold(strings.TrimSpace(s.SkippedVersion), strings.TrimSpace(rel.Tag)) {
			a.mutateUpdate(func(s *UpdateState) { s.Status = "idle"; s.Info = nil; s.Error = "" })
			return a.GetUpdateState(), nil
		}
	}

	// 按「当前是安装版还是绿色版」挑资产：安装版有安装包就用安装包。
	target, _ := updateTargetDir()
	mode, asset := chooseUpdatePayload(rel, installedBySetup(target))

	info := &UpdateInfo{
		Version:        rel.Tag,
		Name:           rel.Name,
		Notes:          rel.Notes,
		PublishedAt:    rel.PublishedAt.Format("2006-01-02"),
		PageURL:        rel.PageURL,
		Size:           asset.Size,
		CanAutoInstall: runtime.GOOS == "windows" && asset.URL != "",
		InstallMode:    mode,
		assetURL:       asset.URL,
		assetName:      asset.Name,
		digest:         asset.Digest,
	}
	st := a.mutateUpdate(func(s *UpdateState) {
		s.Status = "available"
		s.Info = info
		s.Downloaded = 0
		s.Total = asset.Size
		s.Error = ""
	})
	return st, nil
}

// StartUpdateDownload 启动后台下载，立即返回；进度通过 updateProgress 事件推送。
func (a *App) StartUpdateDownload() error {
	a.updateMu.Lock()
	st := a.updateState
	if st.Status != "available" || st.Info == nil || st.Info.assetURL == "" {
		a.updateMu.Unlock()
		return fmt.Errorf("当前没有可下载的更新")
	}
	if !a.updateBusy.CompareAndSwap(false, true) {
		a.updateMu.Unlock()
		return fmt.Errorf("已有更新任务在进行")
	}
	info := *st.Info
	ctx, cancel := context.WithCancel(context.Background())
	a.updateCancel = cancel
	a.updateState.Status = "downloading"
	a.updateState.Downloaded = 0
	a.updateState.Error = ""
	a.updateMu.Unlock()
	a.emitUpdate()

	go a.downloadAndStage(ctx, info)
	return nil
}

// CancelUpdateDownload 中止下载，状态退回 available，可重试。
func (a *App) CancelUpdateDownload() {
	a.updateMu.Lock()
	cancel := a.updateCancel
	a.updateCancel = nil
	a.updateMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) downloadAndStage(ctx context.Context, info UpdateInfo) {
	defer a.updateBusy.Store(false)

	dir, err := updateCacheDir()
	if err != nil {
		a.failUpdate("准备下载目录失败：" + err.Error())
		return
	}
	archive := filepath.Join(dir, info.assetName)

	err = updater.Download(ctx, info.assetURL, archive, info.Size, info.digest, func(done, total int64) {
		a.updateMu.Lock()
		a.updateState.Downloaded = done
		if total > 0 {
			a.updateState.Total = total
		}
		a.updateMu.Unlock()
		if a.ctx != nil {
			wailsruntime.EventsEmit(a.ctx, "updateProgress", map[string]any{
				"downloaded": done,
				"total":      total,
			})
		}
	})
	if err != nil {
		if ctx.Err() != nil {
			a.mutateUpdate(func(s *UpdateState) {
				s.Status = "available"
				s.Downloaded = 0
				s.Error = ""
			})
			return
		}
		a.failUpdate("下载更新失败：" + err.Error())
		return
	}

	stage := filepath.Join(dir, "staging-"+sanitizeVersion(info.Version))
	_ = os.RemoveAll(stage)
	if info.InstallMode == "setup" {
		// 安装包本身就是可执行文件，不需要解压：直接把落盘的 exe 当作「暂存产物」。
		// 校验（大小 + SHA256）已在 updater.Download 里完成。
		a.updateMu.Lock()
		a.updateStage = archive
		a.updateState.Status = "ready"
		a.updateState.Downloaded = a.updateState.Total
		a.updateMu.Unlock()
		a.emitUpdate()
		return
	}
	if err := updater.ExtractZip(archive, stage); err != nil {
		a.failUpdate("解压更新包失败：" + err.Error())
		return
	}

	a.updateMu.Lock()
	a.updateStage = stage
	a.updateState.Status = "ready"
	a.updateState.Downloaded = a.updateState.Total
	a.updateMu.Unlock()
	a.emitUpdate()
}

// InstallUpdateAndRestart 启动独立更新进程，然后退出主程序。
//
// 更新进程会等本进程真正退出后才动手（安装版是运行安装包，绿色版是替换文件），
// 所以这里必须先启动它再调用 QuitApp。
func (a *App) InstallUpdateAndRestart() error {
	a.updateMu.Lock()
	st := a.updateState
	stage := a.updateStage
	a.updateMu.Unlock()

	if st.Status != "ready" || stage == "" {
		return fmt.Errorf("更新尚未准备完成")
	}
	if runtime.GOOS != "windows" {
		return fmt.Errorf("当前平台暂不支持自动安装，请到发布页手动下载")
	}

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位主程序失败: %w", err)
	}
	target := filepath.Dir(exePath)

	// 安装版：helper 常驻安装目录（由安装器一起装上），升级动作是把安装包交给它。
	// 绿色版：helper 在新包的解压目录里，升级动作是逐文件替换。
	isSetup := st.Info != nil && st.Info.InstallMode == "setup"
	helperPath := filepath.Join(stage, updateHelperName)
	modeArgs := []string{"--stage", stage}
	if isSetup {
		helperPath = filepath.Join(target, updateHelperName)
		modeArgs = []string{"--setup", stage}
	}
	if _, err := os.Stat(helperPath); err != nil {
		if isSetup {
			return fmt.Errorf("安装目录缺少 %s，无法静默升级；请手动运行安装包：%s", updateHelperName, stage)
		}
		return fmt.Errorf("更新包缺少 %s，请重新下载", updateHelperName)
	}

	args := []string{
		"--pid", strconv.Itoa(os.Getpid()),
		"--target", target,
		"--exe", filepath.Base(exePath),
		"--log", filepath.Join(os.TempDir(), "chmlfrp-updater.log"),
	}
	args = append(args, modeArgs...)

	cmd := exec.Command(helperPath, args...)
	cmd.Dir = target
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动更新程序失败: %w", err)
	}

	// 给更新进程留出启动时间，再真正退出；否则可能出现"父进程先没、子进程还没跑起来"的空窗。
	go func() {
		time.Sleep(500 * time.Millisecond)
		a.QuitApp()
	}()
	return nil
}

// SkipUpdateVersion 记住用户选择跳过的版本，自动检查时不再提示。
func (a *App) SkipUpdateVersion(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if err := a.mutateSettings(func(s *AppSettings) { s.SkippedVersion = v }); err != nil {
		return err
	}
	a.mutateUpdate(func(s *UpdateState) { s.Status = "idle"; s.Info = nil; s.Error = "" })
	return nil
}

// GetUpdatePrefs 返回设置页需要的更新偏好。
func (a *App) GetUpdatePrefs() UpdatePrefs {
	s := a.GetSettings()
	return UpdatePrefs{
		CurrentVersion:    version,
		DisableAutoUpdate: s.DisableAutoUpdate,
		SkippedVersion:    s.SkippedVersion,
		LastUpdateCheck:   s.LastUpdateCheck,
	}
}

// SetAutoUpdateCheck 开关启动时自动检查。
func (a *App) SetAutoUpdateCheck(enabled bool) error {
	return a.mutateSettings(func(s *AppSettings) { s.DisableAutoUpdate = !enabled })
}

func (a *App) touchUpdateCheck() {
	_ = a.mutateSettings(func(s *AppSettings) { s.LastUpdateCheck = time.Now().Unix() })
}

func (a *App) failUpdate(msg string) {
	a.mutateUpdate(func(s *UpdateState) {
		s.Status = "error"
		s.Error = msg
		s.Downloaded = 0
	})
}

func updateCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		return "", fmt.Errorf("无法定位缓存目录")
	}
	dir := filepath.Join(base, "CHMLFrp-SpeedTest", "updates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func sanitizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
