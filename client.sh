#!/usr/bin/env bash
# frp-client：FRP Panel 客户端本机管理脚本（Ubuntu / Debian，需要 systemd）
#
# 一台客户端可以同时接入多台服务端，每台服务端在本机是一个独立的“实例”：
#   配置与身份  /etc/frp-panel/servers/<名称>/agent.json
#   frpc 配置   /etc/frp/<名称>/frpc.toml
#   程序        /usr/local/lib/frp-panel/<名称>/{frp-panel,frpc}
#   服务        frpc@<名称>、frp-panel-agent@<名称>
#
# 所有管理操作都在本机完成，服务端失联时也能使用：
#   sudo frp-client                     交互菜单
#   sudo frp-client list                列出已接入的服务端及运行状态
#   sudo frp-client remove <名称>        删除一台服务端（清除配置、停止并卸载其服务）
#   sudo frp-client uninstall           全部卸载
# 本机没有 frp-client 时（例如被误删）：
#   curl -fsSL https://raw.githubusercontent.com/wangmancang520/frp-panel/main/client.sh | sudo bash -s -- uninstall
#
# 测试用：路径都可以用 FRP_PANEL_*_DIR 等环境变量改到别处（见下方）。
FRP_CLIENT_SCRIPT_VERSION=1
set -euo pipefail

DATA_DIR="${FRP_PANEL_DATA_DIR:-/etc/frp-panel}"
FRP_DIR="${FRP_PANEL_FRP_DIR:-/etc/frp}"
LIB_DIR="${FRP_PANEL_LIB_DIR:-/usr/local/lib/frp-panel}"
BIN_DIR="${FRP_PANEL_BIN_DIR:-/usr/local/bin}"
SYSTEMD_DIR="${FRP_PANEL_SYSTEMD_DIR:-/etc/systemd/system}"
SELF_PATH="${FRP_CLIENT_PATH:-/usr/local/sbin/frp-client}"
CTL_PATH="${FRP_PANEL_CTL_PATH:-/usr/local/sbin/frp-panel-ctl}"
SERVERS="$DATA_DIR/servers"
FRP_USER=frp
LEGACY=legacy   # pseudo instance: the single-server layout of v0.2.x

ACTION=""
TARGET=""
PANEL_URL=""
PIN=""
ENROLL_KEY=""
NAME=""
BINARY=""
FORCE=0
YES=0
ALL=0
FOLLOW=0
OFFLINE=0
KEEP_ON_SERVER=0
WHICH=frpc
INTERACTIVE=0
LIST=()

c_red=$'\e[31m'; c_grn=$'\e[32m'; c_ylw=$'\e[33m'; c_cyn=$'\e[36m'; c_dim=$'\e[2m'; c_b=$'\e[1m'; c_0=$'\e[0m'
info() { echo "${c_cyn}==>${c_0} $*"; }
ok()   { echo "${c_grn}✔${c_0} $*"; }
warn() { echo "${c_ylw}!${c_0} $*" >&2; }
die()  { echo "${c_red}✘ $*${c_0}" >&2; exit 1; }

usage() {
  cat <<EOF
frp-client：FRP Panel 客户端本机管理（服务端失联时也能使用）

用法: frp-client [命令] [参数]        不带命令时打开交互菜单

命令:
  list                    列出已接入的服务端、服务状态与面板连通性（--offline 跳过连通性检查）
  show <名称>             查看一台服务端的详情与本机代理列表
  logs <名称>             查看 frpc 日志（--agent 看 agent 日志，-f 持续跟踪）
  start|stop|restart <名称>|--all
                          启动 / 停止 / 重启 frpc 和 agent
  update <名称>|--all     立即从该服务端面板重新下载 frp-panel 与 frpc（需要能连上面板）
  remove <名称>           删除一台服务端：先尝试在服务端注销本机，再停止服务并清除本机配置
                          （--keep-on-server 不在服务端注销；连不上服务端时只清理本机）
  uninstall               删除全部服务端并卸载客户端程序与本脚本
  join                    接入一台服务端（通常由面板的接入命令调用；重复执行会沿用原有身份）
                            --panel URL --pin PIN --key KEY [--name 名称] [--force]
  migrate                 把 v0.2 的单服务端布局迁移为实例布局
  version                 显示脚本版本

名称就是 list 中显示的实例名；旧版单服务端布局显示为 "${LEGACY}"。
其他选项: -y/--yes 不再确认
EOF
}

