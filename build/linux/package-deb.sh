#!/usr/bin/env bash
# 用 dpkg-deb 把 build/bin 里的 Linux 产物打成 .deb 安装包。
#
# 用法: build/linux/package-deb.sh <version> <arch> [outdir]
#   version  Debian 版本号，必须以数字开头（tag 的 v1.46.0 去掉 v；dev 构建用 0.0.0-dev）
#   arch     amd64 / arm64，需与 wails build 的目标架构一致
#   outdir   输出目录，默认 dist
#
# 依赖：dpkg-deb（Ubuntu/Debian 自带）。只在 Linux 上运行。
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "用法: $0 <version> <arch> [outdir]" >&2
  exit 2
fi

version="$1"
arch="$2"
outdir="${3:-dist}"

if [[ ! "$version" =~ ^[0-9] ]]; then
  echo "非法 Debian 版本号: $version（必须以数字开头）" >&2
  exit 1
fi
if ! command -v dpkg-deb >/dev/null 2>&1; then
  echo "未找到 dpkg-deb，请先安装 dpkg（apt-get install dpkg）" >&2
  exit 1
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
bin="$root/build/bin/CHMLFrp-SpeedTest"
pkg="chmlfrp-speedtest"

if [[ ! -f "$bin" ]]; then
  echo "未找到主程序: $bin（先执行 wails build）" >&2
  exit 1
fi

stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
# mktemp 默认 700；dpkg-deb 会把根目录的权限原样写进 data.tar，
# 安装时可能把 / 的权限改坏，所以显式改回 755。
chmod 0755 "$stage"

# 安装布局：主程序进 /usr/bin，桌面入口与图标交给桌面环境。
install -Dm755 "$bin" "$stage/usr/bin/$pkg"
install -Dm644 "$root/build/linux/$pkg.desktop" "$stage/usr/share/applications/$pkg.desktop"
install -Dm644 "$root/build/appicon.png" "$stage/usr/share/icons/hicolor/512x512/apps/$pkg.png"

mkdir -p "$stage/DEBIAN"

# 图标与桌面数据库缓存，装/卸后刷新；命令不存在时静默跳过，避免安装失败。
cat > "$stage/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e
if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database -q /usr/share/applications || true
fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
  gtk-update-icon-cache -q -t -f /usr/share/icons/hicolor || true
fi
exit 0
EOF
cp "$stage/DEBIAN/postinst" "$stage/DEBIAN/postrm"
chmod 0755 "$stage/DEBIAN/postinst" "$stage/DEBIAN/postrm"

# Wails v2 在 Linux 上链接 GTK3 + WebKitGTK 4.1 + AppIndicator；
# 依赖写在这里，用户 apt install 时缺什么会自动补齐。
# Ubuntu 24.04 把 libgtk-3-0 改名成了 libgtk-3-0t64，所以两种名字都接受。
cat > "$stage/DEBIAN/control" <<EOF
Package: $pkg
Version: $version
Section: net
Priority: optional
Architecture: $arch
Installed-Size: $(du -sk "$stage" | cut -f1)
Depends: libgtk-3-0 | libgtk-3-0t64, libwebkit2gtk-4.1-0, libayatana-appindicator3-1
Maintainer: XiMu <ximu.setback@qq.com>
Homepage: https://github.com/Xi-Mu12/CHMLFrp-SpeedTest
Description: CHMLFrp 节点带宽与延迟测速工具
 基于 Wails + Go + Web 前端构建的桌面应用，支持并发探测节点延迟、
 经 frpc 隧道实测上下行吞吐，另带本机 IP 归属地与 Minecraft 服务器查询。
EOF

mkdir -p "$outdir"
target="$outdir/CHMLFrp-SpeedTest-linux-$arch.deb"
# 固定用 xz：Ubuntu 的 dpkg 默认 zstd，但 Debian 12 以前的 dpkg 未必能解压 zstd。
dpkg-deb --root-owner-group --compression=xz --build "$stage" "$target"
echo "已生成: $target"
