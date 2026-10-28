#!/usr/bin/env bash
# FRP Panel 安装 / 更新 / 回滚 / 卸载 管理程序（Ubuntu / Debian，需要 systemd）
#
#   交互菜单： sudo frp-panel-ctl               （安装后可用；未安装时运行本脚本不带参数）
#   服务端：   curl -fsSL <url>/install.sh | sudo bash -s -- server
#   客户端：   在服务端面板「客户端」页复制接入命令（agent 模式，程序和配置都来自服务端）。
#              客户端可以接入多台服务端，本机管理用 frp-client（client.sh，服务端失联时也能用）
#   其他：     sudo frp-panel-ctl update | rollback | status | logs | restart | reset-password | uninstall
#
# 测试用：所有路径都可以用 FRP_PANEL_*_DIR 等环境变量改到别处（见下方），
# FRP_PANEL_ALLOW_NONROOT=1 允许非 root 运行，便于在沙盒中反复测试安装和更新。
set -euo pipefail

PANEL_REPO="${PANEL_REPO:-wangmancang520/frp-panel}"
FRP_REPO="fatedier/frp"
DATA_DIR="${FRP_PANEL_DATA_DIR:-/etc/frp-panel}"
FRP_DIR="${FRP_PANEL_FRP_DIR:-/etc/frp}"
BIN_DIR="${FRP_PANEL_BIN_DIR:-/usr/local/bin}"
SYSTEMD_DIR="${FRP_PANEL_SYSTEMD_DIR:-/etc/systemd/system}"
BACKUP_DIR="${FRP_PANEL_BACKUP_DIR:-/var/backups/frp-panel}"
CTL_PATH="${FRP_PANEL_CTL_PATH:-/usr/local/sbin/frp-panel-ctl}"
CLIENT_PATH="${FRP_CLIENT_PATH:-/usr/local/sbin/frp-client}"
LIB_DIR="${FRP_PANEL_LIB_DIR:-/usr/local/lib/frp-panel}"
FRP_USER=frp
KEEP_BACKUPS=5

ACTION=""
PANEL_PORT=7600
BIND_PORT=7000
PUBLIC_ADDR=""
MIRROR=""
FRP_VERSION=""     # empty = latest (install) / keep or latest (update, see FRP_MODE)
FRP_MODE=""        # update: keep | latest | version
PANEL_BIN=""       # local frp-panel binary, or a dist/ directory from build.sh
PANEL_VERSION=""   # GitHub release to install, e.g. 0.2.0 (empty = latest)
TOKEN=""
PANEL_URL=""
PIN=""
ENROLL_KEY=""
INSTANCE_NAME=""  # agent: instance name for this server on the client
FIREWALL=1
PURGE=0
FORCE=0
YES=0
WITH_CONFIG=0
BACKUP_ID=""
FOLLOW=0
INTERACTIVE=0

c_red=$'\e[31m'; c_grn=$'\e[32m'; c_ylw=$'\e[33m'; c_cyn=$'\e[36m'; c_dim=$'\e[2m'; c_b=$'\e[1m'; c_0=$'\e[0m'
info() { echo "${c_cyn}==>${c_0} $*"; }
ok()   { echo "${c_grn}✔${c_0} $*"; }
warn() { echo "${c_ylw}!${c_0} $*" >&2; }
die()  { echo "${c_red}✘ $*${c_0}" >&2; exit 1; }

usage() {
  cat <<EOF
FRP Panel 安装 / 管理程序

用法: install.sh [命令] [选项]        不带命令时打开交互菜单（安装后可用 sudo frp-panel-ctl）

命令:
  server              安装服务端（面板 + frps）
  agent               接入受管客户端（使用面板生成的接入命令；同一台机器可接入多台服务端）
  update              更新：服务端更新面板和 FRP；客户端立即从各面板重新下载程序
  rollback            服务端：回滚到更新前的备份
  status              查看版本、服务状态和访问信息
  logs                查看服务日志
  restart             重启本机的 FRP Panel 相关服务
  reset-password      服务端：重置管理员密码
  uninstall           卸载

服务端安装选项:
  --panel-port N      面板 HTTPS 端口（默认 7600）
  --bind-port N       frps 监听端口（默认 7000）
  --public-addr ADDR  本机公网 IP 或域名（默认自动检测）
  --token TOKEN       指定 frps 认证令牌（默认随机生成）
  --no-firewall       不自动配置 ufw / firewalld

服务端安装 / 更新时选择程序来源:
  --panel-bin PATH    使用本地 frp-panel 程序，或 build.sh 生成的 dist 目录（会同时提供各架构的客户端程序）
  --panel-version X   使用 GitHub 上的指定发布版本（默认最新版本）
  --frp-version X.Y.Z 指定 FRP 版本
  --frp-latest        更新时把 FRP 升级到最新版本（命令行 update 的默认行为）
  --keep-frp          更新时保持当前 FRP 版本
  --mirror URL        GitHub 下载镜像前缀，如 https://ghfast.top/

客户端（agent）选项，由接入命令自动填写:
  --panel URL         服务端面板地址，如 https://1.2.3.4:7600
  --pin PIN           面板证书公钥指纹（sha256//...）
  --key KEY           接入密钥
  --name NAME         本机上这台服务端的实例名称（默认按面板地址生成）
  --force             已接入时重新注册为新的客户端

客户端机器上的日常管理（列表、日志、删除某台服务端、卸载）请使用 frp-client，
它在服务端失联时也能使用。

其他选项:
  --backup ID         rollback：要恢复的备份（默认最近一次）
  --with-config       rollback：同时恢复配置（默认只恢复程序）
  --follow            logs：持续跟踪
  --purge             uninstall：同时删除配置和数据
  -y, --yes           不再确认
EOF
}

