package updater

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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
