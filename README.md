# FRP Panel

一键部署 [FRP](https://github.com/fatedier/frp)，并在**服务端的一个 Web 面板里集中管理所有客户端**。面板只提供 HTTPS 访问，不会以明文传输密码。

- **服务端一键安装**：自动识别 CPU 架构，下载官方 FRP 并做 SHA-256 校验，生成配置、写入 systemd 服务、放行防火墙端口
- **客户端一条命令接入**：面板给出接入命令，在内网机器上运行即可。程序从服务端下载，本机无需任何配置，也不开放任何端口
- **集中管理**：在服务端面板为每台客户端添加代理（SSH、远程桌面、网站等模板），保存后几秒内自动下发并热重载；查看每台客户端的在线状态、代理状态、错误原因和日志
- **统一升级**：服务端升级 FRP 或面板后，客户端自动跟随升级
- **一台客户端接入多台服务端**：每台服务端在客户端上是独立的实例，各自跟随所属服务端的版本，互不影响；重复运行接入命令会沿用原有身份
- **管理工具**：服务端用 `sudo frp-panel-ctl`（状态、更新、回滚、日志、卸载，变更前自动备份）；客户端用 `sudo frp-client`（列出已接入的服务端、日志、删除某台服务端、全部卸载），**服务端失联时也能使用**
- **通用功能**：实时流量、frps 设置表单、原始 TOML 编辑（保存前用 `frps/frpc verify` 校验）、GitHub 镜像
- **HTTPS 证书**：「证书」页一键为面板域名申请 Let's Encrypt 证书（先检查条件，失败时给出具体原因），也可以上传证书
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

- **接入多台服务端**：在同一台机器上运行另一台服务端的接入命令即可，每台服务端在本机是一个独立的实例（默认按面板地址命名，如 `45-192-243-39`，也可以加 `--name 名称` 指定）
- **重复运行接入命令**：同一台服务端的命令再运行一次，会沿用原有身份重新接入（重新下载程序和配置），不会产生重复的客户端；加 `--force` 才会注册为新的客户端

### 客户端管理：frp-client

接入后客户端上会有 `frp-client`。它只操作本机，**服务端失联时也能使用**，主要用来清理和卸载。不带参数运行会打开交互菜单：

```bash
sudo frp-client
```

| 命令 | 作用 |
|---|---|
| `list` | 列出已接入的服务端：实例名、面板地址、客户端 ID、frpc / agent 状态、代理数量、面板是否可连接（`--offline` 跳过连通性检查） |
| `show <名称>` | 某台服务端的详情：版本、本地管理端口、文件位置、本机当前的代理列表、agent 最近日志 |
| `logs <名称>` | frpc 日志（`--agent` 看 agent 日志，`-f` 持续跟踪） |
| `start` / `stop` / `restart <名称>` | 启停某台服务端的 frpc 和 agent（`--all` 全部） |
| `update <名称>` | 立即从该服务端面板重新下载 frp-panel 和 frpc（`--all` 全部；连不上的跳过） |
| `remove <名称>` | 删除一台服务端：先尝试在服务端注销本机，再停止并卸载它的 frpc 与 agent、清除本机配置。服务端失联时只清理本机（之后可在面板里手动删除这台客户端）；`--keep-on-server` 不注销 |
| `uninstall` | 删除全部服务端，并卸载客户端程序和 `frp-client` 本身 |
| `join` | 接入一台服务端（接入命令会自动调用，也可以在菜单里粘贴接入命令） |
| `migrate` | 把 v0.2 的单服务端布局迁移为实例布局（见下） |

本机的 `frp-client` 被误删时，可以直接从 GitHub 运行同一个脚本：

```bash
curl -fsSL https://raw.githubusercontent.com/wangmancang520/frp-panel/main/client.sh | sudo bash -s -- uninstall
```

**从 v0.2 升级的客户端**：v0.2 的客户端只能接入一台服务端（`frpc` / `frp-panel-agent` 服务）。接入新的服务端时会自动把它迁移为实例，客户端身份不变；如果它所属的服务端还没升级（程序太旧，不支持实例），就暂时保留原样继续运行，在 `frp-client list` 中显示为 `legacy`，等那台服务端升级后运行 `sudo frp-client migrate`。

### 服务端管理：frp-panel-ctl

服务端上有 `frp-panel-ctl`，不带参数运行会打开交互菜单：

```bash
sudo frp-panel-ctl
```

| 菜单项 / 命令 | 作用 |
|---|---|
| `status` 查看状态 | 版本、服务状态、面板地址、证书指纹、接入命令 |
| `update` 更新 | 面板来源可选 GitHub 最新版、指定版本或本地 `dist/` 目录；FRP 可保持、升级到最新或指定版本 |
| `rollback` 回滚 | 恢复到某次备份：只恢复程序，或程序和配置一起恢复 |
| `restart` / `logs` | 重启、查看日志（可持续跟踪） |
| `reset-password` | 重置管理员密码 |
| `uninstall` | 卸载；加 `--purge` 同时删除配置 |

在客户端上运行 `frp-panel-ctl` 或 `install.sh` 的 status / update / logs / restart / uninstall，会转交给 `frp-client`。

每次更新、回滚、重新安装和 `--purge` 卸载前，都会自动把程序和配置备份到 `/var/backups/frp-panel`，保留最近 5 份。命令行用法见 `frp-panel-ctl --help`，例如：

```bash
sudo frp-panel-ctl update --panel-bin ./dist --keep-frp -y   # 用本地编译结果更新，不动 FRP
sudo frp-panel-ctl rollback -y                               # 回滚到最近一次备份（只恢复程序）
sudo frp-panel-ctl rollback --with-config --backup 20261026-213135
```

`frp-panel-ctl` 和 `frp-client` 都内置在 frp-panel 程序中，会随更新自动换成新版本（客户端接入了多台不同版本的服务端时，保留最新的 `frp-client`，它兼容所有旧布局）。未安装时，直接运行 `install.sh`（不带参数）也会打开菜单。

### 多轮更新测试

1. `./build.sh` 编译全部架构到 `dist/`。版本号取自 `git describe --dirty`，也可以用 `VERSION=0.2.0-test1 ./build.sh` 指定，每轮用不同的版本号
2. 把 `dist/` 复制到服务端，运行 `sudo frp-panel-ctl update --panel-bin dist`。服务端会同时缓存其他架构的 frp-panel，开发版本也能分发给 ARM 客户端
3. 客户端会在一分钟内自动跟随服务端的 frp-panel 和 FRP 版本。想立即更新，可以在客户端运行 `sudo frp-client update --all`
4. 有问题时在服务端运行 `sudo frp-panel-ctl rollback`，客户端会跟随服务端回到原来的版本

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
- **删除**：在面板删除客户端后，它的凭据立即失效，agent 会停止 frpc。重新运行接入命令可以再次接入。反过来，在客户端运行 `frp-client remove` 时会先在服务端注销本机
- **一台客户端、多台服务端**：每台服务端在客户端上是一个独立的实例，有自己的身份、程序和服务，各自跟随所属服务端的版本：

  ```
  /etc/frp-panel/servers/<名称>/agent.json    身份（面板地址、公钥固定值、凭据、本地管理端口）
  /etc/frp/<名称>/frpc.toml                    frpc 配置
  /usr/local/lib/frp-panel/<名称>/             frp-panel 和 frpc 程序
  frpc@<名称>、frp-panel-agent@<名称>           systemd 服务
  ```

  服务端渲染的配置里 frpc 本地管理端口都是 7400，agent 写入前会换成本机为该实例分配的端口（从 7401 起），多个 frpc 因此不会冲突
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
| 想去掉浏览器警告 | 在「证书」页为面板域名申请 Let's Encrypt 证书（会先检查域名解析和 80 端口，申请后自动续期），或上传自己的证书。用域名访问时使用该证书，**用 IP 访问时仍然使用自签名证书**（客户端也不受影响）。需要在安全组放行 80/tcp。Let's Encrypt 模式下面板会一直占用 80 端口（验证和自动续期都要用），此时 frps 的 HTTP 端口不能设为 80（面板会拒绝保存并提示），可改用 8080，或改用上传证书；面板监听 443 时改用 TLS-ALPN 验证，不受此限制 |
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
client.sh               客户端本机管理脚本（安装后即 frp-client），内嵌进程序；接入、多服务端实例、离线清理与卸载
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
# FRP_PANEL_SYSTEMD_DIR、FRP_PANEL_BACKUP_DIR、FRP_PANEL_CTL_PATH，以及 client.sh 的 FRP_PANEL_LIB_DIR、
# FRP_CLIENT_PATH），FRP_PANEL_ALLOW_NONROOT=1 允许非 root 运行，
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
