package main

import (
	"os"
	"sync/atomic"

	"github.com/energye/systray"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// 托盘状态：窗口当前是否处于“已隐藏到托盘”的状态。
// 用原子变量记录，避免 systray 回调线程与 Wails 主线程竞争。
var windowHidden int32

// trayIconData 由平台分支文件提供（icon_windows.go / icon_unix.go）：
// Windows 用 icon.ico，macOS / Linux 用 appicon.png。

// startTray 启动系统托盘。必须在 Wails 的 OnStartup 之后调用，
// 因为回调里要用到 a.ctx 来操作窗口。
func (a *App) startTray() {
	go systray.Run(func() {
		// onReady：真正初始化图标与菜单
		onTrayReady(a)
	}, func() {
		// onExit：托盘退出时无需额外处理，程序即将结束
	})
}

// onTrayReady 在 systray 初始化完成后执行：设置图标、标题、菜单、点击行为。
func onTrayReady(a *App) {
	if len(trayIconData) > 0 {
		systray.SetIcon(trayIconData)
	}
	systray.SetTitle("CHMLFrp 测速")
	systray.SetTooltip("CHMLFrp 网络测速工具")

	// 左键单击：显示/隐藏主窗口切换
	systray.SetOnClick(func(menu systray.IMenu) {
		a.toggleWindow()
	})
	// 右键：弹出菜单
	systray.SetOnRClick(func(menu systray.IMenu) {
		_ = menu.ShowMenu()
	})

	// 右键菜单项
	mShow := systray.AddMenuItem("显示主窗口", "恢复并前置主窗口")
	mAutostart := systray.AddMenuItemCheckbox("开机自启", "开机时自动启动", a.autostartEnabled())
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出程序", "完全退出 CHMLFrp 测速")

	mShow.Click(func() {
		a.showWindow()
	})
	mAutostart.Click(func() {
		enable := !mAutostart.Checked()
		if err := a.SetAutostart(enable); err != nil {
			return
		}
		if enable {
			mAutostart.Check()
		} else {
			mAutostart.Uncheck()
		}
	})
	mQuit.Click(func() {
		a.QuitApp()
	})
}

// autostartEnabled 读取注册表里的开机自启状态，读取失败时返回 false。
func (a *App) autostartEnabled() bool {
	on, err := a.GetAutostart()
	if err != nil {
		return false
	}
	return on
}

// toggleWindow 在“显示”与“隐藏”之间切换主窗口。
func (a *App) toggleWindow() {
	if atomic.LoadInt32(&windowHidden) == 1 {
		a.showWindow()
		return
	}
	a.hideWindow()
}

// showWindow 恢复、显示并前置主窗口（若已最小化则先取消最小化）。
func (a *App) showWindow() {
	if a.ctx == nil {
		return
	}
	if runtime.WindowIsMinimised(a.ctx) {
		runtime.WindowUnminimise(a.ctx)
	}
	runtime.WindowShow(a.ctx)
	runtime.WindowSetAlwaysOnTop(a.ctx, true)
	runtime.WindowSetAlwaysOnTop(a.ctx, false)
	atomic.StoreInt32(&windowHidden, 0)
}

// hideWindow 隐藏主窗口到托盘（任务栏不再显示）。
func (a *App) hideWindow() {
	if a.ctx == nil {
		return
	}
	runtime.WindowHide(a.ctx)
	atomic.StoreInt32(&windowHidden, 1)
}

// HideToTray 供前端调用：点击标题栏“×”时隐藏到托盘而不退出。
func (a *App) HideToTray() {
	a.hideWindow()
}

// QuitApp 供前端调用：真正退出程序（会关闭托盘）。
func (a *App) QuitApp() {
	// 让正在跑的测速收尾，并兜底杀掉 frpc / 删除临时隧道，避免残留进程与配额占用
	atomic.AddInt64(&a.speedRun, 1)
	atomic.AddInt64(&a.preferRun, 1)
	a.CleanupBandwidth()
	systray.Quit()
	if a.ctx != nil {
		runtime.Quit(a.ctx)
	} else {
		os.Exit(0)
	}
}