parse_args() {
  if [[ $# -gt 0 && "$1" != -* ]]; then ACTION="$1"; shift; fi
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --panel-port)    PANEL_PORT="$2"; shift 2 ;;
      --bind-port)     BIND_PORT="$2"; shift 2 ;;
      --public-addr)   PUBLIC_ADDR="$2"; shift 2 ;;
      --mirror)        MIRROR="$2"; shift 2 ;;
      --frp-version)   FRP_VERSION="${2#v}"; FRP_MODE=version; shift 2 ;;
      --frp-latest)    FRP_MODE=latest; shift ;;
      --keep-frp)      FRP_MODE=keep; shift ;;
      --panel-bin)     PANEL_BIN="$2"; shift 2 ;;
      --panel-version) PANEL_VERSION="${2#v}"; shift 2 ;;
      --token)         TOKEN="$2"; shift 2 ;;
      --panel)         PANEL_URL="$2"; shift 2 ;;
      --pin)           PIN="$2"; shift 2 ;;
      --key)           ENROLL_KEY="$2"; shift 2 ;;
      --name)          INSTANCE_NAME="$2"; shift 2 ;;
      --backup)        BACKUP_ID="$2"; shift 2 ;;
      --with-config)   WITH_CONFIG=1; shift ;;
      --follow|-f)     FOLLOW=1; shift ;;
      --no-firewall)   FIREWALL=0; shift ;;
      --purge)         PURGE=1; shift ;;
      --force)         FORCE=1; shift ;;
      -y|--yes)        YES=1; shift ;;
      -h|--help)       usage; exit 0 ;;
      *) die "未知参数: $1（使用 --help 查看帮助）" ;;
    esac
  done
  check_options
}

check_options() {
  if [[ -n "$MIRROR" ]]; then
    [[ "$MIRROR" == https://* ]] || die "--mirror 必须以 https:// 开头"
    MIRROR="${MIRROR%/}/"
  fi
  local p
  for p in "$PANEL_PORT" "$BIND_PORT"; do
    [[ "$p" =~ ^[0-9]+$ ]] && (( p >= 1 && p <= 65535 )) || die "端口无效: $p"
  done
}

# ---------- interaction ----------
have_tty() { [[ -r /dev/tty && -w /dev/tty ]] && { : </dev/tty; } 2>/dev/null; }

# input_closed stops the whole script (also from inside $(...)) when the
# terminal input ends, instead of carrying on with default answers.
input_closed() {
  echo >/dev/tty 2>/dev/null || true
  echo "${c_red}✘ 输入已结束，退出${c_0}" >&2
  kill -TERM "$$" 2>/dev/null
  exit 1
}

# ask PROMPT [DEFAULT]: prints the answer (prompt goes to the terminal).
ask() {
  local p="$1" d="${2:-}" v=""
  [[ -n "$d" ]] && p+=" ${c_dim}[${d}]${c_0}"
  read -r -p "$p: " v </dev/tty || input_closed
  echo "${v:-$d}"
}

# confirm PROMPT [DEFAULT y|n]: interactive yes/no; non-interactive runs proceed.
confirm() {
  (( YES )) && return 0
  (( INTERACTIVE )) || return 0
  local v
  v=$(ask "$1 (y/n)" "${2:-n}") || exit 1
  [[ "$v" =~ ^[Yy] ]]
}

# choose TITLE OPTION...: prints the 1-based index of the chosen option.
choose() {
  local title="$1" i v; shift
  {
    echo
    echo "${c_b}${title}${c_0}"
    for i in $(seq 1 $#); do echo "  ${c_cyn}${i})${c_0} ${!i}"; done
  } >/dev/tty
  while true; do
    v=$(ask "请选择" 1) || exit 1
    if [[ "$v" =~ ^[0-9]+$ ]] && (( v >= 1 && v <= $# )); then echo "$v"; return; fi
    echo "请输入 1-$# 之间的数字" >/dev/tty
  done
}

pause() { if (( INTERACTIVE )); then read -r -p "按回车继续…" _ </dev/tty || input_closed; fi; }

# ---------- environment ----------
require_root() {
  [[ $EUID -eq 0 || -n "${FRP_PANEL_ALLOW_NONROOT:-}" ]] || die "请使用 root 运行（sudo frp-panel-ctl 或 sudo bash install.sh ...）"
}

check_os() {
  command -v systemctl >/dev/null || die "需要 systemd"
  local rel="${FRP_PANEL_OS_RELEASE:-/etc/os-release}" os
  if [[ -r "$rel" ]]; then
    # Read it in a subshell: os-release defines NAME, VERSION, ID… which
    # must not overwrite this script's variables.
    # shellcheck disable=SC1091
    os=$(. "$rel" && echo "${ID:-}${ID_LIKE:-}|${PRETTY_NAME:-此系统}")
    case "${os%%|*}" in
      *ubuntu*|*debian*) ;;
      *) warn "未在 ${os#*|} 上测试，将继续尝试" ;;
    esac
  fi
}

install_deps() {
  local missing=() c
  for c in curl tar sha256sum; do command -v "$c" >/dev/null || missing+=("$c"); done
  if (( ${#missing[@]} )); then
    info "安装依赖: ${missing[*]}"
    if command -v apt-get >/dev/null; then
      DEBIAN_FRONTEND=noninteractive apt-get update -qq
      DEBIAN_FRONTEND=noninteractive apt-get install -y -qq curl tar coreutils ca-certificates >/dev/null
    else
      die "请先安装: ${missing[*]}"
    fi
  fi
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)  FRP_ARCH=amd64;  PANEL_ARCH=amd64 ;;
    aarch64|arm64) FRP_ARCH=arm64;  PANEL_ARCH=arm64 ;;
    armv7*|armhf)  FRP_ARCH=arm_hf; PANEL_ARCH=armv7 ;;
    *) die "不支持的 CPU 架构: $(uname -m)" ;;
  esac
}

# role: none | server | client | legacy-client. A server's config left behind
# by an uninstall without --purge does not count; a reinstall picks it up.
# "client" is a machine joined to one or more servers (managed by frp-client).
role() {
  if [[ -f "$DATA_DIR/panel.json" && -x "$BIN_DIR/frp-panel" ]]; then
    if grep -q '"role": "client"' "$DATA_DIR/panel.json"; then echo legacy-client; else echo server; fi
  elif [[ -f "$DATA_DIR/agent.json" ]] || compgen -G "$DATA_DIR/servers/*/agent.json" >/dev/null; then
    echo client
  else
    echo none
  fi
}

units_of() { case "$1" in server) echo "frps frp-panel" ;; legacy-client) echo "frpc frp-panel" ;; esac; }

