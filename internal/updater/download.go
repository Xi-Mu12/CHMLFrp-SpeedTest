package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// maxPackageBytes 是安装包体积上限。正常包 ~20MB，留足余量同时防止异常响应写满磁盘。
const maxPackageBytes = 300 << 20

const (
	// 分段下载参数。GitHub 资产（objects.githubusercontent.com）在国内常被单连接
	// 限速到几百 KB/s，而带宽本身没占满；多条 Range 并发通常能把速度拉起来。
	maxSegments   = 6
	segmentFloor  = 2 << 20 // 小于 2MB 不分段：握手开销大于收益
	segmentMinLen = 256 << 10

	// 探测参数：每个候选源只取首块，用「首块耗时」估算吞吐，避免为了测速
	// 把整个包拉一遍。
	probeBytes    = 128 << 10
	probeTimeout  = 4 * time.Second
	fastEnoughBps = 2 << 20 // 直连就到 2MB/s 时不再探测镜像，省掉多余流量

	// 单个分段的尝试次数。镜像链路抖动比 GitHub 更常见，重试一次往往就够了。
	segmentAttempts = 2
)

// mirrorPrefixes 是 GitHub Release 的加速前缀，拼接方式为「前缀 + 完整原始地址」，
// 例如 https://ghfast.top/https://github.com/o/r/releases/download/v1/x.zip。
//
// 这些都是第三方公益服务，随时可能失效、限速或排队，所以只作为「直连慢/不通」时的
// 兜底：下载结果最终仍要用 Release API 给出的 SHA256 校验，镜像替换内容无法通过校验，
// 因此引入镜像是安全的（不可用只影响速度，不影响正确性）。
var mirrorPrefixes = []string{
	"https://ghfast.top/",
	"https://gh-proxy.com/",
	"https://ghproxy.net/",
}

// errRangeUnsupported 表示服务端（或中间层）忽略了 Range 请求。
// 此时按块写入会把整包写错位置，必须退回单连接下载。
var errRangeUnsupported = errors.New("服务端不支持分段下载")

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
	// 分段请求跟着 302 跳到 objects.githubusercontent.com 后，Range 头必须还在，
	// 否则拿到的会是整个文件。Go 默认会复制非敏感头，这里显式再设一次更保险。
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 10 {
			return errors.New("重定向次数过多")
		}
		if r := via[0].Header.Get("Range"); r != "" {
			req.Header.Set("Range", r)
		}
		if ua := via[0].Header.Get("User-Agent"); ua != "" {
			req.Header.Set("User-Agent", ua)
		}
		return nil
	},
}

// source 是一个可尝试的下载地址。
type source struct {
	label string
	url   string
}

// sourcesFor 生成候选源：GitHub 官方地址优先直连，随后追加国内加速镜像。
// 非 GitHub 地址（例如测试用的本地服务）原样返回，不做任何改写。
func sourcesFor(raw string) []source {
	srcs := []source{{label: "GitHub 直连", url: raw}}
	if strings.HasPrefix(raw, "https://github.com/") || strings.HasPrefix(raw, "http://github.com/") {
		for _, prefix := range mirrorPrefixes {
			srcs = append(srcs, source{label: prefix, url: prefix + raw})
		}
	}
	return srcs
}

// probe 是一次探测的结果。
type probe struct {
	src    source
	bps    float64 // 首块实测吞吐，仅用于排序
	ranged bool    // 是否支持 Range
	size   int64   // 文件总大小，0 表示未知
}

