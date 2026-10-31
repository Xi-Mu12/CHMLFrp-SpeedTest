package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// frpcTestServer 起一个本地"官方下载表 + frpc 文件"服务，
// 返回 server 和 payload，供续传 / 校验测试使用。
func frpcTestServer(t *testing.T, payload []byte) (*httptest.Server, *string) {
	t.Helper()
	sum := sha256.Sum256(payload)
	var gotRange string

	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/frpc_info.json", func(w http.ResponseWriter, r *http.Request) {
		platform, err := frpcPlatformName()
		if err != nil {
			t.Errorf("platform 解析失败: %v", err)
			return
		}
		resp := map[string]any{
			"msg":   "Success",
			"state": "success",
			"code":  200,
			"data": map[string]any{
				"downloads": []map[string]any{{
					"platform":  platform,
					"link":      srv.URL + "/frpc.exe",
					"hash":      hex.EncodeToString(sum[:]),
					"hash_type": "sha256",
					"size":      len(payload),
				}},
			},
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("写下载表失败: %v", err)
		}
	})
	mux.HandleFunc("/frpc.exe", func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		// ServeContent 自带 Range / 206 / 416 支持
		http.ServeContent(w, r, "frpc.exe", time.Time{}, bytes.NewReader(payload))
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	old := frpcInfoURL
	frpcInfoURL = srv.URL + "/frpc_info.json"
	t.Cleanup(func() { frpcInfoURL = old })

	return srv, &gotRange
}

// TestDownloadFrpcResume 前半段已落盘时，应从断点续传、拼出完整文件、
// 校验通过、改名到位并写下旁文件。
func TestDownloadFrpcResume(t *testing.T) {
	payload := bytes.Repeat([]byte("frpc-payload-"), 800)
	_, gotRange := frpcTestServer(t, payload)

	dir := t.TempDir()
	dest := filepath.Join(dir, frpcBinaryName())
	part := dest + ".part"
	half := len(payload) / 2
	if err := os.WriteFile(part, payload[:half], 0o644); err != nil {
		t.Fatalf("准备断点文件失败: %v", err)
	}

	if err := downloadFrpc(dest, nil); err != nil {
		t.Fatalf("续传下载失败: %v", err)
	}

	if want := "bytes=" + strconv.Itoa(half) + "-"; *gotRange != want {
		t.Errorf("请求头 Range 应为 %q，实际 %q", want, *gotRange)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("读取下载结果失败: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("续传后内容不完整：got %d 字节，want %d 字节", len(got), len(payload))
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Error(".part 应在完成后被改名，不应残留")
	}
	side, err := os.ReadFile(dest + frpcHashSuffix)
	if err != nil {
		t.Fatalf("旁文件未写入: %v", err)
	}
	sum := sha256.Sum256(payload)
	if string(side) != hex.EncodeToString(sum[:]) {
		t.Errorf("旁文件哈希不符: %s", side)
	}
}

// TestDownloadFrpcBadResume 断点内容被污染时，续传出来的文件必然哈希不符，
// 此时必须报错并删掉 .part，避免下次还在坏文件上续传。
func TestDownloadFrpcBadResume(t *testing.T) {
	payload := bytes.Repeat([]byte("frpc-payload-"), 800)
	frpcTestServer(t, payload)

	dir := t.TempDir()
	dest := filepath.Join(dir, frpcBinaryName())
	part := dest + ".part"
	half := len(payload) / 2
	if err := os.WriteFile(part, bytes.Repeat([]byte("X"), half), 0o644); err != nil {
		t.Fatalf("准备断点文件失败: %v", err)
	}

	if err := downloadFrpc(dest, nil); err == nil {
		t.Fatal("断点被污染时应校验失败")
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Error("校验失败后应删除 .part")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("校验失败后不应留下目标文件")
	}
}

// TestVerifyCachedFrpc 覆盖缓存复用的三条分支：哈希对得上、对不上、没有旁文件。
func TestVerifyCachedFrpc(t *testing.T) {
	payload := []byte("fake-frpc-binary")
	sum := sha256.Sum256(payload)

	write := func(t *testing.T, dir string, sideContent *string) string {
		t.Helper()
		p := filepath.Join(dir, frpcBinaryName())
		if err := os.WriteFile(p, payload, 0o644); err != nil {
			t.Fatalf("写缓存文件失败: %v", err)
		}
		if sideContent != nil {
			if err := os.WriteFile(p+frpcHashSuffix, []byte(*sideContent), 0o644); err != nil {
				t.Fatalf("写旁文件失败: %v", err)
			}
		}
		return p
	}

	t.Run("哈希一致", func(t *testing.T) {
		good := hex.EncodeToString(sum[:])
		p := write(t, t.TempDir(), &good)
		if !verifyCachedFrpc(p) {
			t.Fatal("哈希一致时应该可用")
		}
		if _, err := os.Stat(p); err != nil {
			t.Error("可用文件不应被删除")
		}
	})

	t.Run("哈希不一致", func(t *testing.T) {
		bad := "deadbeef"
		p := write(t, t.TempDir(), &bad)
		if verifyCachedFrpc(p) {
			t.Fatal("哈希不一致时应判定不可用")
		}
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Error("判定损坏后应删除文件")
		}
	})

	t.Run("无旁文件退化为大小判断", func(t *testing.T) {
		p := write(t, t.TempDir(), nil)
		if !verifyCachedFrpc(p) {
			t.Fatal("旧缓存（无旁文件）应放行，避免每次都重下")
		}
	})

	t.Run("空文件不可用", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), frpcBinaryName())
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatalf("写空文件失败: %v", err)
		}
		if verifyCachedFrpc(p) {
			t.Fatal("空文件应判定不可用")
		}
	})
}
