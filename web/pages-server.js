// Server (frps) pages.
import {
  html, useState, useEffect, useMemo, api, usePoll, useAction, icon,
  Field, TextInput, NumInput, SecretInput, Check, Seg, Alert, CodeBox, ServiceBadge, Stat, Spinner, Empty,
  fmtBytes, fmtRate, since, fmtTime,
} from './lib.js';
import { ServiceControl } from './pages-common.js';

// ---------- traffic chart (hand-drawn SVG) ----------
function TrafficChart({ history }) {
  const [hover, setHover] = useState(null);
  const pts = useMemo(() => {
    const out = [];
    for (let i = 1; i < (history || []).length; i++) {
      const a = history[i - 1], b = history[i];
      const dt = Math.max(1, b.t - a.t);
      out.push({ t: b.t, in: Math.max(0, (b.in - a.in) / dt), out: Math.max(0, (b.out - a.out) / dt), conns: b.conns });
    }
    return out;
  }, [history]);
  const W = 800, H = 220, L = 56, R = 12, T = 12, B = 26;
  if (pts.length < 2) {
    return html`<div class="empty" style="height:220px;display:grid;place-items:center">正在收集流量数据…（每 5 秒采样一次）</div>`;
  }
  const max = Math.max(1024, ...pts.map(p => Math.max(p.in, p.out))) * 1.15;
  const x = i => L + (W - L - R) * i / (pts.length - 1);
  const y = v => T + (H - T - B) * (1 - v / max);
  const line = k => pts.map((p, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)},${y(p[k]).toFixed(1)}`).join('');
  const area = k => `${line(k)}L${x(pts.length - 1)},${H - B}L${L},${H - B}Z`;
  const ticks = [0, .25, .5, .75, 1].map(f => max * f);
  const tl = t => new Date(t * 1000).toLocaleTimeString('zh-CN', { hour12: false, hour: '2-digit', minute: '2-digit' });
  const onMove = e => {
    const r = e.currentTarget.getBoundingClientRect();
    const px = (e.clientX - r.left) / r.width * W;
    const i = Math.round((px - L) / (W - L - R) * (pts.length - 1));
    setHover(i >= 0 && i < pts.length ? i : null);
  };
  const hp = hover != null ? pts[hover] : null;
  return html`<div style="position:relative">
    <svg class="chart" viewBox="0 0 ${W} ${H}" preserveAspectRatio="none" onMouseMove=${onMove} onMouseLeave=${() => setHover(null)}>
      <defs>
        <linearGradient id="gin" x1="0" y1="0" x2="0" y2="1"><stop offset="0" class="s-in" stop-opacity=".35"/><stop offset="1" class="s-in" stop-opacity="0"/></linearGradient>
        <linearGradient id="gout" x1="0" y1="0" x2="0" y2="1"><stop offset="0" class="s-out" stop-opacity=".3"/><stop offset="1" class="s-out" stop-opacity="0"/></linearGradient>
      </defs>
      ${ticks.map(v => html`<line class="grid-line" x1=${L} x2=${W - R} y1=${y(v)} y2=${y(v)} />
        <text class="axis" x=${L - 8} y=${y(v) + 3} text-anchor="end">${fmtRate(v).replace('/s', '')}</text>`)}
      <text class="axis" x=${L} y=${H - 6}>${tl(pts[0].t)}</text>
      <text class="axis" x=${W - R} y=${H - 6} text-anchor="end">${tl(pts[pts.length - 1].t)}</text>
      <path d=${area('in')} fill="url(#gin)" />
      <path d=${area('out')} fill="url(#gout)" />
      <path d=${line('in')} class="l-in" fill="none" stroke-width="2" vector-effect="non-scaling-stroke" />
      <path d=${line('out')} class="l-out" fill="none" stroke-width="2" vector-effect="non-scaling-stroke" />
      ${hp && html`<line x1=${x(hover)} x2=${x(hover)} y1=${T} y2=${H - B} class="cursor" stroke-dasharray="3 3" vector-effect="non-scaling-stroke" />`}
    </svg>
    ${hp && html`<div class="card" style=${`position:absolute;top:8px;left:${Math.min(70, hover / pts.length * 100)}%;padding:8px 12px;font-size:12px;pointer-events:none`}>
      <div class="muted">${new Date(hp.t * 1000).toLocaleTimeString('zh-CN', { hour12: false })}</div>
      <div>入站 <b>${fmtRate(hp.in)}</b></div><div>出站 <b>${fmtRate(hp.out)}</b></div><div>连接 <b>${hp.conns}</b></div>
    </div>`}
  </div>`;
}

// ---------- dashboard ----------
export function ServerDashboard({ go }) {
  const ov = usePoll(() => api('GET', '/server/overview'), 5000);
  const join = usePoll(() => api('GET', '/server/join'));
  const sys = usePoll(() => api('GET', '/system'));
  const d = ov.data;
  if (!d) return html`<div class="card">${ov.error ? html`<${Alert} kind="err">${ov.error.message}<//>` : html`<${Spinner} />`}</div>`;
  const info = d.info || {};
  const types = Object.entries(info.proxyTypeCount || {}).filter(([, n]) => n > 0);
  return html`<div>
    ${d.infoError && html`<${Alert} kind="warn">${d.infoError}<//>`}
    <div class="grid stats">
      <div class="card stat">
        <div class="label">frps 服务</div>
        <div class="value" style="font-size:18px;margin-top:10px"><${ServiceBadge} s=${d.service} /></div>
        <div class="hint">${d.service.running ? '已运行 ' + since(d.service.since) : '未运行'}${info.version ? ' · v' + info.version : ''}</div>
        <div style="margin-top:10px"><${ServiceControl} status=${d.service} role="server" onChange=${ov.reload} /></div>
      </div>
      <${Stat} label="在线客户端" value=${info.clientCounts ?? '-'} ic="clients" hint="当前连接到本服务器的 frpc" />
      <${Stat} label="代理" value=${d.proxyOnline ?? '-'} unit=${d.proxyTotal != null ? '/ ' + d.proxyTotal : ''} ic="proxies"
        hint=${types.length ? types.map(([t, n]) => `${t.toUpperCase()} ${n}`).join(' · ') : '在线 / 全部'} />
      <${Stat} label="当前连接数" value=${info.curConns ?? '-'} ic="activity" hint="经过 frps 的用户连接" />
      <${Stat} label="总流量" value=${fmtBytes((info.totalTrafficIn || 0) + (info.totalTrafficOut || 0))} ic="zap"
        hint=${`入 ${fmtBytes(info.totalTrafficIn)} · 出 ${fmtBytes(info.totalTrafficOut)}`} />
    </div>
    <div class="card">
      <div class="card-head"><h2>实时流量</h2><div class="grow"></div>
        <div class="legend"><span><i style="background:var(--accent)"></i>入站</span><span><i style="background:var(--accent-2)"></i>出站</span></div></div>
      <${TrafficChart} history=${d.history} />
    </div>
    <div class="grid two mt">
      <div class="card">
        <h2>添加客户端</h2>
        <p class="desc">在内网机器（Ubuntu/Debian）上以 root 运行下面的命令，即可自动安装 frpc 并连接到本服务器。</p>
        ${join.data ? html`<div>
          ${!join.data.publicAddrSet && html`<${Alert} kind="warn">服务器地址取自当前浏览器访问的地址（${join.data.serverAddr}）。如果它不是公网地址，请到「系统」页设置公网地址。<//>`}
          <${CodeBox} text=${join.data.command} />
          <div class="muted small mt">命令中包含连接令牌，请勿公开分享。</div>
        </div>` : html`<${Spinner} />`}
      </div>
      <div class="card">
        <h2>需要放行的端口</h2>
        <p class="desc">请同时在系统防火墙和云服务商的<b>安全组</b>中放行以下端口。安装脚本已尝试自动配置 ufw / firewalld。</p>
        ${sys.data && sys.data.firewallPorts ? html`<div class="row">${sys.data.firewallPorts.map(p => html`<span class="badge mono">${p}</span>`)}
          <span class="badge mono">${(sys.data.listen || '').split(':').pop()}/tcp（面板）</span></div>` : html`<${Spinner} />`}
        <div class="row mt"><button class="btn sm" onClick=${() => go('settings')}>${icon('settings')}服务端设置</button></div>
      </div>
    </div>
  </div>`;
}

// ---------- proxies ----------
export function ServerProxies() {
  const [type, setType] = useState('all');
  const { data, error } = usePoll(() => api('GET', '/server/proxies'), 5000);
  const list = ((data && data.proxies) || []).filter(p => type === 'all' || p.type === type);
  const types = ['all', ...new Set(((data && data.proxies) || []).map(p => p.type))];
  const target = p => {
    const c = p.conf || {};
    if (c.remotePort) return ':' + c.remotePort;
    if (c.customDomains && c.customDomains.length) return c.customDomains.join(', ');
    if (c.subdomain) return c.subdomain + '.*';
    return '-';
  };
  return html`<div class="card">
    <div class="card-head"><h2>代理列表</h2><div class="grow"></div>
      <${Seg} value=${type} onChange=${setType} options=${types.map(t => ({ value: t, label: t === 'all' ? '全部' : t.toUpperCase() }))} /></div>
    ${error && html`<${Alert} kind="err">${error.message}<//>`}
    ${!data && !error ? html`<${Spinner} />` : list.length === 0 ? html`<${Empty} text="还没有客户端注册代理" />` : html`<div class="table-wrap"><table>
      <thead><tr><th>名称</th><th>类型</th><th>公网入口</th><th>客户端</th><th>状态</th><th class="num">连接</th><th class="num">今日入</th><th class="num">今日出</th><th>上线时间</th></tr></thead>
      <tbody>${list.map(p => html`<tr key=${p.type + p.name}>
        <td><b>${p.name}</b></td>
        <td><span class="badge type">${p.type}</span></td>
        <td class="mono">${target(p)}</td>
        <td>${p.hostname || html`<span class="muted mono">${(p.clientID || '').slice(0, 8)}</span>`}</td>
        <td>${p.status === 'online' ? html`<span class="badge ok"><span class="dot ok"></span>在线</span>` : html`<span class="badge"><span class="dot"></span>离线</span>`}</td>
        <td class="num">${p.curConns}</td>
        <td class="num">${fmtBytes(p.todayTrafficIn)}</td>
        <td class="num">${fmtBytes(p.todayTrafficOut)}</td>
        <td class="muted">${p.lastStartTime || '-'}</td>
      </tr>`)}</tbody></table></div>`}
  </div>`;
}

// ---------- clients ----------
export function ServerClients() {
  const { data, error } = usePoll(() => api('GET', '/server/clients'), 5000);
  const list = (data && data.clients) || [];
  return html`<div class="card">
    <div class="card-head"><h2>客户端</h2><span class="muted small">${list.filter(c => c.online).length} 在线 / ${list.length} 个</span></div>
    ${error && html`<${Alert} kind="err">${error.message}<//>`}
    ${!data && !error ? html`<${Spinner} />` : list.length === 0 ? html`<${Empty} text="还没有客户端连接过。到「概览」页复制接入命令开始添加。" />` : html`<div class="table-wrap"><table>
      <thead><tr><th>主机名</th><th>状态</th><th>IP</th><th>版本</th><th>用户</th><th class="num">代理</th><th>首次连接</th><th>最近连接</th></tr></thead>
      <tbody>${list.map(c => html`<tr key=${c.clientID}>
        <td><b>${c.hostname || '-'}</b><div class="muted mono small">${c.clientID.slice(0, 12)}</div></td>
        <td>${c.online ? html`<span class="badge ok"><span class="dot ok"></span>在线</span>` : html`<span class="badge"><span class="dot"></span>离线</span>`}</td>
        <td class="mono">${c.clientIP}</td><td>v${c.version}</td><td>${c.user || '-'}</td><td class="num">${c.proxies}</td>
        <td class="muted">${fmtTime(c.firstConnectedAt)}</td><td class="muted">${fmtTime(c.lastConnectedAt)}</td>
      </tr>`)}</tbody></table></div>`}
  </div>`;
}

// ---------- settings ----------
export function ServerSettings() {
  const [s, setS] = useState(null);
  const [err, setErr] = useState('');
  const [ports, setPorts] = useState(null);
  const [busy, run] = useAction();
  useEffect(() => { api('GET', '/server/settings').then(setS, e => setErr(e.message)); }, []);
  if (!s) return html`<div class="card">${err ? html`<${Alert} kind="err">${err}<//>` : html`<${Spinner} />`}</div>`;
  const set = k => v => setS({ ...s, [k]: v });
  const save = () => run(async () => {
    setErr('');
    if (!confirm('保存后 frps 会重启，所有客户端会自动重连（约几秒）。继续？')) return;
    try {
      const r = await api('PUT', '/server/settings', s);
      setPorts(r.firewallPorts);
      return r;
    } catch (e) { setErr(e.message); throw e; }
  }, r => r && r.message);
  return html`<div>
    ${err && html`<${Alert} kind="err"><span style="white-space:pre-wrap">${err}</span><//>`}
    ${ports && html`<${Alert} kind="ok">已保存。如有新增端口，记得在防火墙/安全组放行：${ports.join('，')}<//>`}
    <div class="grid two">
      <div class="card">
        <h2>连接与认证</h2><p class="desc">frpc 通过这些参数连接到本服务器。</p>
        <div class="fields">
          <${Field} label="监听端口（bindPort）" help="frpc 连接的端口，默认 7000"><${NumInput} value=${s.bindPort} onInput=${set('bindPort')} /><//>
          <${Field} label="监听地址" help="留空表示 0.0.0.0"><${TextInput} value=${s.bindAddr} onInput=${set('bindAddr')} placeholder="0.0.0.0" /><//>
        </div>
        <${Field} label="认证令牌（token）" help="修改后，所有客户端都要同步修改，否则会无法连接。">
          <${SecretInput} value=${s.token} onInput=${set('token')} generate=${32} /><//>
        <${Check} checked=${s.tlsForce} onChange=${set('tlsForce')} label="强制 TLS 加密" hint="拒绝未启用 TLS 的客户端（frpc 默认已启用 TLS），建议保持开启。" />
      </div>
      <div class="card">
        <h2>端口策略</h2><p class="desc">限制客户端可以占用的公网端口，避免误占 22、80 等系统端口。</p>
        <${Field} label="允许的端口（allowPorts）" help="例如 10000-20000,8080；留空表示不限制。">
          <${TextInput} value=${s.allowPorts} onInput=${set('allowPorts')} placeholder="10000-20000" /><//>
        <${Field} label="每个客户端最多端口数" help="0 表示不限制"><${NumInput} value=${s.maxPortsPerClient} onInput=${set('maxPortsPerClient')} /><//>
      </div>
      <div class="card">
        <h2>HTTP / HTTPS 域名转发</h2><p class="desc">需要时开启：多个网站可以共用 80/443 端口，按域名区分。</p>
        <div class="fields">
          <${Field} label="HTTP 端口" help="0 表示关闭，常用 80"><${NumInput} value=${s.vhostHTTPPort} onInput=${set('vhostHTTPPort')} /><//>
          <${Field} label="HTTPS 端口" help="0 表示关闭，常用 443"><${NumInput} value=${s.vhostHTTPSPort} onInput=${set('vhostHTTPSPort')} /><//>
        </div>
        <${Field} label="子域名后缀（subDomainHost）" help="设置为 frp.example.com 并泛解析 *.frp.example.com 后，客户端可以直接使用子域名。">
          <${TextInput} value=${s.subDomainHost} onInput=${set('subDomainHost')} placeholder="frp.example.com" /><//>
      </div>
      <div class="card">
        <h2>高级</h2><p class="desc">弱网环境下可以启用 KCP / QUIC（UDP 端口）。</p>
        <div class="fields">
          <${Field} label="KCP 端口"><${NumInput} value=${s.kcpBindPort} onInput=${set('kcpBindPort')} placeholder="0 = 关闭" /><//>
          <${Field} label="QUIC 端口"><${NumInput} value=${s.quicBindPort} onInput=${set('quicBindPort')} placeholder="0 = 关闭" /><//>
          <${Field} label="日志级别">
            <select class="input" value=${s.logLevel || 'info'} onChange=${e => set('logLevel')(e.target.value)}>
              ${['trace', 'debug', 'info', 'warn', 'error'].map(l => html`<option value=${l}>${l}</option>`)}
            </select><//>
          <${Field} label="日志保留天数"><${NumInput} value=${s.logMaxDays} onInput=${set('logMaxDays')} /><//>
        </div>
      </div>
    </div>
    <div class="row end mt"><button class="btn primary" disabled=${busy} onClick=${save}>${icon(busy ? 'loader' : 'check', busy ? 'spin' : '')}保存并重启 frps</button></div>
  </div>`;
}