parse_args() {
  if [[ $# -gt 0 && "$1" != -* ]]; then ACTION="$1"; shift; fi
  if [[ $# -gt 0 && "$1" != -* ]]; then TARGET="$1"; shift; fi
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --panel)          PANEL_URL="$2"; shift 2 ;;
      --pin)            PIN="$2"; shift 2 ;;
      --key)            ENROLL_KEY="$2"; shift 2 ;;
      --name)           NAME="$2"; shift 2 ;;
      --binary)         BINARY="$2"; shift 2 ;;
      --force)          FORCE=1; shift ;;
      --all)            ALL=1; shift ;;
      --agent)          WHICH=agent; shift ;;
      --follow|-f)      FOLLOW=1; shift ;;
      --offline)        OFFLINE=1; shift ;;
      --keep-on-server) KEEP_ON_SERVER=1; shift ;;
      -y|--yes)         YES=1; shift ;;
      -h|--help)        usage; exit 0 ;;
      *) die "未知参数: $1（使用 --help 查看帮助）" ;;
    esac
  done
}

# ---------- interaction ----------
have_tty() { [[ -r /dev/tty && -w /dev/tty ]] && { : </dev/tty; } 2>/dev/null; }

input_closed() {
  echo >/dev/tty 2>/dev/null || true
  echo "${c_red}✘ 输入已结束，退出${c_0}" >&2
  kill -TERM "$$" 2>/dev/null
  exit 1
}

ask() {
  local p="$1" d="${2:-}" v=""
  [[ -n "$d" ]] && p+=" ${c_dim}[${d}]${c_0}"
  read -r -p "$p: " v </dev/tty || input_closed
  echo "${v:-$d}"
}

# confirm PROMPT [DEFAULT y|n]: asks when interactive (or when a tty exists for
# destructive actions); -y skips it.
confirm() {
  (( YES )) && return 0
  have_tty || return 0
  local v
  v=$(ask "$1 (y/n)" "${2:-n}") || exit 1
  [[ "$v" =~ ^[Yy] ]]
}

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
  [[ $EUID -eq 0 || -n "${FRP_PANEL_ALLOW_NONROOT:-}" ]] || die "请使用 root 运行（sudo frp-client ...）"
}

need_systemd() { command -v systemctl >/dev/null || die "需要 systemd"; }

need_curl() {
  command -v curl >/dev/null && return 0
  command -v apt-get >/dev/null || die "请先安装 curl"
  info "安装 curl"
  DEBIAN_FRONTEND=noninteractive apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq curl ca-certificates >/dev/null
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)  FRP_ARCH=amd64 ;;
    aarch64|arm64) FRP_ARCH=arm64 ;;
    armv7*|armhf)  FRP_ARCH=arm_hf ;;
    *) die "不支持的 CPU 架构: $(uname -m)" ;;
  esac
}

# A server panel (role server) on this machine, which must be left alone.
server_present() {
  [[ -f "$DATA_DIR/panel.json" ]] && ! grep -q '"role": "client"' "$DATA_DIR/panel.json"
}

# v0.1 standalone client panel.
old_client_panel_present() {
  [[ -f "$DATA_DIR/panel.json" ]] && grep -q '"role": "client"' "$DATA_DIR/panel.json"
}

legacy_present() { [[ -f "$DATA_DIR/agent.json" ]]; }

# ---------- instances ----------
instances() {
  local f
  legacy_present && echo "$LEGACY"
  for f in "$SERVERS"/*/agent.json; do
    [[ -f "$f" ]] && basename "$(dirname "$f")"
  done
  return 0
}

exists() { instances | grep -qx -- "$1"; }

# load_list: fills LIST with the instance names (portable to bash 3.2, where
# mapfile is missing and an empty "${LIST[@]}" trips set -u).
load_list() {
  local l
  LIST=()
  while IFS= read -r l; do [[ -n "$l" ]] && LIST+=("$l"); done < <(instances)
  return 0
}

p_data()  { if [[ "$1" == "$LEGACY" ]]; then echo "$DATA_DIR"; else echo "$SERVERS/$1"; fi; }
p_frp()   { if [[ "$1" == "$LEGACY" ]]; then echo "$FRP_DIR"; else echo "$FRP_DIR/$1"; fi; }
p_bin()   { if [[ "$1" == "$LEGACY" ]]; then echo "$BIN_DIR"; else echo "$LIB_DIR/$1"; fi; }
u_frpc()  { if [[ "$1" == "$LEGACY" ]]; then echo frpc; else echo "frpc@$1"; fi; }
u_agent() { if [[ "$1" == "$LEGACY" ]]; then echo frp-panel-agent; else echo "frp-panel-agent@$1"; fi; }

# conf NAME KEY: a string field from the instance's agent.json.
conf() {
  sed -n "s/.*\"$2\": *\"\([^\"]*\)\".*/\1/p" "$(p_data "$1")/agent.json" 2>/dev/null | head -1 || true
}
conf_num() {
  sed -n "s/.*\"$2\": *\([0-9][0-9]*\).*/\1/p" "$(p_data "$1")/agent.json" 2>/dev/null | head -1 || true
}

