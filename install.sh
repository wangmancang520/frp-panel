#!/usr/bin/env bash
# FRP Panel 一键安装脚本（Ubuntu / Debian，需要 systemd）
#
#   服务端： curl -fsSL <url>/install.sh | sudo bash -s -- server
#   客户端： curl -fsSL <url>/install.sh | sudo bash -s -- client --server 1.2.3.4 --token xxx
#   其他：   sudo bash install.sh upgrade | status | reset-password | uninstall [--purge]
set -euo pipefail

PANEL_REPO="${PANEL_REPO:-wangmancang520/frp-panel}"
FRP_REPO="fatedier/frp"
DATA_DIR=/etc/frp-panel
FRP_DIR=/etc/frp
BIN_DIR=/usr/local/bin
FRP_USER=frp

ACTION=""
PANEL_PORT=7600
BIND_PORT=7000
PUBLIC_ADDR=""
MIRROR=""
FRP_VERSION=""
PANEL_BIN=""
SERVER_ADDR=""
SERVER_PORT=7000
TOKEN=""
FIREWALL=1
PURGE=0

c_red=$'\e[31m'; c_grn=$'\e[32m'; c_ylw=$'\e[33m'; c_cyn=$'\e[36m'; c_b=$'\e[1m'; c_0=$'\e[0m'
info() { echo "${c_cyn}==>${c_0} $*"; }
ok()   { echo "${c_grn}✔${c_0} $*"; }
warn() { echo "${c_ylw}!${c_0} $*" >&2; }
die()  { echo "${c_red}✘ $*${c_0}" >&2; exit 1; }

usage() {
  cat <<EOF
FRP Panel 安装脚本

用法: install.sh <server|client|upgrade|status|reset-password|uninstall> [选项]

通用选项:
  --panel-port N      面板 HTTPS 端口（默认 7600）
  --mirror URL        GitHub 下载镜像前缀，如 https://ghfast.top/
  --frp-version X.Y.Z 指定 FRP 版本（默认最新）
  --panel-bin PATH    使用本地 frp-panel 二进制，而不是从 GitHub 下载
  --no-firewall       不自动配置 ufw / firewalld

服务端选项:
  --bind-port N       frps 监听端口（默认 7000）
  --public-addr ADDR  本机公网 IP 或域名（默认自动检测）
  --token TOKEN       指定认证令牌（默认随机生成）

客户端选项:
  --server ADDR       frps 服务器地址（必填）
  --port N            frps 端口（默认 7000）
  --token TOKEN       认证令牌（必填）

卸载选项:
  --purge             同时删除配置（/etc/frp、/etc/frp-panel）
EOF
}

parse_args() {
  [[ $# -gt 0 ]] || { usage; exit 1; }
  ACTION="$1"; shift
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --panel-port)  PANEL_PORT="$2"; shift 2 ;;
      --bind-port)   BIND_PORT="$2"; shift 2 ;;
      --public-addr) PUBLIC_ADDR="$2"; shift 2 ;;
      --mirror)      MIRROR="$2"; shift 2 ;;
      --frp-version) FRP_VERSION="${2#v}"; shift 2 ;;
      --panel-bin)   PANEL_BIN="$2"; shift 2 ;;
      --server)      SERVER_ADDR="$2"; shift 2 ;;
      --port)        SERVER_PORT="$2"; shift 2 ;;
      --token)       TOKEN="$2"; shift 2 ;;
      --no-firewall) FIREWALL=0; shift ;;
      --purge)       PURGE=1; shift ;;
      -h|--help)     usage; exit 0 ;;
      *) die "未知参数: $1（使用 --help 查看帮助）" ;;
    esac
  done
  if [[ -n "$MIRROR" ]]; then
    [[ "$MIRROR" == https://* ]] || die "--mirror 必须以 https:// 开头"
    MIRROR="${MIRROR%/}/"
  fi
  for p in "$PANEL_PORT" "$BIND_PORT" "$SERVER_PORT"; do
    [[ "$p" =~ ^[0-9]+$ ]] && (( p >= 1 && p <= 65535 )) || die "端口无效: $p"
  done
}

# ---------- environment ----------
require_root() { [[ $EUID -eq 0 ]] || die "请使用 root 运行（sudo bash install.sh ...）"; }

check_os() {
  command -v systemctl >/dev/null || die "需要 systemd"
  if [[ -r /etc/os-release ]]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    case "${ID:-}${ID_LIKE:-}" in
      *ubuntu*|*debian*) ;;
      *) warn "未在 ${PRETTY_NAME:-此系统} 上测试，将继续尝试安装" ;;
    esac
  fi
}

