// Command updater 是自动更新的独立执行进程。
//
// 主程序把"等它退出后替换文件"这件事交给它：Windows 不允许覆盖正在运行的 EXE，
// 所以必须由一个不在安装目录里的进程动手。
//
// 它只接受主程序传入的明确参数（PID、暂存目录、目标目录），
// 不做任何联网操作，也不从外部读取额外指令。
package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type config struct {
	pid      int
	stage    string
	target   string
	exe      string
	relaunch bool
	logPath  string
}

type swapped struct {
	dst       string
	hadBackup bool
}

func main() {
	var c config
	flag.IntVar(&c.pid, "pid", 0, "主程序进程 ID")
	flag.StringVar(&c.stage, "stage", "", "解压后的新版本暂存目录")
	flag.StringVar(&c.target, "target", "", "安装目录")
	flag.StringVar(&c.exe, "exe", "", "更新后要启动的可执行文件名")
	flag.BoolVar(&c.relaunch, "relaunch", true, "替换成功后是否启动新版本")
	flag.StringVar(&c.logPath, "log", "", "日志文件路径")
	relocated := flag.Bool("relocated", false, "内部标记：已从安装目录迁移到临时目录")
	flag.Parse()

	logger := newLogger(c.logPath)
	defer logger.close()

	if c.stage == "" || c.target == "" || c.exe == "" {
		logger.printf("参数不完整：stage/target/exe 均为必填")
		os.Exit(2)
	}
	if c.pid <= 0 {
		logger.printf("无效的 PID: %d", c.pid)
		os.Exit(2)
	}

	stage, err := filepath.Abs(c.stage)
	if err == nil {
		c.stage = stage
	}
	target, err := filepath.Abs(c.target)
	if err == nil {
		c.target = target
	}

	// 如果自己正运行在安装目录里，先复制到临时目录再重启，
	// 否则替换 updater.exe 自身时会因文件被占用失败。
	if !*relocated {
		if err := relocateIfNeeded(&c, logger); err != nil {
			logger.printf("迁移更新进程失败: %v", err)
			os.Exit(3)
		}
	}

	logger.printf("等待主程序退出 (pid=%d)", c.pid)
	if err := waitForPID(c.pid, 180*time.Second); err != nil {
		logger.printf("等待主程序退出失败: %v", err)
		os.Exit(4)
	}
	// 进程刚退出时句柄可能还没完全释放，给系统一点时间。
	time.Sleep(600 * time.Millisecond)

	logger.printf("开始替换文件: %s -> %s", c.stage, c.target)
	done, err := apply(c.stage, c.target)
	if err != nil {
		rollback(done)
		logger.printf("替换失败，已回滚: %v", err)
		os.Exit(5)
	}
	commit(done)
	logger.printf("替换完成，共 %d 个文件", len(done))

	if c.relaunch {
		exePath := filepath.Join(c.target, c.exe)
		cmd := exec.Command(exePath)
		cmd.Dir = c.target
		if err := cmd.Start(); err != nil {
			logger.printf("启动新版本失败: %v", err)
			os.Exit(6)
		}
		logger.printf("已启动新版本: %s", exePath)
	}

	// 暂存目录和安装包由主程序下载，安装成功后一并清理。
	_ = os.RemoveAll(c.stage)
	logger.printf("更新完成")
}

// relocateIfNeeded 把自己复制到临时目录并重新执行，然后立即退出。
func relocateIfNeeded(c *config, logger *logger) error {
	self, err := os.Executable()
	if err != nil {
		return nil // 拿不到自身路径就按原样继续，替换失败会回滚
	}
	selfAbs, _ := filepath.Abs(self)
	targetAbs, _ := filepath.Abs(c.target)
	if !withinDir(selfAbs, targetAbs) {
		return nil
	}

	dir, err := os.MkdirTemp("", "chmlfrp-updater-")
	if err != nil {
		return err
	}
	dst := filepath.Join(dir, filepath.Base(selfAbs))
	if err := copyFile(selfAbs, dst); err != nil {
		return err
	}

	args := append([]string{}, os.Args[1:]...)
	args = append(args, "-relocated")
	cmd := exec.Command(dst, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	logger.printf("已迁移到临时目录并启动子进程: %s", dst)
	os.Exit(0)
	return nil
}

func withinDir(path, dir string) bool {
	path = strings.ToLower(filepath.Clean(path))
	dir = strings.ToLower(filepath.Clean(dir))
	return path == dir || strings.HasPrefix(path, dir+string(os.PathSeparator))
}

// apply 把暂存目录里的文件逐个搬到安装目录，覆盖前先备份。
func apply(stage, target string) ([]swapped, error) {
	var done []swapped
	err := filepath.WalkDir(stage, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(stage, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(target, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("创建目录失败: %w", err)
		}

		rec := swapped{dst: dst}
		if _, statErr := os.Stat(dst); statErr == nil {
			bak := dst + ".old"
			_ = os.Remove(bak)
			if err := os.Rename(dst, bak); err != nil {
				return fmt.Errorf("备份 %s 失败: %w", rel, err)
			}
			rec.hadBackup = true
		}
		if err := copyFileRetry(path, dst); err != nil {
			return fmt.Errorf("写入 %s 失败: %w", rel, err)
		}
		done = append(done, rec)
		return nil
	})
	if err != nil {
		return done, err
	}
	return done, nil
}

// rollback 逆序撤销已替换的文件：删掉新文件、恢复备份。
func rollback(done []swapped) {
	for i := len(done) - 1; i >= 0; i-- {
		r := done[i]
		_ = os.Remove(r.dst)
		if r.hadBackup {
			_ = os.Rename(r.dst+".old", r.dst)
		}
	}
}

// commit 成功后删除备份文件。
func commit(done []swapped) {
	for _, r := range done {
		if r.hadBackup {
			_ = os.Remove(r.dst + ".old")
		}
	}
}

// copyFileRetry 应对杀毒软件/索引服务短暂占用文件的情况。
func copyFileRetry(src, dst string) error {
	var err error
	for attempt := 0; attempt < 15; attempt++ {
		if err = copyFile(src, dst); err == nil {
			return nil
		}
		time.Sleep(400 * time.Millisecond)
	}
	return err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// ---------- 日志 ----------

type logger struct {
	f *os.File
}

func newLogger(path string) *logger {
	if path == "" {
		path = filepath.Join(os.TempDir(), "chmlfrp-updater.log")
	}
	// 日志保持在 1MB 以内，避免长期堆积。
	if info, err := os.Stat(path); err == nil && info.Size() > 1<<20 {
		_ = os.Remove(path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return &logger{}
	}
	return &logger{f: f}
}

func (l *logger) printf(format string, args ...any) {
	line := fmt.Sprintf("[%s] ", time.Now().Format("2006-01-02 15:04:05"))
	line += fmt.Sprintf(format, args...)
	line += "\n"
	if l.f != nil {
		_, _ = l.f.WriteString(line)
	}
}

func (l *logger) close() {
	if l.f != nil {
		_ = l.f.Close()
	}
}
