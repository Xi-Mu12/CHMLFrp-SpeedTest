#!/usr/bin/env bash
# 把 build/bin 里的 .app 打成 .dmg 安装镜像（内含 Applications 快捷方式，拖进去即可安装）。
#
# 用法: build/darwin/package-dmg.sh <arch> [outdir]
#   arch     arm64 / amd64，需与 wails build 的目标架构一致
#   outdir   输出目录，默认 dist
#
# 依赖：hdiutil / ditto（macOS 自带）。只在 macOS 上运行。
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "用法: $0 <arch> [outdir]" >&2
  exit 2
fi

arch="$1"
outdir="${2:-dist}"

if ! command -v hdiutil >/dev/null 2>&1; then
  echo "未找到 hdiutil，只能在 macOS 上打 dmg" >&2
  exit 1
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
app="$root/build/bin/CHMLFrp-SpeedTest.app"

if [[ ! -d "$app" ]]; then
  echo "未找到应用包: $app（先执行 wails build）" >&2
  exit 1
fi

stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT

# ditto 比 cp -R 更完整地保留 bundle 内的符号链接、权限与扩展属性。
ditto "$app" "$stage/CHMLFrp-SpeedTest.app"
ln -s /Applications "$stage/Applications"

mkdir -p "$outdir"
target="$outdir/CHMLFrp-SpeedTest-darwin-$arch.dmg"
rm -f "$target"
hdiutil create -volname "CHMLFrp 测速" -srcfolder "$stage" -ov -format UDZO "$target"
echo "已生成: $target"