install_deps() {
  local missing=()
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

install_frp() { # binary name: frps|frpc
  local name="$1" tmp pkg want got
  if [[ -z "$FRP_VERSION" ]]; then
    FRP_VERSION=$(latest_frp_version)
    [[ -n "$FRP_VERSION" ]] || die "无法获取 FRP 最新版本，请用 --frp-version 指定或设置 --mirror"
  fi
  pkg="frp_${FRP_VERSION}_linux_${FRP_ARCH}"
  info "下载 FRP v${FRP_VERSION} (${FRP_ARCH})"
  tmp="$WORK/frp"; mkdir -p "$tmp"
  local base="https://github.com/${FRP_REPO}/releases/download/v${FRP_VERSION}"
  download "${base}/frp_sha256_checksums.txt" "$tmp/sums.txt"
  download "${base}/${pkg}.tar.gz" "$tmp/frp.tar.gz"
  want=$(awk -v f="${pkg}.tar.gz" '$2==f {print $1}' "$tmp/sums.txt")
  got=$(sha256sum "$tmp/frp.tar.gz" | awk '{print $1}')
  [[ -n "$want" && "$want" == "$got" ]] || die "FRP 压缩包 SHA-256 校验失败（期望 ${want:-无}，实际 $got）"
  ok "SHA-256 校验通过"
  tar -xzf "$tmp/frp.tar.gz" -C "$tmp" "${pkg}/${name}"
  install -m 0755 "$tmp/${pkg}/${name}" "${BIN_DIR}/${name}"
  ok "已安装 ${BIN_DIR}/${name} ($("${BIN_DIR}/${name}" -v))"
}

# ---------- panel ----------
install_panel() {
  local src="" tmp script_dir
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || echo .)"
  if [[ -n "$PANEL_BIN" ]]; then
    src="$PANEL_BIN"
  elif [[ -f "$script_dir/dist/frp-panel-linux-${PANEL_ARCH}" ]]; then
    src="$script_dir/dist/frp-panel-linux-${PANEL_ARCH}"
  fi
  if [[ -n "$src" ]]; then
    [[ -f "$src" ]] || die "找不到 $src"
    info "使用本地面板程序 $src"
    install -m 0755 "$src" "${BIN_DIR}/frp-panel"
  else
    info "下载 FRP Panel (${PANEL_ARCH})"
    tmp="$WORK/panel"; mkdir -p "$tmp"
    local base="https://github.com/${PANEL_REPO}/releases/latest/download"
    download "${base}/sha256sums.txt" "$tmp/sums.txt"
    download "${base}/frp-panel-linux-${PANEL_ARCH}" "$tmp/frp-panel"
    local want got
    want=$(awk -v f="frp-panel-linux-${PANEL_ARCH}" '$2==f {print $1}' "$tmp/sums.txt")
    got=$(sha256sum "$tmp/frp-panel" | awk '{print $1}')
    [[ -n "$want" && "$want" == "$got" ]] || die "面板程序 SHA-256 校验失败"
    install -m 0755 "$tmp/frp-panel" "${BIN_DIR}/frp-panel"
  fi
  ok "已安装 ${BIN_DIR}/frp-panel v$("${BIN_DIR}/frp-panel" version)"
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

write_units() { # frps|frpc
  local name="$1" desc
  [[ "$name" == frps ]] && desc="FRP Server" || desc="FRP Client"
  cat > "/etc/systemd/system/${name}.service" <<EOF
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
  cat > /etc/systemd/system/frp-panel.service <<EOF
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
  systemctl daemon-reload
}

open_ports() { # "7000/tcp 7600/tcp ..."
  (( FIREWALL )) || return 0
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

quote() { local q="'\\''"; printf "'%s'" "${1//\'/$q}"; }

do_install() { # server|client
  local role="$1" name out
  [[ "$role" == server ]] && name=frps || name=frpc
  require_root; check_os; install_deps; detect_arch
  if [[ "$role" == client ]]; then
    [[ -n "$SERVER_ADDR" && -n "$TOKEN" ]] || die "客户端安装需要 --server 和 --token（可在服务端面板「概览」页复制完整命令）"
  else
    detect_public_ip
  fi

  install_frp "$name"
  install_panel
  ensure_user

  local args=(init --role "$role" --data "$DATA_DIR" --frp-dir "$FRP_DIR" --bin-dir "$BIN_DIR"
              --listen ":${PANEL_PORT}" --proc systemd
              --script-url "https://raw.githubusercontent.com/${PANEL_REPO}/main/install.sh")
  [[ -n "$MIRROR" ]] && args+=(--mirror "$MIRROR")
  [[ -n "$PUBLIC_ADDR" ]] && args+=(--public-addr "$PUBLIC_ADDR")
  [[ -n "$TOKEN" ]] && args+=(--token "$TOKEN")
  if [[ "$role" == server ]]; then
    args+=(--bind-port "$BIND_PORT")
  else
    args+=(--server "$SERVER_ADDR" --port "$SERVER_PORT")
  fi
  if [[ -f "$DATA_DIR/panel.json" ]]; then
    warn "检测到已有安装，将重新初始化面板（保留现有 FRP 配置，重置管理员密码）"
    args+=(--force)
  fi
  out=$("${BIN_DIR}/frp-panel" "${args[@]}")
  get() { grep "^$1=" <<<"$out" | head -1 | cut -d= -f2-; }

  fix_perms "$(get FRP_CONFIG)"
  write_units "$name"
  systemctl enable --now "$name" frp-panel >/dev/null 2>&1
  systemctl restart "$name" frp-panel

  if [[ "$role" == server ]]; then
    open_ports "${BIND_PORT}/tcp" "${PANEL_PORT}/tcp"
  else
    open_ports "${PANEL_PORT}/tcp"
  fi

  sleep 2
  systemctl is-active --quiet "$name" && ok "$name 运行中" || warn "$name 未能启动，查看日志: journalctl -u $name -n 50"
  systemctl is-active --quiet frp-panel && ok "frp-panel 运行中" || warn "frp-panel 未能启动，查看日志: journalctl -u frp-panel -n 50"

  local host="${PUBLIC_ADDR:-$(hostname -I 2>/dev/null | awk '{print $1}')}"
  echo
  echo "${c_b}════════════════════════ 安装完成 ════════════════════════${c_0}"
  echo "  面板地址:   ${c_grn}https://${host}:${PANEL_PORT}${c_0}"
  echo "  用户名:     admin"
  echo "  初始密码:   ${c_ylw}$(get ADMIN_PASSWORD)${c_0}   （首次登录后必须修改）"
  echo
  echo "  面板使用自签名证书，浏览器会提示“不安全”。请核对证书指纹一致后再继续："
  echo "  SHA-256:    $(get CERT_FINGERPRINT)"
  if [[ "$role" == server ]]; then
    local cmd
    cmd="curl -fsSL $(quote "${MIRROR}https://raw.githubusercontent.com/${PANEL_REPO}/main/install.sh") | sudo bash -s -- client --server $(quote "${PUBLIC_ADDR:-<服务器IP>}") --port $(get BIND_PORT) --token $(quote "$(get TOKEN)")"
    [[ -n "$MIRROR" ]] && cmd+=" --mirror $(quote "$MIRROR")"
    echo
    echo "  在内网机器上运行以下命令即可接入（令牌请妥善保管）："
    echo "  ${c_cyn}${cmd}${c_0}"
    echo
    echo "  ${c_ylw}别忘了在云服务商安全组放行: ${BIND_PORT}/tcp（frps）、${PANEL_PORT}/tcp（面板），以及代理用到的端口${c_0}"
  fi
  echo "${c_b}═══════════════════════════════════════════════════════════${c_0}"
}

do_upgrade() {
  require_root; install_deps; detect_arch
  [[ -f "$DATA_DIR/panel.json" ]] || die "未检测到安装"
  local name
  name=$(grep -q '"role": "server"' "$DATA_DIR/panel.json" && echo frps || echo frpc)
  install_frp "$name"
  install_panel
  systemctl restart "$name" frp-panel
  ok "升级完成"
}

do_status() {
  for u in frps frpc frp-panel; do
    systemctl list-unit-files "$u.service" >/dev/null 2>&1 || continue
    [[ -f "/etc/systemd/system/$u.service" ]] || continue
    printf "%-10s %s\n" "$u" "$(systemctl is-active "$u")"
  done
  [[ -x "$BIN_DIR/frp-panel" ]] && "$BIN_DIR/frp-panel" info --data "$DATA_DIR"
}

do_reset_password() {
  require_root
  "$BIN_DIR/frp-panel" reset-password --data "$DATA_DIR"
  systemctl restart frp-panel
}

do_uninstall() {
  require_root
  for u in frp-panel frps frpc; do
    if [[ -f "/etc/systemd/system/$u.service" ]]; then
      systemctl disable --now "$u" >/dev/null 2>&1 || true
      rm -f "/etc/systemd/system/$u.service"
    fi
  done
  systemctl daemon-reload
  rm -f "$BIN_DIR/frp-panel" "$BIN_DIR/frps" "$BIN_DIR/frpc"
  if (( PURGE )); then
    rm -rf "$DATA_DIR" "$FRP_DIR"
    userdel "$FRP_USER" >/dev/null 2>&1 || true
    ok "已卸载并删除配置"
  else
    ok "已卸载（配置保留在 $FRP_DIR 和 $DATA_DIR，使用 --purge 一并删除）"
  fi
}

main() {
  parse_args "$@"
  WORK=$(mktemp -d)
  trap 'rm -rf "$WORK"' EXIT
  case "$ACTION" in
    server|client)  do_install "$ACTION" ;;
    upgrade)        do_upgrade ;;
    status)         do_status ;;
    reset-password) do_reset_password ;;
    uninstall)      do_uninstall ;;
    -h|--help|help) usage ;;
    *) usage; exit 1 ;;
  esac
}

main "$@"
