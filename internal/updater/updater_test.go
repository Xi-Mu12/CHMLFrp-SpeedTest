package updater

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, candidate string
		want               bool
	}{
		{"1.0.0", "v1.0.1", true},
		{"v1.0.0", "v1.0.0", false},
		{"1.10.0", "1.9.0", false},
		{"1.9.0", "1.10.0", true},
		{"v1.0.0", "v1.0.0-beta.1", false}, // 预发布低于正式版
		{"v1.0.0-beta.1", "v1.0.0", true},
		{"dev", "v1.0.0", true}, // 开发版允许走更新流程
		{"1.0.0", "not-a-version", false},
		{"1.0.0", "", false},
		{"v2.0.0", "v2.0.0+build.7", false}, // 构建元数据不参与比较
	}
	for _, tc := range cases {
		if got := IsNewer(tc.current, tc.candidate); got != tc.want {
			t.Fatalf("IsNewer(%q,%q)=%v want %v", tc.current, tc.candidate, got, tc.want)
		}
	}
}

func TestAssetName(t *testing.T) {
	if got := AssetName("windows", "amd64"); got != "CHMLFrp-SpeedTest-windows-amd64.zip" {
		t.Fatalf("unexpected asset name: %s", got)
	}
}

func TestInstallerName(t *testing.T) {
	if got := InstallerName("windows", "amd64"); got != "CHMLFrp-SpeedTest-windows-amd64-setup.exe" {
		t.Fatalf("unexpected installer name: %s", got)
	}
	// 只有 Windows 有安装包，其他平台返回空串表示「不参与匹配」。
	if got := InstallerName("darwin", "arm64"); got != "" {
		t.Fatalf("non-windows should have no installer, got %q", got)
	}
}

func TestLatestReleasePicksInstaller(t *testing.T) {
	zipName := AssetName(runtime.GOOS, runtime.GOARCH)
	setupName := InstallerName(runtime.GOOS, runtime.GOARCH)
	assets := []map[string]any{{
		"name":                 zipName,
		"browser_download_url": "https://example.com/pkg.zip",
		"size":                 1234,
		"digest":               "sha256:aaa",
	}}
	if setupName != "" {
		assets = append(assets, map[string]any{
			"name":                 setupName,
			"browser_download_url": "https://example.com/setup.exe",
			"size":                 2345,
			"digest":               "sha256:bbb",
		})
	}
	body := map[string]any{
		"tag_name": "v1.3.0", "name": "v1.3.0", "body": "",
		"html_url": "https://example.com/release", "draft": false, "prerelease": false,
		"published_at": "2026-11-02T10:00:00Z", "assets": assets,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL)

	rel, err := LatestRelease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Asset.URL != "https://example.com/pkg.zip" {
		t.Fatalf("zip asset not picked: %+v", rel.Asset)
	}
	if setupName == "" {
		return // 非 Windows 到这里就够了
	}
	if rel.Installer.URL != "https://example.com/setup.exe" || rel.Installer.Digest != "sha256:bbb" {
		t.Fatalf("installer asset not picked: %+v", rel.Installer)
	}
}

func TestLatestReleaseRejectsAssetWithoutDigest(t *testing.T) {
	zipName := AssetName(runtime.GOOS, runtime.GOARCH)
	body := map[string]any{
		"tag_name": "v1.3.0", "name": "v1.3.0", "body": "",
		"html_url": "https://example.com/release", "draft": false, "prerelease": false,
		"published_at": "2026-11-02T10:00:00Z",
		"assets": []map[string]any{{
			"name": zipName, "browser_download_url": "https://example.com/pkg.zip", "size": 10,
		}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL)

	if _, err := LatestRelease(context.Background()); err == nil {
		t.Fatal("缺少摘要的资产必须报错，不能盲装")
	}
}

func writeZip(t *testing.T, entries map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pkg.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractZip(t *testing.T) {
	zipPath := writeZip(t, map[string]string{
		"CHMLFrp-SpeedTest.exe": "binary",
		"updater.exe":           "helper",
	})
	dest := filepath.Join(t.TempDir(), "stage")
	if err := ExtractZip(zipPath, dest); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"CHMLFrp-SpeedTest.exe", "updater.exe"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Fatalf("%s 未被解压: %v", name, err)
		}
	}
}

func TestExtractZipRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"../evil.txt", "a/../../evil.txt", "/abs/evil.txt"} {
		zipPath := writeZip(t, map[string]string{
			"ok.txt": "ok",
			bad:      "evil",
		})
		dest := filepath.Join(t.TempDir(), "stage")
		if err := ExtractZip(zipPath, dest); err == nil {
			t.Fatalf("entry %q should be rejected", bad)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "evil.txt")); !os.IsNotExist(err) {
			t.Fatalf("entry %q escaped the target directory", bad)
		}
	}
}

func TestExtractZipRejectsEmpty(t *testing.T) {
	zipPath := writeZip(t, map[string]string{})
	if err := ExtractZip(zipPath, filepath.Join(t.TempDir(), "stage")); err == nil {
		t.Fatal("empty archive should fail")
	}
}

func TestDownloadVerifiesDigest(t *testing.T) {
	payload := []byte("package-payload")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	dest := filepath.Join(t.TempDir(), "pkg.bin")

	var done, total int64
	err := Download(context.Background(), srv.URL, dest, int64(len(payload)), digest, func(d, tt int64) {
		done, total = d, tt
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("downloaded content mismatch: %v", err)
	}
	if done != int64(len(payload)) || total != int64(len(payload)) {
		t.Fatalf("progress mismatch: %d/%d", done, total)
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Fatal("part file should be removed after success")
	}
}

func TestDownloadRejectsBadDigest(t *testing.T) {
	payload := []byte("tampered")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "pkg.bin")
	bad := "sha256:" + hex.EncodeToString(make([]byte, 32))
	if err := Download(context.Background(), srv.URL, dest, int64(len(payload)), bad, nil); err == nil {
		t.Fatal("digest mismatch must fail")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("failed download must not leave the final file")
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Fatal("failed download must clean the part file")
	}
}

func withAPIBase(t *testing.T, base string) {
	t.Helper()
	old := apiBaseURL
	apiBaseURL = base
	t.Cleanup(func() { apiBaseURL = old })
}

// rangeServerMode 控制测试服务对 Range 的处理方式。
type rangeServerMode int

const (
	rangeOK     rangeServerMode = iota // 正常支持 Range
	rangeIgnore                        // 完全忽略 Range，永远返回 200 + 整包
	rangeLie                           // 只在探测请求上返回 206，之后忽略 Range
)

// rangeServer 起一个下载服务，并统计收到的 Range 请求数。
func rangeServer(t *testing.T, payload []byte, mode rangeServerMode) (*httptest.Server, *int32) {
	t.Helper()
	var rangeCount int32
	probeRange := fmt.Sprintf("bytes=0-%d", probeBytes-1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rng := r.Header.Get("Range")
		serveFull := func() {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = w.Write(payload)
		}
		if rng == "" || mode == rangeIgnore {
			serveFull()
			return
		}
		if mode == rangeLie && rng != probeRange {
			serveFull()
			return
		}
		atomic.AddInt32(&rangeCount, 1)
		var begin, end int64
		if _, err := fmt.Sscanf(rng, "bytes=%d-%d", &begin, &end); err != nil {
			http.Error(w, "bad range", http.StatusBadRequest)
			return
		}
		if begin < 0 {
			begin = 0
		}
		if end >= int64(len(payload)) {
			end = int64(len(payload)) - 1
		}
		if begin > end {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", begin, end, len(payload)))
		w.Header().Set("Content-Length", strconv.FormatInt(end-begin+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[begin : end+1])
	}))
	return srv, &rangeCount
}

func payloadBytes(n int) []byte {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = byte(i % 251)
	}
	return buf
}

func TestSourcesForMirrorsOnlyForGitHub(t *testing.T) {
	gh := "https://github.com/o/r/releases/download/v1/x.zip"
	srcs := sourcesFor(gh)
	if len(srcs) != 1+len(mirrorPrefixes) {
		t.Fatalf("GitHub 地址应生成 1 个直连 + %d 个镜像，实际 %d", len(mirrorPrefixes), len(srcs))
	}
	if srcs[0].url != gh || !strings.HasPrefix(srcs[0].label, "GitHub") {
		t.Fatalf("直连应排在第一位: %+v", srcs[0])
	}
	for _, s := range srcs[1:] {
		if !strings.HasSuffix(s.url, gh) {
			t.Fatalf("镜像地址应为「前缀+原始地址」: %s", s.url)
		}
	}

	local := "http://127.0.0.1:9000/pkg.zip"
	plain := sourcesFor(local)
	if len(plain) != 1 || plain[0].url != local {
		t.Fatalf("非 GitHub 地址不应被改写: %+v", plain)
	}
}

