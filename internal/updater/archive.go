package updater

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxArchiveFiles = 500
	maxArchiveEntry = 200 << 20
	maxArchiveUnzip = 500 << 20
)

// ExtractZip 把安装包解压到 destDir，拒绝路径穿越与符号链接。
//
// 解压阶段就做安全校验，比等到替换文件时再判断更可靠：
// 恶意/损坏的包不应该在安装目录里留下任何文件。
func ExtractZip(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("打开安装包失败: %w", err)
	}
	defer r.Close()

	root, err := filepath.Abs(destDir)
	if err != nil {
		return fmt.Errorf("解析目标目录失败: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("创建解压目录失败: %w", err)
	}

	var total int64
	var files int
	for _, entry := range r.File {
		name := strings.TrimSpace(entry.Name)
		if name == "" || strings.HasPrefix(name, "__MACOSX/") {
			continue
		}

		// 先按 zip 的 / 分隔规范化为本地路径，再判断是否越界。
		// 额外拒绝以分隔符开头或带盘符的条目：Windows 上 "\\abs\x" 不算 IsAbs，
		// 但同样不该出现在安装包里。
		if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "\\") || filepath.VolumeName(filepath.FromSlash(name)) != "" {
			return fmt.Errorf("安装包包含非法路径: %s", entry.Name)
		}
		clean := filepath.Clean(filepath.FromSlash(name))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("安装包包含非法路径: %s", entry.Name)
		}
		target := filepath.Join(root, clean)
		rel, err := filepath.Rel(root, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("安装包包含非法路径: %s", entry.Name)
		}

		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("创建目录失败: %w", err)
			}
			continue
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("安装包包含符号链接，已拒绝: %s", entry.Name)
		}

		files++
		if files > maxArchiveFiles {
			return fmt.Errorf("安装包文件数量异常，已中止")
		}
		if int64(entry.UncompressedSize64) > maxArchiveEntry {
			return fmt.Errorf("安装包内单个文件过大: %s", entry.Name)
		}
		total += int64(entry.UncompressedSize64)
		if total > maxArchiveUnzip {
			return fmt.Errorf("安装包解压后体积异常，已中止")
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("创建目录失败: %w", err)
		}
		if err := extractFile(entry, target); err != nil {
			return err
		}
	}

	if files == 0 {
		return fmt.Errorf("安装包为空")
	}
	return nil
}

func extractFile(entry *zip.File, target string) error {
	rc, err := entry.Open()
	if err != nil {
		return fmt.Errorf("读取安装包条目失败: %w", err)
	}
	defer rc.Close()

	mode := entry.Mode().Perm()
	if mode == 0 {
		mode = 0o644
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}
	if _, err := io.Copy(out, io.LimitReader(rc, maxArchiveEntry)); err != nil {
		_ = out.Close()
		return fmt.Errorf("解压文件失败: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("关闭文件失败: %w", err)
	}
	return nil
}
