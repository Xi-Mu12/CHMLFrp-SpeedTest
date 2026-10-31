// version.go —— 版本信息的单一来源
//
// 默认从 wails.json 的 info.productVersion 读取，构建时也可以用
// `wails build -ldflags "-X main.version=v1.4.0"` 覆盖（CI 打 tag 时推荐）。
// 标题栏、设置页“关于”和以后的新版本检查都读这里，避免多处硬编码。
package main

import (
	_ "embed"
	"encoding/json"
	"runtime"
)

//go:embed wails.json
var wailsConfig []byte

var version = "dev"

func init() {
	if version != "dev" {
		return
	}
	var cfg struct {
		Info struct {
			ProductVersion string `json:"productVersion"`
		} `json:"info"`
	}
	if err := json.Unmarshal(wailsConfig, &cfg); err == nil && cfg.Info.ProductVersion != "" {
		version = cfg.Info.ProductVersion
	}
}

// AppInfo 给前端的最小运行时信息（不含任何账号/令牌）。
type AppInfo struct {
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

// GetAppInfo 供标题栏/设置页读取版本。
func (a *App) GetAppInfo() AppInfo {
	return AppInfo{Version: version, OS: runtime.GOOS, Arch: runtime.GOARCH}
}
