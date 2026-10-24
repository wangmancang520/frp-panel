# FRP Panel

一键部署 [FRP](https://github.com/fatedier/frp) 并通过 Web 界面图形化管理。服务端（frps）和客户端（frpc）各运行一个面板；面板只提供 HTTPS 访问，不会以明文传输密码。

- **一键安装**：自动识别 CPU 架构，下载官方 FRP 并做 SHA-256 校验，生成配置、写入 systemd 服务、放行防火墙端口
- **服务端面板**：查看运行状态和实时流量，列出在线客户端和代理，表单编辑 frps.toml，一键复制客户端接入命令
- **客户端面板**：卡片式管理代理，提供 SSH、远程桌面、网站等模板，保存后热重载，每条代理显示状态和错误原因
- **通用功能**：实时日志、原始 TOML 编辑（保存前用 `frps/frpc verify` 校验）、FRP 在线升级、GitHub 镜像
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
在内网机器上运行以下命令即可接入：
curl -fsSL ... | sudo bash -s -- client --server '1.2.3.4' --port 7000 --token '...'
```

> 别忘了在云服务商**安全组**放行 `7000/tcp`（frps）、`7600/tcp`（面板），以及代理用到的端口。

### 2. 客户端（内网机器）

复制服务端打印的命令（面板「概览」页也有），在内网机器上运行。然后打开 `https://内网IP:7600` 添加代理。

### 管理命令

```bash
sudo bash install.sh status            # 查看状态、面板地址、证书指纹
sudo bash install.sh upgrade           # 升级 FRP 和面板
sudo bash install.sh reset-password    # 忘记密码时重置
sudo bash install.sh uninstall --purge # 卸载（--purge 同时删除配置）
```

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
| 密码明文传输 | 面板**只提供 HTTPS**。同一端口上的明文 HTTP 请求只会收到 308 重定向，不会返回页面，也不会读取请求内容 |
| 用 IP 访问没有证书 | 安装时生成 ECDSA 自签名证书，包含本机所有 IP 和检测到的公网 IP。浏览器会提示不受信任，可以核对安装时打印的 SHA-256 指纹来防范中间人攻击 |
| 想去掉浏览器警告 | 在「安全」页配置域名证书（Let's Encrypt 自动申请，或上传证书）。用域名访问时使用该证书，**用 IP 访问时仍然使用自签名证书** |
| FRP 自带 Dashboard 用明文 Basic Auth | frps/frpc 的 `webServer` 被强制绑定到 `127.0.0.1`，并使用随机凭据，只供面板在本机调用 |
| 暴力破解 | bcrypt（cost 12）；同一 IP 连续失败 5 次锁定 15 分钟，另有全局失败次数上限 |
| 会话劫持 / CSRF | Cookie 设置了 `__Host-` 前缀、`Secure`、`HttpOnly`、`SameSite=Strict`；写操作必须带自定义请求头，并校验 Origin |
| XSS / 点击劫持 | 严格 CSP（`script-src 'self'`，不允许内联脚本和外部资源）、`X-Frame-Options: DENY`、HSTS |
| 初始密码泄露 | 安装时随机生成，首次登录必须修改；修改密码后注销其他所有会话 |
| frpc ↔ frps 链路 | 服务端默认开启 `transport.tls.force`，客户端默认启用 TLS |
| 进程权限 | frps/frpc 以专用的 `frp` 用户运行，只授予 `CAP_NET_BIND_SERVICE`；配置文件权限为 `root:frp 0640` |
| 下载被篡改 | FRP 压缩包按官方 `frp_sha256_checksums.txt` 校验；面板程序按 release 中的 `sha256sums.txt` 校验 |

面板本身以 root 运行，因为它需要写配置、调用 systemctl 和替换二进制。

## 架构

```
frp-panel serve (root, HTTPS :7600)
 ├─ 读写 /etc/frp/frps.toml 或 frpc.toml（TOML 是唯一的配置来源；未知字段会保留）
 ├─ 保存前执行 frps/frpc verify；frps 保存后重启，frpc 保存后热重载（改连接参数时重启）
 ├─ 通过 systemctl / journalctl 控制服务、读取日志
 └─ 通过 127.0.0.1 上的 FRP 管理接口获取状态和流量
```

```
main.go                 init / serve / reset-password / info
internal/store          面板自身配置 panel.json
internal/tlsx           证书管理、HTTPS 监听（明文请求重定向）
internal/auth           bcrypt、会话、登录限流
internal/frp            TOML 读写、代理增删改、systemd/exec 进程管理、FRP 下载升级、管理接口客户端
internal/api            HTTP 接口
web/                    前端（Preact + htm，无需构建，已内置在 vendor/）
install.sh              一键安装脚本
build.sh                交叉编译 linux amd64 / arm64 / armv7
```

## 发布 / Fork

1. Fork 后，把 `install.sh` 和 `build.sh` 里的 `wangmancang520/frp-panel` 换成你的仓库
2. 推送 tag（如 `v0.1.0`），GitHub Actions 会自动编译并上传 release
3. 一键命令里的地址就是 `https://raw.githubusercontent.com/<你>/frp-panel/main/install.sh`

不想发布的话，可以本地编译后把整个目录拷到服务器：

```bash
./build.sh                                 # 生成 dist/
scp -r dist root@server:/root/frp-panel
ssh root@server 'cd /root/frp-panel && bash install.sh server'   # 会自动使用 dist/ 里的程序
```

## 本地开发

```bash
go test ./...
# 不依赖 systemd 的调试方式：--proc exec 让面板直接托管 frps/frpc 进程
go run . init --role server --data ./dev/s --frp-dir ./dev/s --bin-dir /path/to/frp --listen 127.0.0.1:7600 --proc exec
go run . serve --data ./dev/s
```

## 已知限制

- 安装脚本面向 Ubuntu/Debian + systemd；其他发行版可能可用，但未测试
- 表单覆盖常用的 tcp/udp/http/https/stcp/xtcp 代理；visitor、插件、负载均衡等需要在「配置文件」页编辑（保存时会保留这些配置）
- 通过面板保存配置时，TOML 中的注释不会保留
- 第一版每台机器各自管理；集中管理多台客户端尚未实现
