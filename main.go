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
		Title:             "ChmlfrpSpeedTest",
		Width:             1145,
		Height:            700,
		MinWidth:          900,
		MinHeight:         560,
		Frameless:         true,
		HideWindowOnClose: true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 11, G: 13, B: 16, A: 1},
		OnStartup: func(ctx context.Context) {
			app.startup(ctx)
			app.startTray()
		},
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
