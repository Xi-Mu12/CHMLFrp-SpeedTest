//go:build !windows

package main

import _ "embed"

// 非 Windows（macOS / Linux 等）托盘图标：energye/systray 在这些平台上
// 期望 PNG 字节，直接复用 build/appicon.png 作为统一图标源。
//
//go:embed build/appicon.png
var trayIconData []byte