// probeSource 取 128KB 首块并计时，据此估算该源的吞吐。
// 失败（连不上、非 200/206）返回错误，调用方据此降级。
func probeSource(ctx context.Context, src source) (probe, error) {
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(pctx, http.MethodGet, src.url, nil)
	if err != nil {
		return probe{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	// 显式声明 identity：禁止透明 gzip，否则 Content-Length 与实际字节数不符。
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", probeBytes-1))

	start := time.Now()
	resp, err := downloadClient.Do(req)
	if err != nil {
		return probe{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return probe{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// 读满探测块再停：只测到首字节会把「连接慢但带宽大」的源误判。
	n, _ := io.CopyN(io.Discard, resp.Body, probeBytes)
	elapsed := time.Since(start)
	if n <= 0 {
		return probe{}, errors.New("探测未收到数据")
	}

	out := probe{src: src, ranged: resp.StatusCode == http.StatusPartialContent}
	if out.ranged {
		out.size = parseContentRangeTotal(resp.Header.Get("Content-Range"))
	} else if resp.ContentLength > 0 {
		out.size = resp.ContentLength
	}
	if secs := elapsed.Seconds(); secs > 0 {
		out.bps = float64(n) / secs
	}
	return out, nil
}

// parseContentRangeTotal 从 "bytes 0-65535/12345" 里取总长度；"*" 表示未知。
func parseContentRangeTotal(v string) int64 {
	i := strings.LastIndexByte(v, '/')
	if i < 0 {
		return 0
	}
	total := strings.TrimSpace(v[i+1:])
	if total == "" || total == "*" {
		return 0
	}
	n, err := strconv.ParseInt(total, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// planSources 决定候选的尝试顺序。
//
// 直连够快就只试直连；直连慢或不通时并发探测镜像（并发而非串行，避免等待时间叠加），
// 把实测更快的排在前面。全部探测失败时仍按原顺序硬试一遍——探测失败不等于下载一定失败。
func planSources(ctx context.Context, list []source) []probe {
	if len(list) == 0 {
		return nil
	}
	head, headErr := probeSource(ctx, list[0])
	if headErr == nil && head.bps >= fastEnoughBps {
		return []probe{head}
	}

	found := make([]probe, 0, len(list))
	if headErr == nil {
		found = append(found, head)
	}
	rest := list[1:]
	ch := make(chan probe, len(rest))
	var wg sync.WaitGroup
	for _, src := range rest {
		wg.Add(1)
		go func(s source) {
			defer wg.Done()
			if p, err := probeSource(ctx, s); err == nil {
				ch <- p
			}
		}(src)
	}
	wg.Wait()
	close(ch)
	for p := range ch {
		found = append(found, p)
	}

	if len(found) == 0 {
		fallback := make([]probe, 0, len(list))
		for _, s := range list {
			fallback = append(fallback, probe{src: s})
		}
		return fallback
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].bps > found[j].bps })
	return found
}

// Download 下载安装包到 dest，先写 .part，全部校验通过后才改名为正式文件。
//
// 流程：探测各源速度 → 选最快者 → 支持 Range 且体积够大就走并发分段，否则单连接
// → 落盘后统一做大小与 SHA256 校验。任一源失败自动换下一个，全部失败才报错。
// 任何失败（网络中断、取消、大小超限、摘要不符）都会删除 .part 临时文件，
// 不会把半成品留给安装阶段。
func Download(ctx context.Context, url, dest string, expectedSize int64, digest string, onProgress ProgressFunc) (err error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("创建下载目录失败: %w", err)
	}
	if expectedSize > maxPackageBytes {
		return fmt.Errorf("安装包体积异常（%d 字节），已中止", expectedSize)
	}

	part := dest + ".part"
	_ = os.Remove(part)
	// 无论从哪条路径退出，都不留下半成品。
	defer func() {
		if err != nil {
			_ = os.Remove(part)
		}
	}()

	var firstErr error
	for _, p := range planSources(ctx, sourcesFor(url)) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := fetch(ctx, p, part, expectedSize, onProgress); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if firstErr == nil {
				firstErr = err
			}
			_ = os.Remove(part)
			continue // 换下一个源
		}
		if err := verifyFile(part, expectedSize, digest); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			_ = os.Remove(part)
			continue // 镜像可能缓存了旧版本，换源还有机会
		}
		if err := os.Rename(part, dest); err != nil {
			return fmt.Errorf("保存安装包失败: %w", err)
		}
		if onProgress != nil {
			if st, statErr := os.Stat(dest); statErr == nil {
				onProgress(st.Size(), st.Size())
			}
		}
		return nil
	}
	if firstErr == nil {
		firstErr = errors.New("没有可用的下载源")
	}
	return fmt.Errorf("下载失败: %w", firstErr)
}

// fetch 从单个源把包完整拉到 part。
func fetch(ctx context.Context, p probe, part string, expectedSize int64, onProgress ProgressFunc) error {
	size := p.size
	if size <= 0 {
		size = expectedSize
	}
	// 已知总大小、支持 Range、且体积值得分段时才并发；否则老老实实单连接。
	if p.ranged && size >= segmentFloor && size <= maxPackageBytes {
		err := downloadSegmented(ctx, p.src, part, size, onProgress)
		if !errors.Is(err, errRangeUnsupported) {
			return err
		}
		_ = os.Remove(part)
	}
	return downloadStream(ctx, p.src, part, size, onProgress)
}

// downloadStream 单连接下载，边下边报进度。
func downloadStream(ctx context.Context, src source, part string, expected int64, onProgress ProgressFunc) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.url, nil)
	if err != nil {
		return fmt.Errorf("构造下载请求失败: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Encoding", "identity")

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
		total = expected
	}
	if total > maxPackageBytes {
		return fmt.Errorf("安装包体积异常（%d 字节），已中止", total)
	}

	f, err := os.Create(part)
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	defer f.Close()

	buf := make([]byte, 64*1024)
	var written int64
	lastReport := time.Now()

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if written+int64(n) > maxPackageBytes {
				return fmt.Errorf("安装包超过大小上限，已中止")
			}
			if _, writeErr := f.Write(buf[:n]); writeErr != nil {
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
	if err := f.Sync(); err != nil {
		return fmt.Errorf("刷新下载文件失败: %w", err)
	}
	return nil
}

