// Package updater 负责 GitHub Releases 版本检查、安装包下载与校验。
//
// 只做"查 + 下 + 验"，真正替换文件由独立进程 cmd/updater 完成：
// 主程序无法覆盖正在运行的自身 EXE，必须借第三方进程在退出后动手。
package updater

import (
	"strings"

	"golang.org/x/mod/semver"
)

// NormalizeVersion 把版本号统一成 semver 的 "v" 前缀形式。
// 无法解析（例如开发版的 "dev"）时返回空串，由调用方决定如何处理。
func NormalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return ""
	}
	return v
}

// IsNewer 判断 candidate 是否严格新于 current。
//
// current 无法解析（开发版 dev、或手工改坏的版本号）时视为"有更新"，
// 这样本地调试也能走完整更新流程；正式版由 CI 注入合法版本号。
func IsNewer(current, candidate string) bool {
	next := NormalizeVersion(candidate)
	if next == "" {
		return false
	}
	cur := NormalizeVersion(current)
	if cur == "" {
		return true
	}
	return semver.Compare(next, cur) > 0
}
