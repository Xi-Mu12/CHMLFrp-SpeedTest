//go:build windows

package main

import _ "embed"

// Windows 托盘图标：复用打包用的多尺寸 icon.ico。
//
//go:embed build/windows/icon.ico
var trayIconData []byte
