# CHMLFrp-SpeedTest

> CHMLFrp 节点测速与优选的 Windows 桌面工具。

CHMLFrp（基于 frp 的内网穿透服务）节点多且离散（不同机房 / 域名 / 端口），手动逐个试既慢又没头绪。本工具自动拉取节点列表、批量测延迟与带宽、给出排行榜，帮你直接挑出最优节点。

## 功能

- **节点优选**：批量 TCPing 测延迟，按「丢包优先、延迟其次」排序，产出 Top 排行榜；可只测勾选的节点
- **带宽测速**：对选中节点逐个测上下行吞吐，实时曲线 + 每节点圆环进度
- **排行榜**：测完自动出榜（Top3 + 其余），延迟越低越靠前
- **结果导出**：一键导出 CSV / PNG
- **节点筛选**：全部 / 会员(VIP) / 普通(user) 分组 + 名称搜索
- **账户登录**：OAuth2 / OIDC 授权登录（Authorization Code + PKCE，Public Client，无密钥泄露风险）
- **桌面体验**：无边框自绘窗口、系统托盘、开机自启、暗色 / 亮色 / 跟随系统主题

## 技术栈

- [Wails v2](https://wails.io) —— Go 后端 + Web 前端，WebView2，单文件 exe
- Go 后端：真实 TCPing 并发测速（对标 CloudflareSpeedTest 的延迟粗筛阶段），对接 CHMLFrp v2 API 拉取节点
- 事件流驱动：测速进度实时推送前端，可暂停 / 继续 / 取消

## 环境要求

- Windows 10/11（WebView2 运行时）
- 开发：[Go](https://go.dev) 1.21+、[Node.js](https://nodejs.org) 18+、[Wails CLI](https://wails.io/docs/gettingstarted/installation) v2

## 开发与构建

```bash
# 安装 Wails CLI（如未安装）
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# 实时开发（热重载）
wails dev

# 构建生产版本（输出到 build/bin/）
wails build
```

## 项目结构

```
├── main.go              # Wails 应用入口、窗口配置
├── app.go               # 后端业务：测速/优选事件流、导出
├── nodes.go             # CHMLFrp 节点拉取 / 过滤 / 候选解析
├── ping.go              # TCPing 延迟测速（丢包 / 延迟 / 抖动）
├── oauth.go             # OIDC 授权登录（PKCE）
├── tray.go              # 系统托盘
├── autostart_windows.go # 开机自启（注册表）
├── frontend/            # Web 前端（原生 HTML/CSS/JS + Vite）
└── build/               # Wails 构建资源（图标 / 安装包脚本 / 平台配置）
```

## 现状说明

节点拉取、UI、导出、登录、托盘、自启、主题均已真实可用；测速核心仍处于脚手架阶段（延迟与带宽数值为模拟数据，`ping.go` 已实现真实 TCPing，待接入事件流，带宽回显测速待接入）。后续将切换为真实测速，前端事件接口保持不变。

## 许可证

[MIT License](./LICENSE)

## 作者

XiMu · ximu.setback@qq.com
