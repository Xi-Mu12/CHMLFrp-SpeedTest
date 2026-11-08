CHMLFrp 测速 v1.45.0

面向 CHMLFrp 用户的节点测速桌面工具，本次为 1.45.0 首个正式版。

本次发布内容

带宽测速
· 经 frpc 隧道 + 本地回环实测真实上下行吞吐，先下行后上行分时打流，避免两向互相挤占带宽
· 支持多隧道并行（默认 2 条），逐节点显示延迟、速率与进度，可暂停、继续、停止
· frpc 首次使用自动下载并校验 sha256，支持断点续传，缓存复用前复校验

延迟测速
· 真实 TCPing，每节点 4 次、单次超时 3 秒、最大并发 20
· 并行探测 7000 / 80 / 81 端口，取首个可连通端口，不硬编码单一端口
· 统计平均延迟、收发次数、丢包率与抖动，丢包超 50% 不计入排行榜

MC 服务器查询
· Java 版走 Server List Ping，未写端口时自动解析 _minecraft._tcp SRV 记录
· 基岩版走 RakNet Unconnected Ping
· 显示 MOTD（保留颜色与格式）、版本、协议号、在线人数与延迟，Java 版可显示服务器图标与在线玩家示例

其他功能
· 本机 IP 枚举与公网 IP 归属地查询（国家省市、邮编、区号、运营商、ASN、时区、经纬度）
· OAuth 2.0 设备码授权登录，不保存 client_secret，令牌自动续期
· CSV / PNG 结果导出，运行日志按天落盘保留 7 天
· 系统托盘、开机自启、关闭窗口隐藏到托盘、暗色 / 亮色 / 跟随系统主题

自动更新（Windows）

启动后静默检查新版本，发现新版本时展示本公告；确认后后台流式下载并校验 SHA256，
由独立的 updater.exe 在主程序退出后替换文件并重启。替换前自动备份，失败自动回滚。
可在设置中关闭自动检查，或跳过指定版本。

下载

· Windows：CHMLFrp-SpeedTest-windows-amd64.zip，解压即用，updater.exe 需与主程序放在同一目录
· Linux：CHMLFrp-SpeedTest-linux-amd64.zip
· macOS：CHMLFrp-SpeedTest-darwin-arm64.zip

Windows 10/11 需要 WebView2 Runtime（Win11 与新版 Win10 已内置）。

已知限制

· 自动安装仅支持 Windows，其他平台会提示前往下载页
· 带宽测速会真实创建临时隧道，占用隧道配额并消耗账号流量，请留意额度
· macOS 包未签名公证，首次打开需在「系统设置 → 隐私与安全性」中允许
· Linux 仅提供裸二进制，暂无 AppImage / deb

问题反馈

https://github.com/Xi-Mu12/CHMLFrp-SpeedTest/issues