ver_of() { # binary [args]
  local v
  v=$("$@" 2>/dev/null) && echo "$v" || echo "-"
}

state() { systemctl is-active "$1" 2>/dev/null || true; }

valid_name() { [[ "$1" =~ ^[a-z0-9][a-z0-9-]{0,31}$ && "$1" != "$LEGACY" ]]; }

# derive_name URL: a readable instance name from the panel address,
# e.g. https://1.2.3.4:7600 -> 1-2-3-4, https://frp.example.com:8443 -> frp-example-com-8443.
derive_name() {
  local hp="${1#https://}" host port n
  hp="${hp%%/*}"
  if [[ "$hp" =~ ^\[(.*)\]:([0-9]+)$ ]] || [[ "$hp" =~ ^([^:]*):([0-9]+)$ ]]; then
    host="${BASH_REMATCH[1]}"; port="${BASH_REMATCH[2]}"
  else
    host="$hp"; port=""
  fi
  n=$(echo "$host" | tr 'A-Z' 'a-z' | sed -E 's/[^a-z0-9]+/-/g; s/^-+//; s/-+$//')
  [[ -n "$port" && "$port" != 7600 ]] && n="${n}-${port}"
  n="${n:0:32}"; n="${n%-}"
  [[ -n "$n" ]] || n=server
  [[ "$n" == "$LEGACY" ]] && n=server
  echo "$n"
}

unique_name() {
  local base="$1" n="$1" i=2
  while exists "$n" || [[ -e "$SERVERS/$n" ]]; do
    n="${base:0:29}-$i"; i=$((i + 1))
  done
  echo "$n"
}

# find_instance URL PIN: the instance already joined to this panel, if any.
find_instance() {
  local n
  for n in $(instances); do
    if [[ "$(conf "$n" panel)" == "${1%/}" && "$(conf "$n" pin)" == "$2" ]]; then echo "$n"; return; fi
  done
}

