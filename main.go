package main

import (
	"context"
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// 创建 App 实例
	app := NewApp()

	// 按配置启动应用
	err := wails.Run(&options.App{
		Title:             "CHMLFrp 测速",
		Width:             1145,
		Height:            700,
		MinWidth:          800,
		MinHeight:         560,
		Frameless:         true,
		HideWindowOnClose: false,
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "CHMLFrp-SpeedTest",
			// 第二次双击图标时，把已有窗口从托盘/最小化状态叫回来。
			OnSecondInstanceLaunch: func(options.SecondInstanceData) {
				app.showWindow()
			},
		},
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 24, G: 24, B: 24, A: 1},
		OnStartup: func(ctx context.Context) {
			app.startup(ctx)
			app.startTray()
		},
		OnBeforeClose: app.handleBeforeClose,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
