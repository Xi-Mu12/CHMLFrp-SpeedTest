# CHMLFrp-SpeedTest

<img src="./assets/CHMLFrp-SpeedTest.webp" alt="CHMLFrp-SpeedTest" width="160" />

**CHMLFrp 节点延迟测速与优选工具**

基于 Wails + Go + 原生 Web 前端构建的桌面应用，帮助你快速筛选延迟低、可用性好的 CHMLFrp 节点。

## 功能概览

- **延迟测速**：并发探测节点端口，通过真实 TCPing 统计平均延迟、收发次数、丢包率，并生成延迟排行榜。
- **节点筛选**：按全部、会员节点、普通节点筛选，也可以按名称或编号搜索。
- **节点信息**：从 CHMLFrp v2 API 获取节点名称、区域、节点组、国内/国外、建站、UDP 和防御等信息。
- **结果导出**：将节点列表和测速结果导出为 CSV 或 PNG 图片。
- **带宽测速界面**：提供上下行速率曲线、节点进度、暂停/继续/停止和结果导出等完整交互流程。
- **本机 IP 工具**：查看本机网卡地址、当前公网 IP 归属地，并支持查询指定公网 IP。
- **运行日志**：集中查看延迟测速和带宽测速过程中的成功、失败及警告信息。
- **账户与桌面体验**：支持可选的 OAuth2/OIDC + PKCE 登录、系统托盘、开机自启、关闭窗口隐藏到托盘，以及暗色/亮色/跟随系统主题。

## 当前状态

| 模块 | 状态 | 说明 |
| --- | --- | --- |
| 节点列表 | ✅ 可用 | 从 `https://cf-v2.uapis.cn/node` 获取，无需 Token |
| 延迟测速 | ✅ 可用 | 真实 TCPing；并行探测 `7000`、`80`、`81` 端口，按丢包率和延迟排序 |
| CSV / PNG 导出 | ✅ 可用 | 通过系统保存对话框导出 |
| 本机 IP / 公网归属地 | ✅ 可用 | 本机地址由系统枚举，公网信息来自 ip-api.com |
| 托盘、开机自启、主题 | ✅ 可用 | 开机自启行为随平台实现不同 |
| OAuth2/OIDC 登录 | ⚙️ 可选 | 需要配置 `QZHUA_CLIENT_ID`，详见下文 |
| 带宽测速 | ✅ 可用 | frpc 隧道 + 本地回环实测上下行吞吐；测速期间临时创建隧道，用后立即删除 |

## 使用延迟测速

### 1. 准备节点 IP 列表

CHMLFrp API 返回的数据

### 2. 开始测速

1. 启动应用，点击“刷新”加载节点。
2. 按需筛选节点或搜索节点名称。
3. 点击节点卡片选择目标节点；不选择时默认测速全部可匹配节点。
4. 点击“开始测速”，等待结果完成后查看排行榜或导出结果。

测速参数当前为：每个节点 TCPing 4 次、单次超时 2 秒、最大并发 20；丢包率超过 50% 的节点会从排行榜中剔除。端口 `7000`、`80`、`81` 会并行探测，最终使用第一个可连接端口进行测速。

## 登录配置（可选）

登录使用桌面端 Authorization Code + PKCE 流程，不使用或保存 `client_secret`。启动前设置 OAuth 客户端 ID：

```powershell
$env:QZHUA_CLIENT_ID = "你的 client_id"
go run .
```

也可以在构建或运行应用前，将 `QZHUA_CLIENT_ID` 配置到系统环境变量。默认回调地址为：

```text
http://127.0.0.1:49152/oauth/callback
```

如 OAuth 服务端登记了其他本地回调地址，可通过 `QZHUA_REDIRECT_URI` 覆盖，但必须使用 `127.0.0.1` 的 HTTP 地址和明确端口。

## 环境要求

- Windows 10/11：需要 WebView2 Runtime。
- Go 1.25 或更高版本。
- Node.js 20 或更高版本。
- Wails CLI v2.16.0。
- Linux/macOS 构建还需要 Wails 对应的平台依赖。

## 开发与构建

在项目根目录执行：

```bash
# 安装 Wails CLI
go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0

# 开发模式：启动前端热更新和 Wails 桌面窗口
wails dev

# 构建生产版本，输出到 build/bin/
wails build -clean
```

如果只需要单独调试前端：

```bash
cd frontend
npm install
npm run dev
```

仓库中的 GitHub Actions 工作流支持手动构建 Windows、Linux 和 macOS 产物：进入 **Actions → Build → Run workflow** 即可触发。

## 项目结构

```text
.
├── main.go                  # Wails 应用入口与窗口配置
├── app.go                   # 应用绑定、带宽测速事件流、导出
├── nodes.go                 # CHMLFrp 节点 API、分组过滤与候选解析
├── prefer.go                # 延迟测速流程、节点 IP 关联与排行榜事件
├── ping.go                  # TCPing、延迟/丢包/抖动统计
├── oauth.go                 # OAuth2/OIDC + PKCE 登录
├── tools.go                 # 本机 IP 与公网 IP 归属地查询
├── tray.go                  # 系统托盘与窗口显示/隐藏
├── autostart_*.go           # 各平台开机自启实现
├── frontend/                # 原生 HTML/CSS/JavaScript 前端
├── assets/                  # 应用展示资源
└── build/                   # Wails 图标、平台配置与构建资源
```

## 外部服务

- [CHMLFrp v2 API](https://cf-v2.uapis.cn)：获取节点列表。
- [ip-api.com](http://ip-api.com)：查询公网 IP 的地理位置、运营商和 ASN；免费接口有访问频率限制。
- QZhua OIDC 服务：仅在配置 `QZHUA_CLIENT_ID` 后用于账户登录。

## 法律与隐私

- [隐私政策](./docs/privacy.html)
- [服务条款](./docs/terms.html)

## 许可证

[MIT License](./LICENSE)

## 作者

XiMu · [ximu.setback@qq.com](mailto:ximu.setback@qq.com)