# resolve_target: checks TARGET (or asks for it when interactive).
resolve_target() {
  load_list
  (( ${#LIST[@]} )) || die "本机没有接入任何服务端"
  if [[ -z "$TARGET" ]]; then
    if (( INTERACTIVE )); then
      TARGET="${LIST[$(( $(choose "选择服务端实例" "${LIST[@]}") - 1 ))]}"
    elif (( ${#LIST[@]} == 1 )); then
      TARGET="${LIST[0]}"
    else
      die "请指定实例名称：${LIST[*]}"
    fi
  fi
  exists "$TARGET" || die "没有名为 $TARGET 的实例（现有：${LIST[*]}）"
}

# panel_reachable NAME: whether the instance's panel answers with the pinned key.
panel_reachable() {
  local url pin code
  url=$(conf "$1" panel); pin=$(conf "$1" pin)
  [[ -n "$url" && -n "$pin" ]] || return 1
  code=$(curl -sk --pinnedpubkey "$pin" --max-time 4 -o /dev/null -w '%{http_code}' "${url%/}/" 2>/dev/null || true)
  [[ "$code" == 200 ]]
}

# proxies NAME: "name<TAB>type<TAB>target" for each proxy in frpc.toml.
proxies() {
  local f
  f="$(p_frp "$1")/frpc.toml"
  [[ -f "$f" ]] || return 0
  awk '
    function unq(v) { gsub(/^[ \t]+|[ \t]+$/, "", v); gsub(/^['\''"]|['\''"]$/, "", v); return v }
    function flush() { if (inp && name != "") printf "%s\t%s\t%s\n", name, type, (rp != "" ? ":" rp : dom) }
    /^\[\[proxies\]\]/ { flush(); inp = 1; name = type = rp = dom = ""; next }
    /^\[proxies\./ { next }
    /^\[/ { flush(); inp = 0; next }
    inp && /^name[ \t]*=/ { sub(/^[^=]*=/, ""); name = unq($0) }
    inp && /^type[ \t]*=/ { sub(/^[^=]*=/, ""); type = unq($0) }
    inp && /^remotePort[ \t]*=/ { sub(/^[^=]*=/, ""); rp = unq($0) }
    inp && /^customDomains[ \t]*=/ { sub(/^[^=]*=/, ""); gsub(/[\[\]'\''" ]/, ""); dom = $0 }
    inp && /^subdomain[ \t]*=/ { sub(/^[^=]*=/, ""); if (dom == "") dom = unq($0) ".*" }
    END { flush() }
  ' "$f"
}

# ---------- layout ----------
ensure_user() {
  id -u "$FRP_USER" >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin "$FRP_USER"
}

fix_perms() { # NAME
  local fd
  fd=$(p_frp "$1")
  chmod 0700 "$DATA_DIR" "$(p_data "$1")"
  [[ "$1" == "$LEGACY" ]] || chmod 0700 "$SERVERS"
  chown root:"$FRP_USER" "$FRP_DIR" "$fd"
  chmod 0750 "$FRP_DIR" "$fd"
  if [[ -f "$fd/frpc.toml" ]]; then chown root:"$FRP_USER" "$fd/frpc.toml"; chmod 0640 "$fd/frpc.toml"; fi
}

write_units() {
  mkdir -p "$SYSTEMD_DIR"
  cat > "${SYSTEMD_DIR}/frpc@.service" <<EOF
[Unit]
Description=FRP Client for server %i (managed by FRP Panel)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${FRP_USER}
Group=${FRP_USER}
ExecStart=${LIB_DIR}/%i/frpc -c ${FRP_DIR}/%i/frpc.toml
Restart=always
RestartSec=5s
LimitNOFILE=1048576
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
  # The agent runs as root: it rewrites frpc.toml, controls frpc via
  # systemctl and replaces this instance's binaries when the server upgrades.
  cat > "${SYSTEMD_DIR}/frp-panel-agent@.service" <<EOF
[Unit]
Description=FRP Panel agent for server %i (frpc managed by the server panel)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${LIB_DIR}/%i/frp-panel agent run --data ${SERVERS}/%i --frp-dir ${FRP_DIR}/%i --bin-dir ${LIB_DIR}/%i --unit frpc@%i --client-script-path ${SELF_PATH}
Restart=always
RestartSec=5s

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
}

wait_active() {
  local u i bad=0
  for u in "$@"; do
    for i in $(seq 1 20); do systemctl is-active --quiet "$u" && break; sleep 0.5; done
    if systemctl is-active --quiet "$u"; then ok "$u 运行中"; else warn "$u 未能启动，查看日志: journalctl -u $u -n 50"; bad=1; fi
  done
  return $bad
}

# script_version FILE: FRP_CLIENT_SCRIPT_VERSION of a frp-client script (empty if missing).
script_version() { [[ -f "$1" ]] || return 0; sed -n 's/^FRP_CLIENT_SCRIPT_VERSION=\([0-9][0-9]*\).*/\1/p' "$1" | head -1; }

# install_self: keeps the newest frp-client at $SELF_PATH.
install_self() {
  local src="${BASH_SOURCE[0]:-}" mine have
  [[ -f "$src" && "$src" != "$SELF_PATH" ]] || return 0
  mine=$(script_version "$src"); have=$(script_version "$SELF_PATH")
  if [[ ! -f "$SELF_PATH" ]] || (( ${mine:-0} > ${have:-0} )); then
    mkdir -p "$(dirname "$SELF_PATH")"
    install -m 0755 "$src" "$SELF_PATH"
  fi
}

# supports_instances BINARY: frp-panel builds with frp-client support (the
# agent understands --unit). Older agents keep the single-server layout.
supports_instances() { "$1" client-script >/dev/null 2>&1; }

# ---------- migrate ----------
# migrate_legacy: turns the v0.2 single-server layout into an instance, when
# its frp-panel binary is new enough to run as one.
migrate_legacy() {
  legacy_present || return 0
  if ! supports_instances "$BIN_DIR/frp-panel"; then
    warn "本机还有一台旧版布局的服务端（${LEGACY}，$(conf "$LEGACY" panel)），其程序版本较旧，暂不迁移；它会继续正常运行。该服务端升级后运行 frp-client migrate 即可迁移"
    return 0
  fi
  local url name
  url=$(conf "$LEGACY" panel)
  name=$(unique_name "$(derive_name "$url")")
  info "迁移旧版布局（${url}）为实例 ${name}"
  systemctl disable --now frp-panel-agent frpc >/dev/null 2>&1 || true
  mkdir -p "$SERVERS/$name" "$FRP_DIR/$name" "$LIB_DIR/$name"
  mv "$DATA_DIR/agent.json" "$SERVERS/$name/agent.json"
  [[ -f "$DATA_DIR/applied" ]] && mv "$DATA_DIR/applied" "$SERVERS/$name/applied"
  [[ -f "$FRP_DIR/frpc.toml" ]] && mv "$FRP_DIR/frpc.toml" "$FRP_DIR/$name/frpc.toml"
  [[ -f "$BIN_DIR/frp-panel" ]] && mv "$BIN_DIR/frp-panel" "$LIB_DIR/$name/frp-panel"
  [[ -f "$BIN_DIR/frpc" ]] && mv "$BIN_DIR/frpc" "$LIB_DIR/$name/frpc"
  rm -f "${SYSTEMD_DIR}/frp-panel-agent.service" "${SYSTEMD_DIR}/frpc.service"
  # frp-panel-ctl managed the old layout; clients now use frp-client.
  server_present || rm -f "$CTL_PATH"
  ensure_user
  fix_perms "$name"
  write_units
  systemctl enable --now "frpc@$name" "frp-panel-agent@$name" >/dev/null 2>&1 || true
  wait_active "frpc@$name" "frp-panel-agent@$name" || true
  ok "已迁移为实例 ${name}（客户端身份不变，服务端无需任何操作）"
}

cmd_migrate() {
  require_root; need_systemd
  legacy_present || { ok "没有需要迁移的旧版布局"; return 0; }
  migrate_legacy
}

# ---------- join ----------
parse_join() { # pasted join command
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

cmd_join() {
  require_root; need_systemd; need_curl; detect_arch
  if [[ -z "$PANEL_URL" || -z "$PIN" || -z "$ENROLL_KEY" ]] && (( INTERACTIVE )); then
    echo "请在服务端面板「客户端」页复制接入命令，粘贴到这里：" >/dev/tty
    parse_join "$(ask "接入命令")"
  fi
  [[ -n "$PANEL_URL" && -n "$PIN" && -n "$ENROLL_KEY" ]] \
    || die "缺少 --panel / --pin / --key，请在服务端面板「客户端」页复制完整的接入命令"
  [[ "$PANEL_URL" == https://* ]] || die "--panel 必须是 https:// 地址"
  [[ "$PIN" == sha256//* ]] || die "--pin 格式应为 sha256//<base64>"
  PANEL_URL="${PANEL_URL%/}"
  server_present && die "本机已安装服务端面板，不能同时作为受管客户端"

  if old_client_panel_present; then
    warn "检测到旧版客户端面板（v0.1），将停用它并改为由服务端管理（原有代理需要在服务端面板重新添加）"
    confirm "继续？" y || die "已取消"
    systemctl disable --now frp-panel frpc >/dev/null 2>&1 || true
    rm -f "${SYSTEMD_DIR}/frp-panel.service" "${SYSTEMD_DIR}/frpc.service" "$BIN_DIR/frp-panel" "$BIN_DIR/frpc"
    mv "$DATA_DIR/panel.json" "$DATA_DIR/panel.json.old-client"
  fi
  migrate_legacy

  local name rejoin=0 tmp bin out
  name=$(find_instance "$PANEL_URL" "$PIN")
  if [[ "$name" == "$LEGACY" ]]; then
    die "本机已以旧版布局接入该服务端（${LEGACY}），且暂时无法迁移。请先升级该服务端，或运行 frp-client remove ${LEGACY} 后再接入"
  elif [[ -n "$name" ]]; then
    rejoin=1
    [[ -z "$NAME" || "$NAME" == "$name" ]] || warn "该服务端已接入为实例 ${name}，忽略 --name ${NAME}"
    info "该服务端已接入为实例 ${name}，将沿用原有身份重新接入$( (( FORCE )) && echo '（--force：重新注册为新的客户端）')"
  elif [[ -n "$NAME" ]]; then
    valid_name "$NAME" || die "实例名称只能包含小写字母、数字和 -（最长 32 个字符），且不能是 ${LEGACY}"
    exists "$NAME" && die "实例 ${NAME} 已存在（连接的是另一台服务端），请换一个名称"
    name="$NAME"
  else
    name=$(unique_name "$(derive_name "$PANEL_URL")")
  fi

  tmp=$(mktemp -d)
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT
  if [[ -n "$BINARY" ]]; then
    bin="$BINARY"
  else
    info "从面板下载 frp-panel (${FRP_ARCH})"
    curl -fsSLk --pinnedpubkey "$PIN" --retry 3 --connect-timeout 15 -H "X-Enroll-Key: ${ENROLL_KEY}" \
      "${PANEL_URL}/agent/v1/download/frp-panel?arch=${FRP_ARCH}" -o "$tmp/frp-panel" \
      || die "从面板下载失败（请确认面板可达，且安全组已放行面板端口）"
    bin="$tmp/frp-panel"
  fi
  chmod 0755 "$bin"
  "$bin" version >/dev/null 2>&1 || die "frp-panel 无法运行"
  supports_instances "$bin" || die "该服务端的面板版本（v$("$bin" version)）过旧，不支持多服务端接入，请先升级服务端"

  mkdir -p "$SERVERS/$name" "$FRP_DIR/$name" "$LIB_DIR/$name"
  (( rejoin )) && systemctl stop "frp-panel-agent@$name" >/dev/null 2>&1 || true
  install -m 0755 "$bin" "$LIB_DIR/$name/frp-panel"
  ensure_user
  chmod 0700 "$DATA_DIR" "$SERVERS" "$SERVERS/$name"

  info "注册到面板并下载 frpc"
  local args=(agent enroll --panel "$PANEL_URL" --pin "$PIN" --key "$ENROLL_KEY"
              --data "$SERVERS/$name" --frp-dir "$FRP_DIR/$name" --bin-dir "$LIB_DIR/$name")
  (( FORCE )) && args+=(--force)
  if ! out=$("$LIB_DIR/$name/frp-panel" "${args[@]}"); then
    (( rejoin )) || rm -rf "${SERVERS:?}/$name" "${FRP_DIR:?}/$name" "${LIB_DIR:?}/$name"
    die "注册失败"
  fi
  get() { grep "^$1=" <<<"$out" | head -1 | cut -d= -f2-; }

  fix_perms "$name"
  write_units
  systemctl enable "frpc@$name" "frp-panel-agent@$name" >/dev/null 2>&1
  systemctl restart "frpc@$name" "frp-panel-agent@$name"
  wait_active "frpc@$name" "frp-panel-agent@$name" || true
  install_self

  local n
  n=$(instances | wc -l | tr -d " ")
  echo
  echo "${c_b}════════════════════════ 接入完成 ════════════════════════${c_0}"
  echo "  实例名称:   ${c_grn}${name}${c_0}（本机共接入 ${n} 台服务端）"
  echo "  客户端:     $(get CLIENT_NAME)（ID $(get CLIENT_ID)）"
  echo "  服务端面板: ${PANEL_URL}"
  echo "  本机无需任何配置，也不开放任何端口。请回到服务端面板「客户端」页为它添加代理。"
  echo "  本机管理（服务端失联时也可用）: ${c_b}sudo frp-client${c_0}"
  echo "${c_b}═══════════════════════════════════════════════════════════${c_0}"
}

# ---------- list / show / logs ----------
cmd_list() {
  local n url fa
  load_list
  if (( ${#LIST[@]} == 0 )); then
    echo "本机没有接入任何服务端。"
    old_client_panel_present && echo "（检测到旧版客户端面板，可用服务端面板的接入命令迁移，或 frp-client uninstall 卸载）"
    return 0
  fi
  echo "${c_b}已接入 ${#LIST[@]} 台服务端${c_0}"
  for n in "${LIST[@]}"; do
    url=$(conf "$n" panel)
    fa=$(state "$(u_agent "$n")"); local fc; fc=$(state "$(u_frpc "$n")")
    echo
    echo "  ${c_b}${n}${c_0}  ${url}"
    printf "    客户端 ID %-12s  frpc %-9s agent %-9s  代理 %s 个\n" \
      "$(conf "$n" id)" "$fc" "$fa" "$(proxies "$n" | wc -l | tr -d ' ')"
    if (( ! OFFLINE )); then
      if panel_reachable "$n"; then echo "    面板 ${c_grn}可连接${c_0}"; else echo "    面板 ${c_ylw}无法连接${c_0}（本机服务照常运行；如服务端已不再使用，可 frp-client remove ${n}）"; fi
    fi
  done
  legacy_present && { echo; echo "  ${c_dim}${LEGACY} 为 v0.2 的单服务端布局，可运行 frp-client migrate 迁移${c_0}"; }
  return 0
}

cmd_show() {
  resolve_target
  local n="$TARGET" b
  b=$(p_bin "$n")
  echo "${c_b}实例:${c_0}        $n"
  echo "${c_b}服务端面板:${c_0}  $(conf "$n" panel)"
  echo "${c_b}客户端 ID:${c_0}   $(conf "$n" id)"
  echo "${c_b}版本:${c_0}        frp-panel v$(ver_of "$b/frp-panel" version)   frpc $(ver_of "$b/frpc" -v)"
  local ap
  ap=$(conf_num "$n" adminPort)
  echo "${c_b}管理端口:${c_0}    127.0.0.1:${ap:-7400}（frpc 本地管理接口）"
  echo "${c_b}服务:${c_0}        $(u_frpc "$n") $(state "$(u_frpc "$n")")   $(u_agent "$n") $(state "$(u_agent "$n")")"
  echo "${c_b}文件:${c_0}        $(p_data "$n")/agent.json   $(p_frp "$n")/frpc.toml   $b/"
  if (( ! OFFLINE )); then
    if panel_reachable "$n"; then echo "${c_b}面板:${c_0}        ${c_grn}可连接${c_0}"; else echo "${c_b}面板:${c_0}        ${c_ylw}无法连接${c_0}"; fi
  fi
  echo
  echo "${c_b}代理（本机当前配置）:${c_0}"
  local rows
  rows=$(proxies "$n")
  if [[ -z "$rows" ]]; then echo "  （无）"; else echo "$rows" | awk -F'\t' '{printf "  %-24s %-6s %s\n", $1, $2, $3}'; fi
  echo
  echo "${c_b}agent 最近日志:${c_0}"
  journalctl -u "$(u_agent "$n")" -n 6 --no-pager -o cat 2>/dev/null | sed 's/^/  /' || true
}

cmd_logs() {
  resolve_target
  local u
  if (( INTERACTIVE )); then
    [[ "$(choose "查看哪个服务的日志" "frpc" "agent")" == 2 ]] && WHICH=agent
    [[ "$(choose "方式" "最近 100 行" "持续跟踪（Ctrl+C 返回）")" == 2 ]] && FOLLOW=1
  fi
  if [[ "$WHICH" == agent ]]; then u=$(u_agent "$TARGET"); else u=$(u_frpc "$TARGET"); fi
  if (( FOLLOW )); then
    trap 'true' INT
    journalctl -u "$u" -n 30 -f --no-pager -o cat || true
    trap - INT
  else
    journalctl -u "$u" -n 100 --no-pager -o cat || true
  fi
}

# ---------- service control ----------
cmd_service() { # start|stop|restart
  require_root; need_systemd
  local n
  if (( ALL )); then load_list; else resolve_target; LIST=("$TARGET"); fi
  (( ${#LIST[@]} )) || die "本机没有接入任何服务端"
  for n in "${LIST[@]}"; do
    systemctl "$1" "$(u_frpc "$n")" "$(u_agent "$n")"
    if [[ "$1" == stop ]]; then ok "$n 已停止"; else wait_active "$(u_frpc "$n")" "$(u_agent "$n")" || true; fi
  done
}

cmd_update() {
  require_root; need_systemd; need_curl; detect_arch
  local n url pin tok b tmp
  if (( ALL )); then load_list; else resolve_target; LIST=("$TARGET"); fi
  (( ${#LIST[@]} )) || die "本机没有接入任何服务端"
  tmp=$(mktemp -d)
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT
  for n in "${LIST[@]}"; do
    url=$(conf "$n" panel); pin=$(conf "$n" pin); tok=$(conf "$n" token); b=$(p_bin "$n")
    info "从 ${url} 更新实例 ${n}"
    if ! curl -fsSLk --pinnedpubkey "$pin" --connect-timeout 15 -H "Authorization: Bearer ${tok}" \
           "${url%/}/agent/v1/download/frp-panel?arch=${FRP_ARCH}" -o "$tmp/frp-panel" \
       || ! curl -fsSLk --pinnedpubkey "$pin" --connect-timeout 15 -H "Authorization: Bearer ${tok}" \
           "${url%/}/agent/v1/download/frpc?arch=${FRP_ARCH}" -o "$tmp/frpc"; then
      warn "无法从面板下载，跳过 ${n}（面板失联时本机程序保持不变）"
      continue
    fi
    chmod 0755 "$tmp/frp-panel" "$tmp/frpc"
    "$tmp/frp-panel" version >/dev/null && "$tmp/frpc" -v >/dev/null || { warn "下载的程序无法运行，跳过 ${n}"; continue; }
    echo "  frp-panel: v$(ver_of "$b/frp-panel" version) → v$("$tmp/frp-panel" version)"
    echo "  frpc:      $(ver_of "$b/frpc" -v) → $("$tmp/frpc" -v)"
    if [[ "$n" == "$LEGACY" ]] && supports_instances "$tmp/frp-panel"; then
      install -m 0755 "$tmp/frp-panel" "$b/frp-panel"; install -m 0755 "$tmp/frpc" "$b/frpc"
      migrate_legacy
      continue
    fi
    systemctl stop "$(u_agent "$n")" "$(u_frpc "$n")"
    install -m 0755 "$tmp/frp-panel" "$b/frp-panel"
    install -m 0755 "$tmp/frpc" "$b/frpc"
    systemctl start "$(u_frpc "$n")" "$(u_agent "$n")"
    wait_active "$(u_frpc "$n")" "$(u_agent "$n")" || true
  done
}

# ---------- remove / uninstall ----------
# leave NAME: unregisters from the server unless --keep-on-server; never fatal.
leave() {
  local n="$1" b
  (( KEEP_ON_SERVER )) && return 0
  b=$(p_bin "$n")/frp-panel
  if [[ -x "$b" ]] && timeout 20 "$b" agent leave --data "$(p_data "$n")" >/dev/null 2>&1; then
    ok "已在服务端 $(conf "$n" panel) 注销本机"
  else
    warn "无法在服务端注销（服务端失联或版本较旧），只清理本机。如服务端仍在使用，请在其面板「客户端」页手动删除本机"
  fi
}

remove_instance() { # NAME
  local n="$1"
  leave "$n"
  systemctl disable --now "$(u_agent "$n")" "$(u_frpc "$n")" >/dev/null 2>&1 || true
  if [[ "$n" == "$LEGACY" ]]; then
    rm -f "$DATA_DIR/agent.json" "$DATA_DIR/applied" "$FRP_DIR/frpc.toml" \
          "${SYSTEMD_DIR}/frp-panel-agent.service" "${SYSTEMD_DIR}/frpc.service"
    server_present || rm -f "$BIN_DIR/frp-panel" "$BIN_DIR/frpc"
    systemctl daemon-reload
  else
    rm -rf "${SERVERS:?}/$n" "${FRP_DIR:?}/$n" "${LIB_DIR:?}/$n"
  fi
  ok "已删除 ${n}"
}

# cleanup_common: removes what all instances share, once none is left.
cleanup_common() {
  [[ -z "$(instances)" ]] || return 0
  rm -f "${SYSTEMD_DIR}/frpc@.service" "${SYSTEMD_DIR}/frp-panel-agent@.service"
  systemctl daemon-reload 2>/dev/null || true
  rm -rf "$LIB_DIR" "$SERVERS"
  if ! server_present; then
    rmdir "$FRP_DIR" "$DATA_DIR" 2>/dev/null || true
    userdel "$FRP_USER" >/dev/null 2>&1 || true
    rm -f "$CTL_PATH"
  fi
  rm -f "$SELF_PATH"
  ok "客户端程序已全部卸载"
}

cmd_remove() {
  require_root; need_systemd
  resolve_target
  echo "  将删除实例 ${c_b}${TARGET}${c_0}（$(conf "$TARGET" panel)）：停止并卸载其 frpc 与 agent，清除本机配置$( (( KEEP_ON_SERVER )) || echo '，并在服务端注销本机')"
  confirm "确定删除？" n || die "已取消"
  remove_instance "$TARGET"
  if [[ -z "$(instances)" ]]; then
    confirm "本机已没有任何服务端，同时卸载客户端程序和 frp-client？" y && cleanup_common
  fi
  return 0
}

cmd_uninstall() {
  require_root; need_systemd
  local n
  load_list
  if (( ${#LIST[@]} )); then
    echo "  将删除全部 ${#LIST[@]} 台服务端：${LIST[*]}"
  fi
  confirm "确定卸载 FRP Panel 客户端并清除全部配置？" n || die "已取消"
  if (( ${#LIST[@]} )); then
    for n in "${LIST[@]}"; do remove_instance "$n"; done
  fi
  if old_client_panel_present; then
    systemctl disable --now frp-panel frpc >/dev/null 2>&1 || true
    rm -f "${SYSTEMD_DIR}/frp-panel.service" "${SYSTEMD_DIR}/frpc.service" "$BIN_DIR/frp-panel" "$BIN_DIR/frpc"
    rm -rf "${DATA_DIR:?}/panel.json" "${DATA_DIR:?}/tls" "$FRP_DIR/frpc.toml"
    systemctl daemon-reload
    ok "已卸载旧版客户端面板"
  fi
  rm -f "$DATA_DIR/panel.json.old-client"
  cleanup_common
}

# ---------- menu ----------
run_action() {
  set +e
  ( set -e; "$@" )
  local rc=$?
  set -e
  (( rc == 0 )) || warn "操作未完成"
  pause
}

menu() {
  have_tty || die "没有可交互的终端。请指定命令，例如: frp-client list（--help 查看全部）"
  INTERACTIVE=1
  local i
  while true; do
    echo >/dev/tty
    echo "${c_b}══════════════ FRP Panel 客户端 ══════════════${c_0}" >/dev/tty
    i=$(choose "请选择操作" "列出服务端与状态" "查看详情与代理" "查看日志" "重启" "停止" "启动" \
               "立即从面板更新程序" "接入新的服务端（粘贴接入命令）" "删除一台服务端" "全部卸载" "退出")
    TARGET=""; ALL=0
    case "$i" in
      1) run_action cmd_list ;;   2) run_action cmd_show ;;   3) run_action cmd_logs ;;
      4) run_action cmd_service restart ;; 5) run_action cmd_service stop ;; 6) run_action cmd_service start ;;
      7) run_action cmd_update ;;
      8) PANEL_URL=""; PIN=""; ENROLL_KEY=""; NAME=""; run_action cmd_join ;;
      9) run_action cmd_remove ;; 10) run_action cmd_uninstall; [[ -z "$(instances)" ]] && return ;;
      11) return ;;
    esac
  done
}

main() {
  parse_args "$@"
  case "$ACTION" in
    "")                 if have_tty; then menu; else cmd_list; fi ;;
    menu)               menu ;;
    list|status|ls)     cmd_list ;;
    show|info)          cmd_show ;;
    logs|log)           cmd_logs ;;
    start|stop|restart) cmd_service "$ACTION" ;;
    update|upgrade)     cmd_update ;;
    remove|rm|delete)   cmd_remove ;;
    uninstall)          cmd_uninstall ;;
    join|agent)         cmd_join ;;
    migrate)            cmd_migrate ;;
    version)            echo "frp-client 脚本版本 ${FRP_CLIENT_SCRIPT_VERSION}" ;;
    help|-h|--help)     usage ;;
    *) usage; exit 1 ;;
  esac
}

main "$@"
