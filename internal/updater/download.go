package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxPackageBytes 是安装包体积上限。正常包 ~20MB，留足余量同时防止异常响应写满磁盘。
const maxPackageBytes = 300 << 20

// ProgressFunc 回调下载进度；total 可能为 0（服务端未给 Content-Length）。
type ProgressFunc func(downloaded, total int64)

// downloadClient 不设整体超时（大包可能下载很久），只限制各阶段超时。
var downloadClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	},
}

// Download 流式下载到 dest，边下边算 SHA256，校验通过后才落成正式文件。
//
// 任何失败（网络中断、取消、大小超限、摘要不符）都会删除 .part 临时文件，
// 不会把半成品留给安装阶段。
func Download(ctx context.Context, url, dest string, expectedSize int64, digest string, onProgress ProgressFunc) (err error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("创建下载目录失败: %w", err)
	}
	part := dest + ".part"
	_ = os.Remove(part)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("构造下载请求失败: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := downloadClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
	}

	total := resp.ContentLength
	if total <= 0 {
		total = expectedSize
	}
	if total > maxPackageBytes {
		return fmt.Errorf("安装包体积异常（%d 字节），已中止", total)
	}

	f, err := os.Create(part)
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	// 统一收尾：失败删临时文件，成功保留（后面重命名）。
	defer func() {
		_ = f.Close()
		if err != nil {
			_ = os.Remove(part)
		}
	}()

	hasher := sha256.New()
	writer := io.MultiWriter(f, hasher)
	buf := make([]byte, 64*1024)
	var written int64
	lastReport := time.Now()

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if written+int64(n) > maxPackageBytes {
				return fmt.Errorf("安装包超过大小上限，已中止")
			}
			if _, writeErr := writer.Write(buf[:n]); writeErr != nil {
				return fmt.Errorf("写入下载文件失败: %w", writeErr)
			}
			written += int64(n)
			if onProgress != nil && (time.Since(lastReport) >= 200*time.Millisecond || (total > 0 && written == total)) {
				onProgress(written, total)
				lastReport = time.Now()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("下载中断: %w", readErr)
		}
	}

	if total > 0 && written != total {
		return fmt.Errorf("下载不完整：%d/%d 字节", written, total)
	}

	actual := hex.EncodeToString(hasher.Sum(nil))
	want := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(digest)), "sha256:")
	if want == "" || !strings.EqualFold(actual, want) {
		return fmt.Errorf("SHA256 校验失败，已删除下载文件")
	}

	if err := f.Sync(); err != nil {
		return fmt.Errorf("刷新下载文件失败: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("关闭下载文件失败: %w", err)
	}
	if err := os.Rename(part, dest); err != nil {
		return fmt.Errorf("保存安装包失败: %w", err)
	}
	if onProgress != nil {
		onProgress(written, total)
	}
	return nil
}