# script_version FILE: FRP_CLIENT_SCRIPT_VERSION of a frp-client script (empty if missing).
script_version() { [[ -f "$1" ]] || return 0; sed -n 's/^FRP_CLIENT_SCRIPT_VERSION=\([0-9][0-9]*\).*/\1/p' "$1" | head -1; }

# run_client ARGS...: runs frp-client, which manages client machines. Falls
# back to the copy embedded in an installed frp-panel when it is missing.
run_client() {
  local s="$CLIENT_PATH" b
  if [[ ! -f "$s" ]]; then
    s="$WORK/frp-client"
    for b in "$LIB_DIR"/*/frp-panel "$BIN_DIR/frp-panel"; do
      [[ -x "$b" ]] && "$b" client-script >"$s" 2>/dev/null && [[ -s "$s" ]] && break
      rm -f "$s"
    done
    [[ -f "$s" ]] || die "找不到 frp-client。可以直接运行: curl -fsSL https://raw.githubusercontent.com/${PANEL_REPO}/main/client.sh | sudo bash -s -- $*"
  fi
  FRP_CLIENT_PATH="$CLIENT_PATH" bash "$s" "$@"
}

ver_panel() { "$BIN_DIR/frp-panel" version 2>/dev/null || echo "-"; }
ver_frp() { local v; v=$("$BIN_DIR/$1" -v 2>/dev/null) && echo "$v" || echo "-"; }

download() { # url output
  curl -fL --retry 3 --connect-timeout 15 --progress-bar "${MIRROR}$1" -o "$2" \
    || die "下载失败: ${MIRROR}$1$( [[ -z "$MIRROR" ]] && echo '（服务器在国内时可加 --mirror https://ghfast.top/ 重试）')"
}

# ---------- FRP ----------
latest_frp_version() {
  local url
  url=$(curl -fsSLI --connect-timeout 15 -o /dev/null -w '%{url_effective}' "${MIRROR}https://github.com/${FRP_REPO}/releases/latest" 2>/dev/null || true)
  if [[ "$url" =~ /tag/v([0-9]+\.[0-9]+\.[0-9]+) ]]; then echo "${BASH_REMATCH[1]}"; return; fi
  curl -fsSL --connect-timeout 15 "https://api.github.com/repos/${FRP_REPO}/releases/latest" 2>/dev/null \
    | grep -oE '"tag_name":\s*"v[0-9.]+"' | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' || true
}

# fetch_frp: downloads and verifies FRP $FRP_VERSION (default latest) into
# $WORK/frp/<pkg>/ without touching the installed binaries.
fetch_frp() {
  local tmp want got
  if [[ -z "$FRP_VERSION" ]]; then
    FRP_VERSION=$(latest_frp_version)
    [[ -n "$FRP_VERSION" ]] || die "无法获取 FRP 最新版本，请用 --frp-version 指定或设置 --mirror"
  fi
  FRP_PKG="frp_${FRP_VERSION}_linux_${FRP_ARCH}"
  info "下载 FRP v${FRP_VERSION} (${FRP_ARCH})"
  tmp="$WORK/frp"; mkdir -p "$tmp"
  local base="https://github.com/${FRP_REPO}/releases/download/v${FRP_VERSION}"
  download "${base}/frp_sha256_checksums.txt" "$tmp/sums.txt"
  download "${base}/${FRP_PKG}.tar.gz" "$tmp/frp.tar.gz"
  want=$(awk -v f="${FRP_PKG}.tar.gz" '$2==f {print $1}' "$tmp/sums.txt")
  got=$(sha256sum "$tmp/frp.tar.gz" | awk '{print $1}')
  [[ -n "$want" && "$want" == "$got" ]] || die "FRP 压缩包 SHA-256 校验失败（期望 ${want:-无}，实际 $got）"
  ok "SHA-256 校验通过"
  tar -xzf "$tmp/frp.tar.gz" -C "$tmp" "${FRP_PKG}/frps" "${FRP_PKG}/frpc"
}

place_frp() { # names...
  local name
  for name in "$@"; do
    install -m 0755 "$WORK/frp/${FRP_PKG}/${name}" "${BIN_DIR}/${name}"
    ok "已安装 ${BIN_DIR}/${name} ($(ver_frp "$name"))"
  done
}

# ---------- frp-panel binary (server) ----------
# fetch_panel: puts a frp-panel binary for this machine at $WORK/frp-panel.
# Sources, in order: --panel-bin (file or dist dir), ./dist next to this
# script, the GitHub release (--panel-version or latest).
fetch_panel() {
  local src="" script_dir
  PANEL_DIST=""
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || echo .)"
  if [[ -z "$PANEL_BIN" && -z "$PANEL_VERSION" && -f "$script_dir/dist/frp-panel-linux-${PANEL_ARCH}" ]]; then
    PANEL_BIN="$script_dir/dist"
  fi
  if [[ -n "$PANEL_BIN" ]]; then
    if [[ -d "$PANEL_BIN" ]]; then
      PANEL_DIST="$(cd "$PANEL_BIN" && pwd)"
      src="$PANEL_DIST/frp-panel-linux-${PANEL_ARCH}"
    else
      src="$PANEL_BIN"
    fi
    [[ -f "$src" ]] || die "找不到 $src"
    info "使用本地面板程序 $src"
    install -m 0755 "$src" "$WORK/frp-panel"
  else
    local base tmp="$WORK/panel" want got
    mkdir -p "$tmp"
    if [[ -n "$PANEL_VERSION" ]]; then
      base="https://github.com/${PANEL_REPO}/releases/download/v${PANEL_VERSION}"
      info "下载 FRP Panel v${PANEL_VERSION} (${PANEL_ARCH})"
    else
      base="https://github.com/${PANEL_REPO}/releases/latest/download"
      info "下载 FRP Panel 最新版本 (${PANEL_ARCH})"
    fi
    download "${base}/sha256sums.txt" "$tmp/sums.txt"
    download "${base}/frp-panel-linux-${PANEL_ARCH}" "$tmp/frp-panel"
    want=$(awk -v f="frp-panel-linux-${PANEL_ARCH}" '$2==f {print $1}' "$tmp/sums.txt")
    got=$(sha256sum "$tmp/frp-panel" | awk '{print $1}')
    [[ -n "$want" && "$want" == "$got" ]] || die "面板程序 SHA-256 校验失败"
    install -m 0755 "$tmp/frp-panel" "$WORK/frp-panel"
  fi
  NEW_PANEL_VERSION=$("$WORK/frp-panel" version 2>/dev/null) || die "新的 frp-panel 无法运行"
  # Releases before central client management lack the embedded script
  # (and the agent); installing one would leave a half-working setup.
  "$WORK/frp-panel" install-script >/dev/null 2>&1 \
    || die "frp-panel v${NEW_PANEL_VERSION} 版本过旧，与本脚本不匹配（缺少客户端集中管理）。请使用更新的发布版本（--panel-version），或用 build.sh 编译后通过 --panel-bin 指定"
}

place_panel() {
  install -m 0755 "$WORK/frp-panel" "${BIN_DIR}/frp-panel"
  ok "已安装 ${BIN_DIR}/frp-panel v$(ver_panel)"
}

# seed_agent_cache: with a dist dir, the panel can hand out this exact build
# to clients on other CPU architectures (otherwise it needs a GitHub release).
seed_agent_cache() {
  [[ -n "${PANEL_DIST:-}" ]] || return 0
  local v f n=0
  v=$(ver_panel)
  mkdir -p "$DATA_DIR/cache"
  for f in "$PANEL_DIST"/frp-panel-linux-*; do
    [[ -f "$f" && "$f" != *.txt ]] || continue
    [[ "$(basename "$f")" == "frp-panel-linux-${PANEL_ARCH}" ]] && continue
    install -m 0755 "$f" "$DATA_DIR/cache/$(basename "$f")-${v}"
    n=$((n + 1))
  done
  (( n )) && ok "已为其他架构的客户端准备 ${n} 个 frp-panel 程序（v${v}）"
  return 0
}

# install_ctl: frp-panel-ctl is the script embedded in the installed binary,
# so it always matches the installed version.
install_ctl() {
  local tmp="$WORK/ctl"
  if "$BIN_DIR/frp-panel" install-script >"$tmp" 2>/dev/null && [[ -s "$tmp" ]]; then
    mkdir -p "$(dirname "$CTL_PATH")"
    install -m 0755 "$tmp" "$CTL_PATH"
  fi
}

ensure_user() {
  id -u "$FRP_USER" >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin "$FRP_USER"
}

fix_perms() { # config file
  chown root:"$FRP_USER" "$FRP_DIR" "$1"
  chmod 0750 "$FRP_DIR"
  chmod 0640 "$1"
  chmod 0700 "$DATA_DIR"
}

# ---------- systemd units ----------
write_frp_unit() { # frps|frpc
  local name="$1" desc
  [[ "$name" == frps ]] && desc="FRP Server" || desc="FRP Client"
  cat > "${SYSTEMD_DIR}/${name}.service" <<EOF
[Unit]
Description=${desc} (managed by FRP Panel)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${FRP_USER}
Group=${FRP_USER}
ExecStart=${BIN_DIR}/${name} -c ${FRP_DIR}/${name}.toml
Restart=always
RestartSec=5s
LimitNOFILE=1048576
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
}

write_panel_unit() {
  cat > "${SYSTEMD_DIR}/frp-panel.service" <<EOF
[Unit]
Description=FRP Panel (HTTPS web UI)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${BIN_DIR}/frp-panel serve --data ${DATA_DIR}
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
EOF
}

open_ports() { # "7000/tcp 7600/tcp ..."
  (( FIREWALL )) || return 0
  local p
  if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
    for p in "$@"; do ufw allow "$p" >/dev/null && ok "ufw 已放行 $p" || warn "ufw 放行 $p 失败"; done
  elif command -v firewall-cmd >/dev/null && firewall-cmd --state >/dev/null 2>&1; then
    for p in "$@"; do firewall-cmd --permanent --add-port="$p" >/dev/null && ok "firewalld 已放行 $p" || warn "firewalld 放行 $p 失败"; done
    firewall-cmd --reload >/dev/null || true
  else
    info "未检测到启用中的 ufw / firewalld，跳过系统防火墙配置"
  fi
}

detect_public_ip() {
  [[ -n "$PUBLIC_ADDR" ]] && return
  PUBLIC_ADDR=$(curl -fsS4 --max-time 5 https://api.ipify.org 2>/dev/null || curl -fsS4 --max-time 5 https://ifconfig.me 2>/dev/null || true)
  [[ "$PUBLIC_ADDR" =~ ^[0-9.]+$ ]] || PUBLIC_ADDR=""
  [[ -n "$PUBLIC_ADDR" ]] && ok "检测到公网 IP: $PUBLIC_ADDR" || warn "未能检测公网 IP，可稍后在面板「系统」页设置"
}

# wait_active UNITS...: waits up to ~10s for each unit; returns 1 if any is down.
wait_active() {
  local u i bad=0
  for u in "$@"; do
    for i in $(seq 1 20); do systemctl is-active --quiet "$u" && break; sleep 0.5; done
    if systemctl is-active --quiet "$u"; then ok "$u 运行中"; else warn "$u 未能启动，查看日志: journalctl -u $u -n 50"; bad=1; fi
  done
  return $bad
}

# ---------- backups ----------
# backup_create REASON: copies binaries and configs to $BACKUP_DIR/<id>.
backup_create() {
  local id dir b r
  r=$(role)
  id="$(date +%Y%m%d-%H%M%S)"
  dir="$BACKUP_DIR/$id"
  [[ -e "$dir" ]] && { id="${id}-$$"; dir="$BACKUP_DIR/$id"; }
  mkdir -p "$dir/bin"
  chmod 0700 "$BACKUP_DIR" "$dir"
  for b in frp-panel frps frpc; do
    if [[ -x "$BIN_DIR/$b" ]]; then cp -p "$BIN_DIR/$b" "$dir/bin/$b"; fi
  done
  [[ -d "$DATA_DIR" ]] && tar -czf "$dir/data.tgz" -C "$DATA_DIR" --exclude=./cache .
  [[ -d "$FRP_DIR" ]] && tar -czf "$dir/frp.tgz" -C "$FRP_DIR" .
  {
    printf 'time=%q\n' "$(date '+%Y-%m-%d %H:%M:%S')"
    printf 'reason=%q\n' "$1"
    printf 'role=%q\n' "$r"
    printf 'panel=%q\n' "$(ver_panel)"
    printf 'frps=%q\n' "$(ver_frp frps)"
    printf 'frpc=%q\n' "$(ver_frp frpc)"
  } > "$dir/info"
  # Keep the newest $KEEP_BACKUPS.
  ls -1 "$BACKUP_DIR" | sort | head -n -"$KEEP_BACKUPS" | while read -r old; do rm -rf "${BACKUP_DIR:?}/$old"; done
  ok "已备份当前版本 → $dir"
  LAST_BACKUP="$id"
}

backup_list() { [[ -d "$BACKUP_DIR" ]] && ls -1 "$BACKUP_DIR" | sort -r || true; }

backup_desc() { # id
  local f="$BACKUP_DIR/$1/info"
  [[ -f "$f" ]] || { echo "$1"; return; }
  # shellcheck disable=SC1090
  ( . "$f"; echo "$time  面板 v${panel}  frps ${frps} frpc ${frpc}（${reason}）" ) 2>/dev/null || echo "$1"
}

# ---------- server ----------
prompt_server_options() {
  (( INTERACTIVE )) || return 0
  local i
  PANEL_PORT=$(ask "面板 HTTPS 端口" "$PANEL_PORT")
  BIND_PORT=$(ask "frps 监听端口" "$BIND_PORT")
  [[ -z "$PUBLIC_ADDR" ]] && detect_public_ip
  PUBLIC_ADDR=$(ask "公网 IP 或域名（客户端用它连接）" "$PUBLIC_ADDR")
  MIRROR=$(ask "GitHub 下载镜像（国内服务器可填 https://ghfast.top/，留空直连）" "$MIRROR")
  prompt_panel_source
  check_options
  echo
  echo "${c_b}即将安装服务端:${c_0} 面板端口 ${PANEL_PORT}，frps 端口 ${BIND_PORT}，公网地址 ${PUBLIC_ADDR:-未设置}"
  confirm "开始安装？" y || die "已取消"
}

prompt_panel_source() {
  (( INTERACTIVE )) || return 0
  [[ -n "$PANEL_BIN" || -n "$PANEL_VERSION" ]] && return 0
  local script_dir opts=("GitHub 最新发布版本" "GitHub 指定版本" "本地文件或 dist 目录（build.sh 的输出）") i
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || echo .)"
  [[ -d "$script_dir/dist" ]] && opts+=("脚本旁边的 dist 目录（$script_dir/dist）")
  i=$(choose "frp-panel 程序来源" "${opts[@]}")
  case "$i" in
    2) PANEL_VERSION=$(ask "版本号（如 0.2.0）"); PANEL_VERSION="${PANEL_VERSION#v}" ;;
    3) PANEL_BIN=$(ask "文件或目录路径") ;;
    4) PANEL_BIN="$script_dir/dist" ;;
  esac
}

do_server() {
  local out r had_data=0
  require_root; check_os; install_deps; detect_arch
  r=$(role)
  [[ -f "$DATA_DIR/panel.json" ]] && had_data=1
  case "$r" in
    client) die "本机是受管客户端，不能同时安装服务端（先用 frp-client uninstall 卸载）" ;;
    legacy-client) die "本机装有旧版客户端面板，请先卸载" ;;
  esac
  if (( had_data )); then
    warn "检测到服务端的配置。重新安装会保留 FRP 配置、客户端和接入密钥，但会重置管理员密码"
    confirm "继续？" y || die "已取消"
  fi
  prompt_server_options
  [[ -n "$PUBLIC_ADDR" ]] || detect_public_ip

  fetch_frp
  fetch_panel
  (( had_data )) && backup_create "重新安装前"
  systemctl stop frp-panel frps >/dev/null 2>&1 || true
  # frpc is installed too: the panel uses it to verify client configs and
  # hands it to agents on the same CPU architecture.
  place_frp frps frpc
  place_panel
  ensure_user

  local args=(init --role server --data "$DATA_DIR" --frp-dir "$FRP_DIR" --bin-dir "$BIN_DIR"
              --listen ":${PANEL_PORT}" --proc systemd --bind-port "$BIND_PORT")
  [[ -n "$MIRROR" ]] && args+=(--mirror "$MIRROR")
  [[ -n "$PUBLIC_ADDR" ]] && args+=(--public-addr "$PUBLIC_ADDR")
  [[ -n "$TOKEN" ]] && args+=(--token "$TOKEN")
  (( had_data )) && args+=(--force)
  out=$("${BIN_DIR}/frp-panel" "${args[@]}") || die "初始化面板失败"
  get() { grep "^$1=" <<<"$out" | head -1 | cut -d= -f2-; }

  fix_perms "$(get FRP_CONFIG)"
  seed_agent_cache
  install_ctl
  write_frp_unit frps
  write_panel_unit
  systemctl daemon-reload
  systemctl enable frps frp-panel >/dev/null 2>&1
  systemctl restart frps frp-panel

  open_ports "${BIND_PORT}/tcp" "${PANEL_PORT}/tcp"
  wait_active frps frp-panel || true

  local host="${PUBLIC_ADDR:-$(hostname -I 2>/dev/null | awk '{print $1}')}"
  echo
  echo "${c_b}════════════════════════ 安装完成 ════════════════════════${c_0}"
  echo "  面板地址:   ${c_grn}https://${host}:${PANEL_PORT}${c_0}"
  echo "  用户名:     admin"
  echo "  初始密码:   ${c_ylw}$(get ADMIN_PASSWORD)${c_0}   （首次登录后必须修改）"
  echo
  echo "  面板使用自签名证书，浏览器会提示“不安全”。请核对证书指纹一致后再继续："
  echo "  SHA-256:    $(get CERT_FINGERPRINT)"
  echo
  echo "  在内网机器上以 root 运行以下命令即可接入（面板「客户端」页也有）："
  echo "  ${c_cyn}$(get JOIN_COMMAND)${c_0}"
  echo "  客户端无需任何配置，代理在面板中统一添加。命令含接入密钥，请勿公开。"
  echo
  echo "  以后运行 ${c_b}sudo frp-panel-ctl${c_0} 即可更新、回滚、查看状态或卸载。"
  echo "  ${c_ylw}别忘了在云服务商安全组放行: ${BIND_PORT}/tcp（frps）、${PANEL_PORT}/tcp（面板，客户端也要连它），以及代理用到的端口${c_0}"
  echo "${c_b}═══════════════════════════════════════════════════════════${c_0}"
}

prompt_update_options() {
  (( INTERACTIVE )) || return 0
  prompt_panel_source
  if [[ -z "$FRP_MODE" ]]; then
    case "$(choose "FRP（frps/frpc）版本，当前 $(ver_frp frps)" "保持当前版本" "升级到最新版本" "指定版本")" in
      1) FRP_MODE=keep ;;
      2) FRP_MODE=latest ;;
      3) FRP_MODE=version; FRP_VERSION=$(ask "FRP 版本号（如 0.61.0）"); FRP_VERSION="${FRP_VERSION#v}" ;;
    esac
  fi
}

do_server_update() {
  local old_panel old_frp units="frp-panel"
  require_root; install_deps; detect_arch
  prompt_update_options
  [[ -n "$FRP_MODE" ]] || FRP_MODE=latest
  old_panel=$(ver_panel); old_frp=$(ver_frp frps)

  # Download everything first so the services are down as briefly as possible.
  fetch_panel
  if [[ "$FRP_MODE" != keep ]]; then
    [[ "$FRP_MODE" == latest ]] && FRP_VERSION=""
    fetch_frp
    units="frps frp-panel"
  fi
  echo
  echo "  面板: v${old_panel} → v${NEW_PANEL_VERSION}"
  [[ "$FRP_MODE" != keep ]] && echo "  FRP:  ${old_frp} → ${FRP_VERSION}"
  confirm "开始更新？" y || die "已取消"

  backup_create "更新前"
  # shellcheck disable=SC2086
  systemctl stop $units
  place_panel
  [[ "$FRP_MODE" != keep ]] && place_frp frps frpc
  seed_agent_cache
  install_ctl
  # shellcheck disable=SC2086
  systemctl start $units
  if wait_active frps frp-panel; then
    ok "更新完成。受管客户端会在一分钟内自动跟随更新"
  else
    warn "更新后服务未能正常启动。可以运行 sudo frp-panel-ctl rollback 回滚到更新前（备份 ${LAST_BACKUP}）"
    return 1
  fi
}

# ---------- agent ----------
# Client machines are managed by frp-client (client.sh, embedded in every
# frp-panel binary). The join command runs this script with "agent": it gets
# this server's frp-panel, takes frp-client out of it and lets frp-client do
# the join, so one machine can be joined to several servers.

# panel_get PATH OUTPUT: download from the server panel, trusting only its
# pinned key; authenticates with the enrollment key.
panel_get() {
  curl -fsSLk --pinnedpubkey "$PIN" --retry 3 --connect-timeout 15 -H "X-Enroll-Key: ${ENROLL_KEY}" \
    "${PANEL_URL%/}$1" -o "$2" \
    || die "从面板下载失败: ${PANEL_URL%/}$1（请确认面板可达，且安全组已放行面板端口）"
}

# parse_join CMD: takes --panel/--pin/--key out of a pasted join command.
parse_join() {
  local cmd="$1" re
  for re in panel pin key; do
    if [[ "$cmd" =~ --${re}[[:space:]]+\'([^\']*)\' ]] || [[ "$cmd" =~ --${re}[[:space:]]+([^[:space:]\']+) ]]; then
      case "$re" in
        panel) PANEL_URL="${BASH_REMATCH[1]}" ;;
        pin)   PIN="${BASH_REMATCH[1]}" ;;
        key)   ENROLL_KEY="${BASH_REMATCH[1]}" ;;
      esac
    fi
  done
}

prompt_join() {
  [[ -n "$PANEL_URL" && -n "$PIN" && -n "$ENROLL_KEY" ]] && return 0
  (( INTERACTIVE )) || return 0
  echo "请在服务端面板「客户端」页复制接入命令，粘贴到这里：" >/dev/tty
  parse_join "$(ask "接入命令")"
}

do_agent() {
  local script mine have
  require_root; check_os; install_deps; detect_arch
  prompt_join
  [[ -n "$PANEL_URL" && -n "$PIN" && -n "$ENROLL_KEY" ]] \
    || die "缺少 --panel / --pin / --key，请在服务端面板「客户端」页复制完整的接入命令"
  [[ "$PANEL_URL" == https://* ]] || die "--panel 必须是 https:// 地址"
  [[ "$(role)" == server ]] && die "本机已安装服务端面板，不能同时作为受管客户端"

  info "从面板下载 frp-panel (${FRP_ARCH})"
  panel_get "/agent/v1/download/frp-panel?arch=${FRP_ARCH}" "$WORK/frp-panel"
  chmod 0755 "$WORK/frp-panel"
  "$WORK/frp-panel" version >/dev/null 2>&1 || die "下载的 frp-panel 无法运行"
  "$WORK/frp-panel" client-script >"$WORK/frp-client" 2>/dev/null && [[ -s "$WORK/frp-client" ]] \
    || die "该服务端的面板版本过旧，不支持多服务端接入，请先升级服务端"

  # Another server may run a newer version whose frp-client is already
  # installed here; the newest script understands every older layout.
  script="$WORK/frp-client"
  mine=$(script_version "$WORK/frp-client"); have=$(script_version "$CLIENT_PATH")
  if [[ -f "$CLIENT_PATH" ]] && (( ${have:-0} > ${mine:-0} )); then script="$CLIENT_PATH"; fi

  local args=(join --panel "$PANEL_URL" --pin "$PIN" --key "$ENROLL_KEY" --binary "$WORK/frp-panel")
  [[ -n "$INSTANCE_NAME" ]] && args+=(--name "$INSTANCE_NAME")
  (( FORCE )) && args+=(--force)
  (( YES )) && args+=(-y)
  FRP_CLIENT_PATH="$CLIENT_PATH" bash "$script" "${args[@]}"
}

do_update() {
  case "$(role)" in
    server) do_server_update ;;
    client) run_client update --all ;;
    legacy-client) die "旧版客户端面板请改用服务端面板的接入命令重新安装" ;;
    *) die "未检测到安装" ;;
  esac
}

# ---------- rollback ----------
do_rollback() {
  local r ids id units b
  require_root
  r=$(role)
  [[ "$r" == none ]] && die "本机当前未安装。请先重新安装，再回滚（可选择连同配置一起恢复）"
  [[ "$r" == client ]] && die "客户端的程序跟随各服务端的版本，不需要回滚；可用 frp-client update 立即重新下载"
  mapfile -t ids < <(backup_list)
  (( ${#ids[@]} )) || die "没有可用的备份（${BACKUP_DIR}）"
  if [[ -n "$BACKUP_ID" ]]; then
    id="$BACKUP_ID"
  elif (( INTERACTIVE )); then
    local descs=() i
    for i in "${ids[@]}"; do descs+=("$(backup_desc "$i")"); done
    id="${ids[$(( $(choose "选择要恢复的备份（最新在前）" "${descs[@]}") - 1 ))]}"
    case "$(choose "恢复范围" "只恢复程序（保留现在的配置和客户端）" "程序和配置一起恢复（备份之后的修改会丢失）")" in
      2) WITH_CONFIG=1 ;;
    esac
  else
    id="${ids[0]}"
  fi
  [[ -d "$BACKUP_DIR/$id" ]] || die "备份不存在: $id"
  echo "  恢复到: $(backup_desc "$id")$( (( WITH_CONFIG )) && echo '（含配置）')"
  confirm "开始回滚？" y || die "已取消"

  backup_create "回滚前"
  units=$(units_of "$r")
  # shellcheck disable=SC2086
  [[ -n "$units" ]] && systemctl stop $units
  for b in frp-panel frps frpc; do
    if [[ -f "$BACKUP_DIR/$id/bin/$b" ]]; then install -m 0755 "$BACKUP_DIR/$id/bin/$b" "$BIN_DIR/$b"; fi
  done
  if (( WITH_CONFIG )); then
    if [[ -f "$BACKUP_DIR/$id/data.tgz" ]]; then
      find "$DATA_DIR" -mindepth 1 -maxdepth 1 ! -name cache -exec rm -rf {} +
      tar -xzf "$BACKUP_DIR/$id/data.tgz" -C "$DATA_DIR"
    fi
    if [[ -f "$BACKUP_DIR/$id/frp.tgz" ]]; then
      find "$FRP_DIR" -mindepth 1 -maxdepth 1 -exec rm -rf {} +
      tar -xzf "$BACKUP_DIR/$id/frp.tgz" -C "$FRP_DIR"
    fi
  fi
  install_ctl
  r=$(role); units=$(units_of "$r")
  # shellcheck disable=SC2086
  [[ -n "$units" ]] && systemctl start $units && wait_active $units || true
  ok "已回滚：frp-panel v$(ver_panel)，frps $(ver_frp frps)，frpc $(ver_frp frpc)"
}

# ---------- status / logs / misc ----------
do_status() {
  local r u
  r=$(role)
  if [[ "$r" == client ]]; then run_client list; return; fi
  echo "${c_b}角色:${c_0}      $(case "$r" in server) echo 服务端 ;; legacy-client) echo 旧版客户端面板 ;; *) echo 未安装 ;; esac)"
  [[ "$r" == none ]] && return 0
  echo "${c_b}版本:${c_0}      frp-panel v$(ver_panel)   frps $(ver_frp frps)   frpc $(ver_frp frpc)"
  echo "${c_b}服务:${c_0}"
  for u in $(units_of "$r"); do
    printf "  %-16s %s\n" "$u" "$(systemctl is-active "$u" 2>/dev/null || true)"
  done
  echo
  "$BIN_DIR/frp-panel" info --data "$DATA_DIR" 2>/dev/null || true
  local n
  n=$(backup_list | wc -l)
  echo
  echo "${c_b}备份:${c_0}      ${n} 个（${BACKUP_DIR}）"
}

do_logs() {
  local r units u
  r=$(role)
  if [[ "$r" == client ]]; then run_client logs $( (( FOLLOW )) && echo -f); return; fi
  units=$(units_of "$r")
  [[ -n "$units" ]] || die "未检测到安装"
  # shellcheck disable=SC2206
  local list=($units)
  u="${list[0]}"
  if (( INTERACTIVE )); then
    u="${list[$(( $(choose "查看哪个服务的日志" "${list[@]}") - 1 ))]}"
    [[ "$(choose "方式" "最近 100 行" "持续跟踪（Ctrl+C 返回）")" == 2 ]] && FOLLOW=1
  fi
  if (( FOLLOW )); then
    trap 'true' INT
    journalctl -u "$u" -n 30 -f --no-pager -o cat || true
    trap - INT
  else
    journalctl -u "$u" -n 100 --no-pager -o cat || true
  fi
}

do_restart() {
  local units
  require_root
  [[ "$(role)" == client ]] && { run_client restart --all; return; }
  units=$(units_of "$(role)")
  [[ -n "$units" ]] || die "未检测到安装"
  # shellcheck disable=SC2086
  systemctl restart $units
  # shellcheck disable=SC2086
  wait_active $units
}

do_reset_password() {
  require_root
  [[ "$(role)" == server ]] || die "只有服务端有面板账号"
  confirm "重置管理员密码？当前登录会全部失效" y || die "已取消"
  "$BIN_DIR/frp-panel" reset-password --data "$DATA_DIR"
  systemctl restart frp-panel
}

do_uninstall() {
  local u r
  require_root
  r=$(role)
  if [[ "$r" == client ]]; then run_client uninstall $( (( YES )) && echo -y); return; fi
  if (( INTERACTIVE )); then
    confirm "确定卸载 FRP Panel？" n || die "已取消"
    (( PURGE )) || { confirm "同时删除配置和数据（$FRP_DIR、$DATA_DIR）？" n && PURGE=1; } || true
  fi
  if (( PURGE )) && [[ "$r" != none ]]; then
    backup_create "卸载前"
  fi
  for u in frp-panel frps frpc; do
    if [[ -f "${SYSTEMD_DIR}/$u.service" ]]; then
      systemctl disable --now "$u" >/dev/null 2>&1 || true
      rm -f "${SYSTEMD_DIR}/$u.service"
    fi
  done
  systemctl daemon-reload
  rm -f "$BIN_DIR/frp-panel" "$BIN_DIR/frps" "$BIN_DIR/frpc" "$CTL_PATH"
  if (( PURGE )); then
    rm -rf "$DATA_DIR" "$FRP_DIR"
    userdel "$FRP_USER" >/dev/null 2>&1 || true
    ok "已卸载并删除配置（卸载前的备份保留在 ${BACKUP_DIR}，不需要可以手动删除）"
  else
    ok "已卸载（配置保留在 $FRP_DIR 和 $DATA_DIR，重新安装时会沿用；使用 --purge 一并删除）"
  fi
}

show_join() {
  [[ "$(role)" == server ]] || die "只有服务端有接入命令"
  "$BIN_DIR/frp-panel" info --data "$DATA_DIR" | sed -n '/客户端接入命令/,$p'
}

# ---------- menu ----------
header() {
  local r
  r=$(role)
  echo
  echo "${c_b}══════════════ FRP Panel 管理 ══════════════${c_0}"
  case "$r" in
    server) echo "  服务端   面板 v$(ver_panel) · $(ver_frp frps | sed 's/^/frps /') · frp-panel $(systemctl is-active frp-panel 2>/dev/null || true) · frps $(systemctl is-active frps 2>/dev/null || true)" ;;
    client) echo "  受管客户端（由 frp-client 管理）" ;;
    legacy-client) echo "  旧版客户端面板（已不再支持，请迁移为受管客户端）" ;;
    *) echo "  本机尚未安装" ;;
  esac
}

# run_action FN: runs one menu action in a subshell so a failure (die) returns
# to the menu instead of exiting.
run_action() {
  set +e
  ( set -e; "$@" )
  local rc=$?
  set -e
  (( rc == 0 )) || warn "操作未完成"
  pause
}

menu() {
  have_tty || die "没有可交互的终端。请指定命令，例如: install.sh status（--help 查看全部）"
  INTERACTIVE=1
  local r i
  while true; do
    header >/dev/tty
    r=$(role)
    case "$r" in
      none)
        i=$(choose "请选择操作" "安装服务端（公网服务器）" "安装客户端（粘贴服务端面板的接入命令）" "退出")
        case "$i" in 1) run_action do_server ;; 2) run_action do_agent ;; 3) return ;; esac ;;
      server)
        i=$(choose "请选择操作" "查看状态" "更新（面板 / FRP）" "回滚到备份" "重启服务" "查看日志" \
                   "显示客户端接入命令" "重置管理员密码" "重新安装" "卸载" "退出")
        case "$i" in
          1) run_action do_status ;; 2) run_action do_server_update ;; 3) run_action do_rollback ;;
          4) run_action do_restart ;; 5) run_action do_logs ;; 6) run_action show_join ;;
          7) run_action do_reset_password ;; 8) run_action do_server ;; 9) run_action do_uninstall ;; 10) return ;;
        esac ;;
      client)
        # Client machines have their own menu (several servers, offline use).
        run_client menu; return ;;
      legacy-client)
        i=$(choose "请选择操作" "迁移为受管客户端（粘贴服务端面板的接入命令）" "卸载" "退出")
        case "$i" in 1) run_action do_agent ;; 2) run_action do_uninstall ;; 3) return ;; esac ;;
    esac
  done
}

# refresh_self: when run as frp-panel-ctl, switch to the script embedded in
# the installed binary if it is newer (agents update themselves in the
# background, so the copy on disk can lag behind).
refresh_self() {
  [[ -z "${FRP_PANEL_CTL_REFRESHED:-}" && "${BASH_SOURCE[0]:-}" == "$CTL_PATH" && -x "$BIN_DIR/frp-panel" ]] || return 0
  local tmp
  tmp=$(mktemp)
  if "$BIN_DIR/frp-panel" install-script >"$tmp" 2>/dev/null && [[ -s "$tmp" ]] && ! cmp -s "$tmp" "$CTL_PATH"; then
    if [[ -w "$CTL_PATH" ]]; then
      install -m 0755 "$tmp" "$CTL_PATH"
      rm -f "$tmp"
      export FRP_PANEL_CTL_REFRESHED=1
      exec bash "$CTL_PATH" "$@"
    fi
  fi
  rm -f "$tmp"
}

main() {
  refresh_self "$@"
  parse_args "$@"
  WORK=$(mktemp -d)
  trap 'rm -rf "$WORK"' EXIT
  case "$ACTION" in
    ""|menu)        menu ;;
    server)         do_server ;;
    agent)          do_agent ;;
    update|upgrade) do_update ;;
    rollback)       do_rollback ;;
    status)         do_status ;;
    logs)           do_logs ;;
    restart)        do_restart ;;
    reset-password) do_reset_password ;;
    uninstall)      do_uninstall ;;
    client)         die "客户端请在服务端面板「客户端」页复制接入命令安装" ;;
    help)           usage ;;
    *) usage; exit 1 ;;
  esac
}

main "$@"
