package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"
)

const (
	// RepoOwner / RepoName 指向公开仓库，匿名即可读取 Release，无需也绝不能内置 Token。
	RepoOwner = "Xi-Mu12"
	RepoName  = "CHMLFrp-SpeedTest"

	apiBase   = "https://api.github.com"
	userAgent = "CHMLFrp-SpeedTest-Updater"
)

// apiBaseURL 单独放变量，便于测试替换；生产环境始终是 GitHub 官方地址。
// 客户端不接受外部传入的下载源，避免被指向任意 URL。
var apiBaseURL = apiBase

// Asset 是 Release 中与当前平台匹配的安装包。
type Asset struct {
	Name   string
	URL    string
	Size   int64
	Digest string // 形如 "sha256:<hex>"，缺失时拒绝更新
}

// Release 是向 UI 暴露的版本信息。
type Release struct {
	Tag         string
	Name        string
	Notes       string
	PageURL     string
	PublishedAt time.Time

	// Asset 是绿色包（zip）：解压后逐文件替换，绿色版用户与老版本客户端都靠它。
	Asset Asset
	// Installer 是安装包（setup.exe）：安装版用户优先用它，能一并更新卸载项与快捷方式。
	// 非 Windows 或该版本没有安装包时为空。
	Installer Asset
}

type apiAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"`
}

type apiRelease struct {
	TagName     string     `json:"tag_name"`
	Name        string     `json:"name"`
	Body        string     `json:"body"`
	HTMLURL     string     `json:"html_url"`
	Draft       bool       `json:"draft"`
	Prerelease  bool       `json:"prerelease"`
	PublishedAt time.Time  `json:"published_at"`
	Assets      []apiAsset `json:"assets"`
}

// apiClient 只用于查询元数据；下载走 download.go 里单独的超时策略。
var apiClient = &http.Client{Timeout: 25 * time.Second}

// AssetName 约定安装包命名，必须在 CI 打包时严格一致。
func AssetName(goos, goarch string) string {
	return fmt.Sprintf("CHMLFrp-SpeedTest-%s-%s.zip", goos, goarch)
}

// InstallerName 是 NSIS 安装包的资产名，同样由 CI 重命名成这个格式。
// 非 Windows 没有安装包，返回空串表示「该平台不需要安装器」。
func InstallerName(goos, goarch string) string {
	if goos != "windows" {
		return ""
	}
	return fmt.Sprintf("CHMLFrp-SpeedTest-%s-%s-setup.exe", goos, goarch)
}

// LatestRelease 查询最新正式版（GitHub 的 /releases/latest 已排除 draft 与 prerelease）。
//
// 返回的错误信息面向用户，尽量可直接展示。
func LatestRelease(ctx context.Context) (*Release, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", apiBaseURL, RepoOwner, RepoName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent)

	resp, err := apiClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("连接 GitHub 失败，请检查网络或代理: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("仓库暂无正式 Release")
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("GitHub API 访问受限（可能触发频率限制），请稍后再试")
	case resp.StatusCode != http.StatusOK:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GitHub 返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var raw apiRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("解析 Release 失败: %w", err)
	}
	if raw.Draft || raw.Prerelease {
		return nil, fmt.Errorf("最新版本被标记为草稿或预发布")
	}

	name := AssetName(runtime.GOOS, runtime.GOARCH)
	setupName := InstallerName(runtime.GOOS, runtime.GOARCH)

	find := func(assetName string) *apiAsset {
		for i := range raw.Assets {
			if raw.Assets[i].Name == assetName {
				return &raw.Assets[i]
			}
		}
		return nil
	}
	// toAsset 要求资产必须带摘要：没有摘要就无法保证下载内容与发布一致，宁可拒绝也不盲装。
	toAsset := func(a *apiAsset) (Asset, error) {
		digest := strings.TrimSpace(a.Digest)
		if digest == "" {
			return Asset{}, fmt.Errorf("安装包 %s 缺少 SHA256 摘要，已拒绝更新", a.Name)
		}
		return Asset{Name: a.Name, URL: a.BrowserDownloadURL, Size: a.Size, Digest: digest}, nil
	}

	rel := &Release{
		Tag:         raw.TagName,
		Name:        raw.Name,
		Notes:       raw.Body,
		PageURL:     raw.HTMLURL,
		PublishedAt: raw.PublishedAt,
	}
	var err2 error
	if zip := find(name); zip != nil {
		if rel.Asset, err2 = toAsset(zip); err2 != nil {
			return nil, err2
		}
	}
	// 安装包是可选的：老版本 Release 只有 zip，没有它照样能更新。
	if setupName != "" {
		if setup := find(setupName); setup != nil {
			if rel.Installer, err2 = toAsset(setup); err2 != nil {
				return nil, err2
			}
		}
	}
	// 没有对应平台的包不算错误：仍然把版本信息给 UI，
	// 由前端退化成"前往下载页"，而不是让检查更新直接失败。
	return rel, nil
}
