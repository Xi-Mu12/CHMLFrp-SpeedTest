# CHMLFrp-SpeedTest

<img src="./assets/CHMLFrp-SpeedTest.webp" alt="CHMLFrp-SpeedTest" width="160" />

**CHMLFrp 节点延迟测速与优选工具**

基于 Wails + Go + 原生 Web 前端构建的桌面应用，帮助你快速筛选延迟低、可用性好的 CHMLFrp 节点。

## 功能概览

- **延迟测速**：并发探测节点端口，通过真实 TCPing 统计平均延迟、收发次数、丢包率，并生成延迟排行榜。
- **节点筛选**：按全部、会员节点、普通节点筛选，也可以按名称或编号搜索。
- **节点信息**：从 CHMLFrp v2 API 获取节点名称、区域、节点组、国内/国外、建站、UDP 和防御等信息。
- **结果导出**：将节点列表和测速结果导出为 CSV 或 PNG 图片；未测速时导出的报告只包含节点清单，结果列留空。
- **带宽测速**：通过 frpc 隧道 + 本地回环测量真实上下行吞吐，提供速率曲线、节点进度、暂停/继续/停止和结果导出。
- **本机 IP 工具**：查看本机网卡地址、当前公网 IP 归属地，并支持查询指定公网 IP。
- **运行日志**：集中查看延迟测速和带宽测速过程中的成功、失败及警告信息。
- **账户与桌面体验**：支持可选的 OAuth2/OIDC 设备码登录、系统托盘、开机自启、关闭窗口隐藏到托盘，以及暗色/亮色/跟随系统主题。

## 当前状态

| 模块 | 状态 | 说明 |
| --- | --- | --- |
| 节点数据 | ✅ 可用 | 从 `https://cf-v2.uapis.cn/node` 自动拉取并渲染为节点卡片，无需 Token |
| 延迟测速 | ✅ 可用 | 真实 TCPing；并行探测 `7000`、`80`、`81` 端口，按丢包率和延迟排序 |
| CSV / PNG 导出 | ✅ 可用 | 通过系统保存对话框导出 |
| 本机 IP / 公网归属地 | ✅ 可用 | 本机地址由系统枚举，公网信息来自 ipwho.is |
| 托盘、开机自启、主题 | ✅ 可用 | 开机自启行为随平台实现不同 |
| OAuth2/OIDC 登录 | ✅ 可用 | 内置 Public Client `client_id`，浏览器确认设备码授权，支持环境变量覆盖 |
| 带宽测速 | ✅ 可用 | frpc 隧道 + 本地回环实测上下行吞吐；需登录，测速期间临时创建隧道（占用 1 个配额并消耗流量），测完立即删除 |

## 使用延迟测速

### 1. 准备节点 IP 列表

CHMLFrp API 返回的数据

### 2. 开始测速

1. 启动应用，点击“刷新”加载节点。
2. 按需筛选节点或搜索节点名称。
3. 点击节点卡片选择目标节点；不选择时默认测速全部可匹配节点。
4. 点击“开始测速”，等待结果完成后查看排行榜或导出结果。

测速参数当前为：每个节点 TCPing 4 次、单次超时 2 秒、最大并发 20；丢包率超过 50% 的节点会从排行榜中剔除。端口 `7000`、`80`、`81` 会并行探测，最终使用第一个可连接端口进行测速。

## 使用带宽测速

带宽测速走真实 frpc 隧道：应用在账号里为每个选中节点临时建一条 TCP 隧道，把节点上的远程端口映射回本机的回环数据源，然后经该隧道双向打流，测出这条通路的真实上下行吞吐。

1. 先在应用内登录 CHMLFrp 账号（带宽测速需要调用需要鉴权的隧道接口）。
2. 进入“带宽测速”，至少选择一个节点（会真实创建临时隧道、占用配额并消耗流量），再点击“开始测速”。
3. 应用会依次对每个节点：创建临时隧道 → 下载/复用 frpc 并按生成的配置启动 → 测量延迟 → 双向打流 → 结束后立即停止 frpc 并删除临时隧道。
4. 单个节点的结果会实时写入节点卡片，结束后可在排行榜查看并导出。

注意事项：

- 测速期间会占用 **1 个隧道配额**（串行执行，同一时间只有一条），并消耗账号流量额度；节点越多、时长越长，消耗越大。
- 单节点时长与单方向连接数可在「设置 → 测速」中调整：时长 5~60 秒（上下行各占一半），连接数 1~8（默认 6）。
- `frpc` 不随程序分发，首次使用会从官方下载表拉取当前平台客户端（sha256 校验）并缓存到用户缓存目录，之后直接复用；也可自行把 `frpc` 放到程序同目录。
- 打流时 frpc 会直连节点（已剔除系统代理变量），若你的网络必须经代理才能访问节点，隧道会建立失败并给出日志。

## 登录配置（可选覆盖）

登录使用 OAuth 2.0 设备码授权流程（RFC 8628），不使用或保存 `client_secret`。点击登录后，应用会在系统浏览器中打开授权确认页并显示配对码（形如 `XXXX-XXXX`）；在浏览器中确认后应用自动完成登录，不需要手动输入配对码，也不需要配置本地回调地址。应用已内置 Public Client `client_id`，通常无需额外配置。如需在开发环境切换客户端，可以设置：

```powershell
$env:QZHUA_CLIENT_ID = "你的 client_id"
```

应用的授权请求范围（scope）为 `profile offline_access chmlfrp_api`；邮箱等账户资料由 CHMLFrp `/userinfo` 接口获取。更换 client_id 时，需要保证新客户端在账户中心开启设备码授权并使用兼容的 scope。

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
├── app.go                   # 应用绑定与导出
├── bwtest.go                # 带宽测速引擎（逐节点建隧道、打流、清理）
├── bw_tunnel.go             # 隧道 API（列表/创建/删除）
├── bw_frpc.go               # frpc 下载缓存、配置生成与进程管理
├── bw_server.go             # 测速本地回环数据源
├── nodes.go                 # CHMLFrp 节点 API、分组过滤与候选解析
├── prefer.go                # 延迟测速流程、节点 IP 关联与排行榜事件
├── ping.go                  # TCPing、延迟/丢包/抖动统计
├── oauth.go                 # OAuth2/OIDC 设备码登录
├── tools.go                 # 本机 IP 与公网 IP 归属地查询
├── tray.go                  # 系统托盘与窗口显示/隐藏
├── autostart_*.go           # 各平台开机自启实现
├── frontend/                # 原生 HTML/CSS/JavaScript 前端
├── assets/                  # 应用展示资源
└── build/                   # Wails 图标、平台配置与构建资源
```

## 外部服务

- [CHMLFrp v2 API](https://cf-v2.uapis.cn)：获取节点列表；带宽测速时创建/删除临时隧道。
- [frpc 下载表](https://cf-v1.uapis.cn/download/frpc/frpc_info.json)：按平台拉取 frpc 客户端（sha256 校验后缓存，不随程序分发）。
- [ipwho.is](https://ipwho.is)：查询公网 IP 的地理位置、运营商、ASN、时区与旗帜；免密钥，1000 次/天。
- QZhua OIDC 服务：仅在配置 `QZHUA_CLIENT_ID` 后用于账户登录。

## 法律与隐私

- [隐私政策](./docs/privacy.html)
- [服务条款](./docs/terms.html)

## 许可证

[MIT License](./LICENSE)

## 作者

XiMu · [ximu.setback@qq.com](mailto:ximu.setback@qq.com)