// downloadSegmented 把文件切成若干段并发下载，各段用 WriteAt 写到自己的偏移，
// 互不覆盖，因此不需要锁文件。
func downloadSegmented(ctx context.Context, src source, part string, size int64, onProgress ProgressFunc) error {
	segments := int(size / segmentMinLen)
	if segments > maxSegments {
		segments = maxSegments
	}
	if segments < 2 {
		return errRangeUnsupported
	}

	f, err := os.Create(part)
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	defer f.Close()
	// 预先撑到最终大小：并发 WriteAt 落到稀疏区也没问题，但先占位便于失败时排查。
	if err := f.Truncate(size); err != nil {
		return fmt.Errorf("预分配文件失败: %w", err)
	}

	var written int64
	// 进度统一由一个 goroutine 按固定节奏汇报，避免 6 个 worker 各自刷 UI。
	stop := make(chan struct{})
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if onProgress != nil {
					onProgress(atomic.LoadInt64(&written), size)
				}
			}
		}
	}()

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	chunk := size / int64(segments)
	errCh := make(chan error, segments)
	var wg sync.WaitGroup
	var cancelOnce sync.Once

	for i := 0; i < segments; i++ {
		begin := int64(i) * chunk
		end := begin + chunk - 1
		if i == segments-1 {
			end = size - 1 // 除不尽的余数都归最后一段
		}
		wg.Add(1)
		go func(begin, end int64) {
			defer wg.Done()
			if segErr := fetchSegment(cctx, src, f, begin, end, &written); segErr != nil {
				cancelOnce.Do(cancel) // 一段失败即中止其余，免得白白耗流量
				errCh <- segErr
			}
		}(begin, end)
	}
	wg.Wait()
	close(stop)
	<-progressDone
	close(errCh)

	var firstErr error
	for e := range errCh {
		if firstErr == nil {
			firstErr = e
		}
	}
	if firstErr != nil {
		return firstErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	if got := atomic.LoadInt64(&written); got != size {
		return fmt.Errorf("下载不完整：%d/%d 字节", got, size)
	}
	if onProgress != nil {
		onProgress(size, size)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("刷新下载文件失败: %w", err)
	}
	return nil
}

// fetchSegment 带重试地下载一个闭区间段；失败时把已写入的字节从进度里扣回去。
func fetchSegment(ctx context.Context, src source, f *os.File, begin, end int64, written *int64) error {
	want := end - begin + 1
	var lastErr error
	for attempt := 0; attempt < segmentAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		got, err := readSegment(ctx, src, f, begin, end, written)
		if err == nil {
			if got != want {
				return fmt.Errorf("分段长度不符：%d/%d 字节", got, want)
			}
			return nil
		}
		if errors.Is(err, errRangeUnsupported) {
			return err // 换源或退单连接由上层决定，重试没有意义
		}
		if got > 0 {
			atomic.AddInt64(written, -got)
		}
		lastErr = err
	}
	return lastErr
}

// readSegment 下载 [begin,end] 并写入文件对应偏移，返回本次实际写入的字节数。
func readSegment(ctx context.Context, src source, f *os.File, begin, end int64, written *int64) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", begin, end))

	resp, err := downloadClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusPartialContent:
		// 正常
	case http.StatusOK:
		// 忽略了 Range 并返回整个文件：继续按块写会写错位置，交给上层降级。
		return 0, errRangeUnsupported
	default:
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	buf := make([]byte, 64*1024)
	var got int64
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if got+int64(n) > end-begin+1 {
				return got, fmt.Errorf("分段返回字节数超出范围")
			}
			if _, writeErr := f.WriteAt(buf[:n], begin+got); writeErr != nil {
				return got, fmt.Errorf("写入下载文件失败: %w", writeErr)
			}
			got += int64(n)
			atomic.AddInt64(written, int64(n))
		}
		if readErr == io.EOF {
			return got, nil
		}
		if readErr != nil {
			if ctx.Err() != nil {
				return got, ctx.Err()
			}
			return got, fmt.Errorf("下载中断: %w", readErr)
		}
	}
}

// verifyFile 校验落盘文件的大小与 SHA256。
//
// 摘要直接从文件重算（分段下载是乱序写入，没法边写边算），代价是一次本地读盘，
// 相对网络下载可以忽略。
func verifyFile(path string, expectedSize int64, digest string) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("读取下载文件失败: %w", err)
	}
	if st.Size() > maxPackageBytes {
		return fmt.Errorf("安装包体积异常（%d 字节），已中止", st.Size())
	}
	if expectedSize > 0 && st.Size() != expectedSize {
		return fmt.Errorf("下载不完整：%d/%d 字节", st.Size(), expectedSize)
	}

	want := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(digest)), "sha256:")
	if want == "" {
		return fmt.Errorf("安装包缺少 SHA256 摘要，已拒绝更新")
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开下载文件失败: %w", err)
	}
	defer f.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return fmt.Errorf("读取下载文件失败: %w", err)
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actual, want) {
		return fmt.Errorf("SHA256 校验失败，已删除下载文件")
	}
	return nil
}
