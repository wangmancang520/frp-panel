# FRP Panel

一键部署 [FRP](https://github.com/fatedier/frp)，并在**服务端的一个 Web 面板里集中管理所有客户端**。面板只提供 HTTPS 访问，不会以明文传输密码。

- **服务端一键安装**：自动识别 CPU 架构，下载官方 FRP 并做 SHA-256 校验，生成配置、写入 systemd 服务、放行防火墙端口
- **客户端一条命令接入**：面板给出接入命令，在内网机器上运行即可。程序从服务端下载，本机无需任何配置，也不开放任何端口
- **集中管理**：在服务端面板为每台客户端添加代理（SSH、远程桌面、网站等模板），保存后几秒内自动下发并热重载；查看每台客户端的在线状态、代理状态、错误原因和日志
- **统一升级**：服务端升级 FRP 或面板后，客户端自动跟随升级
- **一体化管理工具**：每台机器上的 `sudo frp-panel-ctl` 提供交互菜单，可以查看状态、更新、回滚、查看日志和卸载；每次变更前自动备份
- **通用功能**：实时流量、frps 设置表单、原始 TOML 编辑（保存前用 `frps/frpc verify` 校验）、GitHub 镜像
- **单文件部署**：Go 后端内嵌前端，无需 Node、Nginx 或数据库

## 快速开始

### 1. 服务端（有公网 IP 的服务器）

```bash
curl -fsSL https://raw.githubusercontent.com/wangmancang520/frp-panel/main/install.sh | sudo bash -s -- server
```

国内服务器可加镜像：`... | sudo bash -s -- server --mirror https://ghfast.top/`

安装完成后会打印以下信息：

```
面板地址:   https://1.2.3.4:7600
用户名:     admin
初始密码:   xxxxxxxxxxxxxxxx   （首次登录后必须修改）
SHA-256:    0D:CF:A5:...          ← 浏览器证书警告时核对这个指纹
在内网机器上以 root 运行以下命令即可接入：
curl -fsSLk --pinnedpubkey 'sha256//...' 'https://1.2.3.4:7600/join.sh' | sudo bash -s -- agent --panel 'https://1.2.3.4:7600' --pin 'sha256//...' --key '...'
```

> 别忘了在云服务商**安全组**放行 `7000/tcp`（frps）、`7600/tcp`（面板，客户端也要连它），以及代理用到的端口。

### 2. 客户端（内网机器）

复制服务端打印的接入命令（面板「客户端」页也有），在内网机器上运行。同一条命令可以用于任意多台机器，每台注册后都有自己的凭据。

接入后，客户端会出现在服务端面板的「客户端」页。点进去就能添加代理、修改传输设置、查看日志、重启 frpc，客户端上不需要做任何操作。

### 管理：frp-panel-ctl

安装后，服务端和客户端上都有 `frp-panel-ctl`。不带参数运行会打开交互菜单，菜单内容按本机角色变化：

```bash
sudo frp-panel-ctl
```

| 菜单项 / 命令 | 服务端 | 客户端 |
|---|---|---|
| `status` 查看状态 | 版本、服务状态、面板地址、证书指纹、接入命令 | 版本、服务状态、所属面板、agent 最近日志 |
| `update` 更新 | 面板来源可选 GitHub 最新版、指定版本或本地 `dist/` 目录；FRP 可保持、升级到最新或指定版本 | 立即从面板重新下载 frp-panel 和 frpc（版本号相同也会重新下载） |
| `rollback` 回滚 | 恢复到某次备份：只恢复程序，或程序和配置一起恢复 | 同左 |
| `restart` / `logs` | 重启、查看日志（可持续跟踪） | 同左 |
| `reset-password` | 重置管理员密码 | — |
| `uninstall` | 卸载；加 `--purge` 同时删除配置 | 同左 |

每次更新、回滚、重新安装和 `--purge` 卸载前，都会自动把程序和配置备份到 `/var/backups/frp-panel`，保留最近 5 份。命令行用法见 `frp-panel-ctl --help`，例如：

```bash
sudo frp-panel-ctl update --panel-bin ./dist --keep-frp -y   # 用本地编译结果更新，不动 FRP
sudo frp-panel-ctl rollback -y                               # 回滚到最近一次备份（只恢复程序）
sudo frp-panel-ctl rollback --with-config --backup 20261026-213135
```

`frp-panel-ctl` 内置在 frp-panel 程序中，每次更新后自动换成新版本。客户端的 agent 在后台自动升级时，下次运行 `frp-panel-ctl` 也会先换成新版本。未安装时，直接运行 `install.sh`（不带参数）也会打开菜单。

### 多轮更新测试

1. `./build.sh` 编译全部架构到 `dist/`。版本号取自 `git describe --dirty`，也可以用 `VERSION=0.2.0-test1 ./build.sh` 指定，每轮用不同的版本号
2. 把 `dist/` 复制到服务端，运行 `sudo frp-panel-ctl update --panel-bin dist`。服务端会同时缓存其他架构的 frp-panel，开发版本也能分发给 ARM 客户端
3. 客户端会在一分钟内自动跟随服务端的 frp-panel 和 FRP 版本。想立即更新，可以在客户端运行 `sudo frp-panel-ctl update`
4. 有问题时运行 `sudo frp-panel-ctl rollback`，客户端会跟随服务端回到原来的版本

客户端被删除或需要彻底清理时，可以不依赖本地文件，直接用面板提供的脚本卸载：`curl -fsSLk --pinnedpubkey '<pin>' https://<服务端>:7600/join.sh | sudo bash -s -- uninstall --purge`。

## 客户端是怎么被管理的

```
内网机器                                        公网服务器
frp-panel agent ──HTTPS(公钥固定)──────────────▶ frp-panel serve :7600
  │  长轮询配置、每 10 秒上报状态                    │  clients.json：每台客户端的 frpc.toml
  │  下载 frpc / frp-panel                          │  按客户端渲染 serverAddr、端口、token
  ▼                                                ▼
frpc ─────────────── FRP 协议（TLS） ──────────▶ frps :7000
```

- **接入**：接入命令用 `curl --pinnedpubkey` 只信任面板自签名证书的公钥，从面板下载 install.sh 和对应架构的 frp-panel，再用接入密钥注册。注册后客户端拿到自己的凭据（面板只保存哈希），之后不再需要接入密钥
- **下发**：agent 长轮询面板（约 25 秒一次），配置或版本有变化时立即返回。agent 用 `frpc verify` 校验后写入 frpc.toml，只改了代理时热重载，否则重启 frpc
- **统一参数**：`serverAddr`、`serverPort`、`auth.token`、`user` 由服务端统一填写（`user` 是客户端 ID，因此不同客户端的代理名不会冲突）。修改 frps 的端口或令牌、或修改面板的公网地址后，所有客户端会自动更新
- **端口冲突**：保存代理时会检查其他客户端是否已占用同一个远程端口
- **程序分发**：服务端安装时同时装好 frpc。客户端架构与服务端相同时直接分发本机程序；不同时，面板从 GitHub 下载对应架构的官方 FRP（按校验文件验证）和 frp-panel 发布版本，并缓存在 `/etc/frp-panel/cache`
- **删除**：在面板删除客户端后，它的凭据立即失效，agent 会停止 frpc。重新运行接入命令可以再次接入
- **接入密钥泄露**：在「客户端」页点击「重置命令」，旧命令立即失效，已接入的客户端不受影响

## FRP 两端分别需要什么

| | 服务端 frps | 客户端 frpc |
|---|---|---|
| 程序 | `frps` | `frpc` |
| 必填 | `bindPort`（默认 7000）、`auth.token` | `serverAddr`、`serverPort`、`auth.token`（与服务端一致） |
| 代理规则 | — | `[[proxies]]`：`name`、`type`、`localIP`/`localPort`，以及 `remotePort`（tcp/udp）或 `customDomains`/`subdomain`（http/https） |
| 可选 | `vhostHTTPPort`/`vhostHTTPSPort`、`subDomainHost`、`allowPorts`、`transport.tls.force`、KCP/QUIC 端口 | 传输协议、TLS、加密/压缩、带宽限制 |
| 系统 | systemd 服务；防火墙和安全组放行 bindPort 及代理端口 | systemd 服务；不需要开放入站端口 |

面板会把这些整理成表单，并自动处理两端的 token、TLS 和管理接口。

## 安全设计

| 风险 | 措施 |
|---|---|
| 客户端被冒充或连到假面板 | 接入命令和 agent 只信任面板自签名证书的公钥（SHA-256 公钥固定），重新生成证书时会保留私钥，所以固定值不变；每台客户端有独立凭据，面板只存哈希；接入密钥可随时重置 |
| 密码明文传输 | 面板**只提供 HTTPS**。同一端口上的明文 HTTP 请求只会收到 308 重定向，不会返回页面，也不会读取请求内容 |
| 用 IP 访问没有证书 | 安装时生成 ECDSA 自签名证书，包含本机所有 IP 和检测到的公网 IP。浏览器会提示不受信任，可以核对安装时打印的 SHA-256 指纹来防范中间人攻击 |
| 想去掉浏览器警告 | 在「安全」页配置域名证书（Let's Encrypt 自动申请，或上传证书）。用域名访问时使用该证书，**用 IP 访问时仍然使用自签名证书** |
| FRP 自带 Dashboard 用明文 Basic Auth | frps/frpc 的 `webServer` 被强制绑定到 `127.0.0.1`，并使用随机凭据，只供面板在本机调用 |
| 暴力破解 | bcrypt（cost 12）；同一 IP 连续失败 5 次锁定 15 分钟，另有全局失败次数上限 |
| 会话劫持 / CSRF | Cookie 设置了 `__Host-` 前缀、`Secure`、`HttpOnly`、`SameSite=Strict`；写操作必须带自定义请求头，并校验 Origin |
| XSS / 点击劫持 | 严格 CSP（`script-src 'self'`，不允许内联脚本和外部资源）、`X-Frame-Options: DENY`、HSTS |
| 初始密码泄露 | 安装时随机生成，首次登录必须修改；修改密码后注销其他所有会话 |
| frpc ↔ frps 链路 | 服务端默认开启 `transport.tls.force`，客户端默认启用 TLS |
| 进程权限 | frps/frpc 以专用的 `frp` 用户运行，只授予 `CAP_NET_BIND_SERVICE`；配置文件权限为 `root:frp 0640`。客户端的 agent 以 root 运行（需要写配置、控制 frpc、升级程序），但只主动连接面板，不监听任何端口 |
| 下载被篡改 | FRP 压缩包按官方 `frp_sha256_checksums.txt` 校验；面板程序按 release 中的 `sha256sums.txt` 校验 |

面板本身以 root 运行，因为它需要写配置、调用 systemctl 和替换二进制。

> 注意：控制面板就等于控制所有客户端上 frpc 的配置，请使用强密码，并尽量把面板端口只对可信网络开放。

## 架构

```
frp-panel serve (服务端, root, HTTPS :7600)
 ├─ 读写 /etc/frp/frps.toml（TOML 是唯一的配置来源；未知字段会保留）
 ├─ /etc/frp-panel/clients.json：受管客户端及其 frpc 配置
 ├─ /agent/v1/*：agent 注册、配置长轮询、状态上报、程序下载；/join.sh：内嵌的 install.sh
 ├─ 通过 systemctl / journalctl 控制 frps、读取日志
 └─ 通过 127.0.0.1 上的 frps 管理接口获取状态和流量

frp-panel agent run (客户端, root, 不监听端口)
 ├─ 写 /etc/frp/frpc.toml，热重载或重启 frpc
 └─ 读取 127.0.0.1 上的 frpc 管理接口和 journalctl，上报给面板
```

```
main.go                 init / serve / agent enroll|run / reset-password / info
internal/store          面板自身配置 panel.json（含接入密钥）
internal/tlsx           证书管理、公钥固定值、HTTPS 监听（明文请求重定向）
internal/auth           bcrypt、会话、登录限流
internal/frp            TOML 读写、代理增删改、systemd/exec 进程管理、FRP 下载升级、管理接口客户端
internal/fleet          受管客户端注册表、按客户端渲染 frpc.toml、agent 协议类型
internal/agent          客户端 agent（注册、同步配置、升级、上报）
internal/api            HTTP 接口（面板 /api/*，agent /agent/v1/*）
web/                    前端（Preact + htm，无需构建，已内置在 vendor/）
install.sh              安装 / 更新 / 回滚 / 卸载管理脚本（安装后即 frp-panel-ctl），内嵌进程序，由面板在 /join.sh 提供
build.sh                交叉编译 linux amd64 / arm64 / armv7
```

## 发布 / Fork

1. Fork 后，把 `install.sh` 和 `build.sh` 里的 `wangmancang520/frp-panel` 换成你的仓库
2. 推送 tag（如 `v0.1.0`），GitHub Actions 会自动编译并上传 release
3. 服务端一键命令里的地址就是 `https://raw.githubusercontent.com/<你>/frp-panel/main/install.sh`；客户端接入命令由面板生成，不依赖 GitHub

客户端架构与服务端不同时，面板会从 `PANEL_REPO` 的 release 下载同版本的 frp-panel，所以要用打了 tag 的正式版本；开发版本只能接入与服务端相同架构的客户端。

不想发布的话，可以本地编译后把整个目录拷到服务器：

```bash
./build.sh                                 # 生成 dist/
scp -r dist root@server:/root/frp-panel
ssh root@server 'cd /root/frp-panel && bash install.sh server'   # 会自动使用 dist/ 里的程序；不带参数运行则打开交互菜单
```

## 本地开发

```bash
go test ./...
# 安装脚本的所有路径都可以用环境变量改到别处（FRP_PANEL_DATA_DIR、FRP_PANEL_FRP_DIR、FRP_PANEL_BIN_DIR、
# FRP_PANEL_SYSTEMD_DIR、FRP_PANEL_BACKUP_DIR、FRP_PANEL_CTL_PATH），FRP_PANEL_ALLOW_NONROOT=1 允许非 root 运行，
# 配合一个模拟的 systemctl / journalctl 就能在沙盒里反复测试安装、更新、回滚和卸载
# 不依赖 systemd 的调试方式：--proc exec 让面板直接托管 frps/frpc 进程
go run . init --role server --data ./dev/s --frp-dir ./dev/s --bin-dir /path/to/frp --listen 127.0.0.1:7600 --proc exec
go run . serve --data ./dev/s
```

## 已知限制

- 安装脚本面向 Ubuntu/Debian + systemd；其他发行版可能可用，但未测试
- 表单覆盖常用的 tcp/udp/http/https/stcp/xtcp 代理；visitor、插件、负载均衡等需要在客户端详情的「配置文件」页编辑（保存时会保留这些配置）
- 通过面板保存配置时，TOML 中的注释不会保留
- 旧版本的「客户端面板」已移除。旧客户端运行接入命令即可改为受管客户端，原有代理需要在服务端面板重新添加
- 面板的会话保存在内存中，面板重启后需要重新登录；agent 不受影响
