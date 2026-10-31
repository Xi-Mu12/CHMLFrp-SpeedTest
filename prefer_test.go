package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseChmlIPList 覆盖手抄 IP 列表的各种真实写法：
// 表头 / 分组标记行 / 正常行 / IP 与域名错位 / 只有 IP / 无 IP 的注释 / 空行 / 多空格。
func TestParseChmlIPList(t *testing.T) {
	content := strings.Join([]string{
		"节点名 IP 域名",
		"会员",
		"免费",
		"广州多线 8.134.162.189 gz.4.frp.one",
		"南京多线|175.27.138.59n j.2.frp.one",
		"香港 103.1.2.3",
		"# 纯注释行没有 IP",
		"",
		"上海多线    124.70.1.1     sh.5.frp.one",
	}, "\r\n")

	dir := t.TempDir()
	path := filepath.Join(dir, "chmlfrp.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("准备测试文件失败: %v", err)
	}

	got, err := parseChmlIPList(path)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	want := []rawIPEntry{
		{Name: "广州多线", IP: "8.134.162.189", Domain: "gz.4.frp.one"},
		// IP 后面的 "n j.2.frp.one" 应被拼成 nj.2.frp.one（修复错位），而不是丢掉
		{Name: "南京多线|", IP: "175.27.138.59", Domain: "nj.2.frp.one"},
		{Name: "香港", IP: "103.1.2.3", Domain: ""},
		{Name: "上海多线", IP: "124.70.1.1", Domain: "sh.5.frp.one"},
	}
	if len(got) != len(want) {
		t.Fatalf("条目数不符：got %d want %d（%+v）", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 条不符：\n got %+v\nwant %+v", i+1, got[i], want[i])
		}
	}
}

// TestParseChmlIPListMissing 文件不存在时必须返回错误，而不是空结果
func TestParseChmlIPListMissing(t *testing.T) {
	if _, err := parseChmlIPList(filepath.Join(t.TempDir(), "not-exist.txt")); err == nil {
		t.Fatal("文件不存在时应返回错误")
	}
}

// TestParseChmlIPListEmpty 全是注释/空行时返回空列表且不报错
func TestParseChmlIPListEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(path, []byte("节点名 IP 域名\n会员\n\n# 注释\n"), 0o644); err != nil {
		t.Fatalf("准备测试文件失败: %v", err)
	}
	got, err := parseChmlIPList(path)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("应返回空列表，实际 %+v", got)
	}
}
