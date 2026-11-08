#!/usr/bin/env bash
# GoLand 运行配置调用：启动 Wails 开发模式（前端热重载 + Go 自动重启）。
# wails CLI 通常不在 PATH（go install 会装到 GOPATH/bin），这里按
# PATH → $GOPATH/bin 的顺序查找，避免把本机的绝对路径写进仓库。
set -e

find_wails() {
  if command -v wails >/dev/null 2>&1; then
    command -v wails
    return 0
  fi
  local gobin
  gobin="$(go env GOPATH 2>/dev/null)/bin"
  for name in wails wails.exe; do
    if [ -x "$gobin/$name" ]; then
      printf '%s\n' "$gobin/$name"
      return 0
    fi
  done
  return 1
}

if ! wails_bin="$(find_wails)"; then
  echo "未找到 wails CLI，先执行：go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0" >&2
  exit 1
fi

# 运行配置的工作目录未必是项目根，这里统一切到仓库根目录。
cd "$(dirname "$0")/.."
exec "$wails_bin" dev