func TestDownloadSegmentsLargeFile(t *testing.T) {
	payload := payloadBytes(3 << 20) // 超过 segmentFloor，应触发分段
	srv, rangeCount := rangeServer(t, payload, rangeOK)
	defer srv.Close()

	sum := sha256.Sum256(payload)
	dest := filepath.Join(t.TempDir(), "pkg.zip")
	var last, lastTotal int64
	err := Download(context.Background(), srv.URL, dest, int64(len(payload)),
		"sha256:"+hex.EncodeToString(sum[:]), func(d, total int64) {
			last, lastTotal = d, total
		})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("分段下载拼接后内容与原始数据不一致")
	}
	if n := atomic.LoadInt32(rangeCount); n < 3 {
		t.Fatalf("期望走分段下载，实际只发出 %d 个 Range 请求", n)
	}
	if last != int64(len(payload)) || lastTotal != int64(len(payload)) {
		t.Fatalf("最终进度不正确: %d/%d", last, lastTotal)
	}
}

func TestDownloadFallsBackWhenRangeIgnored(t *testing.T) {
	// 探测阶段返回 206、真正下载时忽略 Range：必须降级为单连接，而不是把整包写到错误偏移。
	payload := payloadBytes(3 << 20)
	srv, _ := rangeServer(t, payload, rangeLie)
	defer srv.Close()

	sum := sha256.Sum256(payload)
	dest := filepath.Join(t.TempDir(), "pkg.zip")
	if err := Download(context.Background(), srv.URL, dest, int64(len(payload)),
		"sha256:"+hex.EncodeToString(sum[:]), nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("Range 被忽略时降级下载的内容不一致")
	}
}

func TestDownloadSmallFileWithoutRange(t *testing.T) {
	payload := payloadBytes(4096)
	srv, rangeCount := rangeServer(t, payload, rangeIgnore)
	defer srv.Close()

	sum := sha256.Sum256(payload)
	dest := filepath.Join(t.TempDir(), "pkg.zip")
	if err := Download(context.Background(), srv.URL, dest, int64(len(payload)),
		"sha256:"+hex.EncodeToString(sum[:]), nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("单连接下载内容不一致")
	}
	if atomic.LoadInt32(rangeCount) != 0 {
		t.Fatal("不支持 Range 的服务不应收到 Range 请求")
	}
}

func releaseServer(t *testing.T, assetName, digest string) *httptest.Server {
	t.Helper()
	body := map[string]any{
		"tag_name":     "v1.2.0",
		"name":         "v1.2.0",
		"body":         "### 新增\n- 测试",
		"html_url":     "https://example.com/release",
		"draft":        false,
		"prerelease":   false,
		"published_at": "2026-11-01T10:00:00Z",
		"assets": []map[string]any{{
			"name":                 assetName,
			"browser_download_url": "https://example.com/pkg.zip",
			"size":                 1234,
			"digest":               digest,
		}},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing user-agent")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
}

func TestLatestReleaseParses(t *testing.T) {
	asset := AssetName(runtime.GOOS, runtime.GOARCH)
	srv := releaseServer(t, asset, "sha256:deadbeef")
	defer srv.Close()
	withAPIBase(t, srv.URL)

	rel, err := LatestRelease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "v1.2.0" || rel.Asset.Name != asset || rel.Asset.Size != 1234 {
		t.Fatalf("unexpected release: %+v", rel)
	}
	if rel.Asset.Digest != "sha256:deadbeef" {
		t.Fatalf("digest not parsed: %q", rel.Asset.Digest)
	}
}

func TestLatestReleaseWithoutPlatformAsset(t *testing.T) {
	srv := releaseServer(t, "some-other-platform.zip", "sha256:deadbeef")
	defer srv.Close()
	withAPIBase(t, srv.URL)

	rel, err := LatestRelease(context.Background())
	if err != nil {
		t.Fatalf("missing platform asset should not fail: %v", err)
	}
	if rel.Asset.URL != "" {
		t.Fatalf("expected empty asset, got %+v", rel.Asset)
	}
}
