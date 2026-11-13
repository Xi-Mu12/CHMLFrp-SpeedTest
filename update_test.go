package main

import (
	"testing"

	"gui/internal/updater"
)

// 安装版必须优先吃安装包，否则卸载项/快捷方式会一直停在老版本。
func TestChooseUpdatePayloadPrefersSetupForInstalled(t *testing.T) {
	rel := &updater.Release{
		Asset:     updater.Asset{Name: "pkg.zip", URL: "https://example.com/pkg.zip"},
		Installer: updater.Asset{Name: "setup.exe", URL: "https://example.com/setup.exe"},
	}
	mode, asset := chooseUpdatePayload(rel, true)
	if mode != "setup" || asset.Name != "setup.exe" {
		t.Fatalf("安装版应选安装包，得到 mode=%s asset=%+v", mode, asset)
	}
}

// 绿色版不能走安装包：否则会被装到 %LOCALAPPDATA%\Programs，原目录留下孤儿文件。
func TestChooseUpdatePayloadUsesZipForPortable(t *testing.T) {
	rel := &updater.Release{
		Asset:     updater.Asset{Name: "pkg.zip", URL: "https://example.com/pkg.zip"},
		Installer: updater.Asset{Name: "setup.exe", URL: "https://example.com/setup.exe"},
	}
	mode, asset := chooseUpdatePayload(rel, false)
	if mode != "zip" || asset.Name != "pkg.zip" {
		t.Fatalf("绿色版应选 zip，得到 mode=%s asset=%+v", mode, asset)
	}
}

// 老版本 Release 只有 zip，安装版也得能继续更新。
func TestChooseUpdatePayloadFallsBackWhenNoInstaller(t *testing.T) {
	rel := &updater.Release{
		Asset: updater.Asset{Name: "pkg.zip", URL: "https://example.com/pkg.zip"},
	}
	mode, asset := chooseUpdatePayload(rel, true)
	if mode != "zip" || asset.Name != "pkg.zip" {
		t.Fatalf("缺少安装包时应退回 zip，得到 mode=%s asset=%+v", mode, asset)
	}
}
