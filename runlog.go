// runlog.go —— 运行日志落盘与导出
// 前端 runLogs 只活在内存里，重启就没了；这里把每条日志同步追加到按天分文件的
// 日志目录，用户反馈问题时可以直接把当天文件发出来，不用靠猜。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// runLogKeepDays 日志保留天数，启动写入时顺手清掉更早的文件
const runLogKeepDays = 7

var (
	runLogMu   sync.Mutex
	runLogFile string // 当天文件路径，跨天自动切换
	runLogDay  string
)

// runLogDir 日志目录：<UserConfigDir>/CHMLFrp-SpeedTest/logs
func runLogDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "CHMLFrp-SpeedTest", "logs"), nil
}

// runLogSourceTag 与前端 logSourceLabel 保持一致
func runLogSourceTag(source string) string {
	if source == "prefer" {
		return "延迟"
	}
	return "带宽"
}

// runLogLevelTag 与前端 logLevelTag 保持一致
func runLogLevelTag(level string) string {
	switch level {
	case "ok":
		return "OK"
	case "err":
		return "ERR"
	case "warn":
		return "WARN"
	default:
		return "INFO"
	}
}

// AppendRunLog 追加一条运行日志到当天文件。
// 故意不返回 error：日志是辅助信息，写盘失败（磁盘满/只读）不能打断测速流程。
func (a *App) AppendRunLog(source, level, msg string) {
	dir, err := runLogDir()
	if err != nil {
		return
	}
	// 消息可能来自远端（节点名、接口错误串），压成单行避免伪造日志行
	msg = strings.ReplaceAll(strings.ReplaceAll(msg, "\r", " "), "\n", " ")
	line := fmt.Sprintf("%s [%s][%s] %s\n",
		time.Now().Format("2006-01-02 15:04:05"),
		runLogSourceTag(source), runLogLevelTag(level), msg)
	writeRunLogLine(dir, line)
}

// writeRunLogLine 串行写入，跨天时重建文件句柄并清理过期日志
func writeRunLogLine(dir, line string) {
	day := time.Now().Format("2006-01-02")
	runLogMu.Lock()
	defer runLogMu.Unlock()
	if runLogFile == "" || runLogDay != day {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}
		runLogFile = filepath.Join(dir, day+".log")
		runLogDay = day
		cleanOldRunLogs(dir)
	}
	f, err := os.OpenFile(runLogFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		runLogFile = "" // 下次重新尝试建目录/打开
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

// cleanOldRunLogs 删除超过保留天数的日志文件
func cleanOldRunLogs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -runLogKeepDays)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}

// GetRunLogPath 返回当天日志文件的完整路径，前端用于提示"日志保存在哪"
func (a *App) GetRunLogPath() string {
	dir, err := runLogDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, time.Now().Format("2006-01-02")+".log")
}

// ExportRunLog 把前端当前筛选出的日志导出成文件（用户可选保存位置）
func (a *App) ExportRunLog(content string) (string, error) {
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		DefaultFilename: "chmlfrp-speedtest-" + time.Now().Format("20060102-1504") + ".log",
		Filters: []runtime.FileFilter{
			{DisplayName: "日志文件 (*.log)", Pattern: "*.log"},
			{DisplayName: "文本文件 (*.txt)", Pattern: "*.txt"},
		},
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
