// Managed clients: list, join command, and per-client proxies / settings /
// logs / raw config. Everything is edited here on the server; each client's
// agent picks changes up within seconds.
import {
  html, useState, useEffect, api, usePoll, useAction, icon, copyText, randomSecret,
  Modal, Field, TextInput, NumInput, SecretInput, Check, Seg, Alert, CodeBox, ServiceBadge, Spinner, Empty, since,
} from './lib.js';
import { LogsPage, RawPage } from './pages-common.js';

const TEMPLATES = [
  { id: 'ssh', label: 'SSH', hint: 'TCP 22', p: { name: 'ssh', type: 'tcp', localPort: 22, remotePort: 6022 } },
  { id: 'rdp', label: '远程桌面', hint: 'TCP 3389', p: { name: 'rdp', type: 'tcp', localPort: 3389, remotePort: 13389 } },
  { id: 'web', label: '网站 HTTP', hint: '按域名转发', p: { name: 'web', type: 'http', localPort: 80, customDomains: [] } },
  { id: 'webs', label: '网站 HTTPS', hint: '本地已有证书', p: { name: 'web-https', type: 'https', localPort: 443, customDomains: [] } },
  { id: 'nas', label: 'NAS 管理页', hint: 'TCP 5000', p: { name: 'nas', type: 'tcp', localPort: 5000, remotePort: 15000 } },
  { id: 'mc', label: 'Minecraft', hint: 'TCP 25565', p: { name: 'minecraft', type: 'tcp', localPort: 25565, remotePort: 25565 } },
  { id: 'dns', label: 'UDP 服务', hint: '游戏 / DNS', p: { name: 'udp-service', type: 'udp', localPort: 53, remotePort: 6053 } },
  { id: 'stcp', label: '私密 SSH', hint: 'STCP 不暴露端口', p: { name: 'secret-ssh', type: 'stcp', localPort: 22 } },
  { id: 'custom', label: '自定义', hint: '从空白开始', p: { name: '', type: 'tcp', localPort: 0, remotePort: 0 } },
];

const TYPE_INFO = {
  tcp: '把内网 TCP 服务映射到服务器的一个端口，适合 SSH、远程桌面、数据库等。',
  udp: '把内网 UDP 服务映射到服务器的一个端口，适合游戏、DNS 等。',
  http: '按域名转发网站，多个网站共用服务端的 HTTP 端口；需要服务端开启 vhostHTTPPort。',
  https: '按域名转发 HTTPS 网站（证书在内网服务上）；需要服务端开启 vhostHTTPSPort。',
  stcp: '私密 TCP：不在服务器上开放端口，访问方也要运行 frpc（visitor）并使用相同密钥。',
  xtcp: '点对点打洞，流量尽量不经过服务器；打洞失败时无法连接，访问方同样需要 visitor。',
};

const blank = () => ({
  name: '', type: 'tcp', localIP: '127.0.0.1', localPort: 0, remotePort: 0, customDomains: [], subdomain: '', locations: [],
  httpUser: '', httpPassword: '', hostHeaderRewrite: '', secretKey: '', allowUsers: [],
  useEncryption: false, useCompression: false, bandwidthLimit: '', disabled: false,
});

const list = s => s.split(/[\s,，]+/).map(x => x.trim()).filter(Boolean);

// ---------- proxy editor ----------
function ProxyEditor({ base, initial, onClose, onSaved }) {
  const editing = !!initial;
  const [p, setP] = useState(() => ({ ...blank(), ...(initial || TEMPLATES[0].p), ...(initial ? {} : { localIP: '127.0.0.1' }) }));
  const [tpl, setTpl] = useState(editing ? null : 'ssh');
  const [domains, setDomains] = useState((p.customDomains || []).join(', '));
  const [locs, setLocs] = useState((p.locations || []).join(', '));
  const [users, setUsers] = useState((p.allowUsers || []).join(', '));
  const [err, setErr] = useState('');
  const [busy, run] = useAction();
  const set = k => v => setP(x => ({ ...x, [k]: v }));
  const pickTpl = t => {
    setTpl(t.id);
    const np = { ...blank(), ...t.p };
    if (np.type === 'stcp') np.secretKey = randomSecret(20);
    setP(np);
    setDomains('');
    setErr('');
  };
  const save = () => run(async () => {
    setErr('');
    const body = { ...p, customDomains: list(domains), locations: list(locs), allowUsers: list(users) };
    delete body.status; delete body.err; delete body.remoteAddr; delete body.publicUrl; delete body.plugin;
    try {
      const r = editing
        ? await api('PUT', base + '/proxies/' + encodeURIComponent(initial.name), body)
        : await api('POST', base + '/proxies', body);
      onSaved();
      onClose();
      return r;
    } catch (e) { setErr(e.message); throw e; }
  }, r => r && r.message);

  const isTCP = p.type === 'tcp' || p.type === 'udp';
  const isHTTP = p.type === 'http' || p.type === 'https';
  const isSecret = p.type === 'stcp' || p.type === 'xtcp';
  return html`<${Modal} wide title=${editing ? '编辑代理 · ' + initial.name : '添加代理'} onClose=${onClose}
    footer=${html`<button class="btn" onClick=${onClose}>取消</button>
      <button class="btn primary" disabled=${busy} onClick=${save}>${icon(busy ? 'loader' : 'check', busy ? 'spin' : '')}${editing ? '保存' : '添加'}</button>`}>
    ${!editing && html`<div class="templates">${TEMPLATES.map(t => html`<button type="button" class="tpl ${tpl === t.id ? 'on' : ''}" onClick=${() => pickTpl(t)}><b>${t.label}</b><small>${t.hint}</small></button>`)}</div>`}
    ${err && html`<${Alert} kind="err"><span style="white-space:pre-wrap">${err}</span><//>`}
    ${p.plugin && html`<${Alert} kind="warn">此代理使用了插件「${p.plugin}」，插件参数请在「配置文件」页编辑；这里的修改会保留插件设置。<//>`}
    <div class="fields">
      <${Field} label="名称" help="唯一，只能包含字母、数字、- _ ."><${TextInput} value=${p.name} onInput=${set('name')} placeholder="my-service" /><//>
      <${Field} label="类型"><${Seg} value=${p.type} onChange=${set('type')} options=${['tcp', 'udp', 'http', 'https', 'stcp', 'xtcp'].map(t => ({ value: t, label: t.toUpperCase() }))} /><//>
    </div>
    <div class="muted small" style="margin:-6px 0 14px">${TYPE_INFO[p.type]}</div>
    ${!p.plugin && html`<div class="fields">
      <${Field} label="本地地址" help="内网服务所在机器，本机填 127.0.0.1"><${TextInput} value=${p.localIP} onInput=${set('localIP')} placeholder="127.0.0.1" /><//>
      <${Field} label="本地端口"><${NumInput} value=${p.localPort} onInput=${set('localPort')} /><//>
    </div>`}
    ${isTCP && html`<${Field} label="远程端口" help="服务器上对外开放的端口，0 表示由服务器随机分配。记得在服务器防火墙/安全组放行。">
      <${NumInput} value=${p.remotePort} onInput=${set('remotePort')} /><//>`}
    ${isHTTP && html`<div class="fields">
      <${Field} label="自定义域名" help="多个用逗号分隔，需要解析到服务器"><${TextInput} value=${domains} onInput=${setDomains} placeholder="app.example.com" /><//>
      <${Field} label="子域名" help="需要服务端设置 subDomainHost"><${TextInput} value=${p.subdomain} onInput=${set('subdomain')} placeholder="app" /><//>
    </div>`}
    ${p.type === 'http' && html`<div class="fields">
      <${Field} label="HTTP 认证用户名" help="可选，给网站加一层密码"><${TextInput} value=${p.httpUser} onInput=${set('httpUser')} autocomplete="off" /><//>
      <${Field} label="HTTP 认证密码"><${SecretInput} value=${p.httpPassword} onInput=${set('httpPassword')} generate=${16} /><//>
    </div>`}
    ${isSecret && html`<div class="fields">
      <${Field} label="密钥（secretKey）" help="访问方 visitor 需要填写相同的密钥"><${SecretInput} value=${p.secretKey} onInput=${set('secretKey')} generate=${20} /><//>
      <${Field} label="允许的用户" help="逗号分隔，* 表示所有用户；留空只允许同一用户"><${TextInput} value=${users} onInput=${setUsers} placeholder="*" /><//>
    </div>`}
    <details class="adv">
      <summary>高级选项</summary>
      ${p.type === 'http' && html`<div class="fields">
        <${Field} label="URL 路径（locations）" help="例如 /api，逗号分隔"><${TextInput} value=${locs} onInput=${setLocs} /><//>
        <${Field} label="改写 Host 头"><${TextInput} value=${p.hostHeaderRewrite} onInput=${set('hostHeaderRewrite')} placeholder="127.0.0.1" /><//>
      </div>`}
      <${Field} label="带宽限制" help="例如 1MB 或 512KB，留空不限制"><${TextInput} value=${p.bandwidthLimit} onInput=${set('bandwidthLimit')} placeholder="不限制" /><//>
      <${Check} checked=${p.useEncryption} onChange=${set('useEncryption')} label="额外加密" hint="frpc 与 frps 之间已经有 TLS；在不信任的中转网络中可以再加一层加密" />
      <${Check} checked=${p.useCompression} onChange=${set('useCompression')} label="压缩传输" hint="适合文本类流量，会增加 CPU 占用" />
      <${Check} checked=${p.disabled} onChange=${set('disabled')} label="暂时停用" hint="保留配置，但不启动这个代理" />
    </details>
  <//>`;
}

// ---------- proxy list ----------
function statusBadge(r) {
  switch (r.status) {
    case 'running': return html`<span class="badge ok"><span class="dot ok"></span>运行中</span>`;
    case 'disabled': return html`<span class="badge">已停用</span>`;
    case 'stopped': return html`<span class="badge"><span class="dot"></span>frpc 未运行</span>`;
    case 'offline': return html`<span class="badge"><span class="dot"></span>客户端离线</span>`;
    case 'wait start': return html`<span class="badge warn"><span class="dot warn"></span>等待启动</span>`;
    case 'new': return html`<span class="badge warn"><span class="dot warn"></span>启动中</span>`;
    case 'unknown': return html`<span class="badge warn">未加载</span>`;
    case 'start error': return html`<span class="badge err"><span class="dot err"></span>启动失败</span>`;
    case 'check failed': return html`<span class="badge err"><span class="dot err"></span>健康检查失败</span>`;
    case 'closed': return html`<span class="badge err"><span class="dot err"></span>已关闭</span>`;
    default: return html`<span class="badge err"><span class="dot err"></span>${r.status}</span>`;
  }
}

function ProxyList({ base }) {
  const res = usePoll(() => api('GET', base + '/proxies'), 4000, [base]);
  const [edit, setEdit] = useState(null); // null | 'new' | row
  const [busy, run] = useAction();
  const rows = (res.data && res.data.proxies) || [];
  const del = r => run(async () => {
    if (!confirm(`删除代理「${r.name}」？`)) return;
    const x = await api('DELETE', base + '/proxies/' + encodeURIComponent(r.name));
    res.reload();
    return x;
  }, x => x && x.message);
  const toggle = r => run(async () => {
    const { status, err, remoteAddr, publicUrl, plugin, ...body } = r;
    const x = await api('PUT', base + '/proxies/' + encodeURIComponent(r.name), { ...body, disabled: !r.disabled });
    res.reload();
    return x;
  }, x => x && x.message);
  const local = r => r.plugin ? '插件 ' + r.plugin : `${r.localIP}:${r.localPort}`;
  const remote = r => {
    if (r.type === 'stcp' || r.type === 'xtcp') return '仅限 visitor 访问';
    if (r.publicUrl) return r.publicUrl;
    if (r.type === 'http' || r.type === 'https') return (r.customDomains || []).join(', ') || (r.subdomain ? r.subdomain + '.<subDomainHost>' : '-');
    return r.remotePort ? `${res.data.serverAddr}:${r.remotePort}` : '随机端口';
  };
  return html`<div>
    <div class="row" style="margin-bottom:16px">
      <span class="muted">${rows.length} 个代理 · 保存后客户端自动热重载，不会中断其他代理</span>
      <div class="spacer"></div>
      <button class="btn primary" onClick=${() => setEdit('new')}>${icon('plus')}添加代理</button>
    </div>
    ${res.error && html`<${Alert} kind="err">${res.error.message}<//>`}
    ${res.data && !res.data.online && html`<${Alert} kind="warn">客户端当前离线。修改会先保存在服务端，客户端上线后自动应用。<//>`}
    ${res.data && res.data.online && !res.data.running && rows.length > 0 && html`<${Alert} kind="warn">客户端上的 frpc 当前未运行，代理不会生效。可以点击上方的「重启 frpc」。<//>`}
    ${!res.data ? html`<div class="card"><${Spinner} /></div>` : rows.length === 0
      ? html`<div class="card"><${Empty} text="还没有代理。添加一个，把内网服务暴露到公网吧。"><button class="btn primary" onClick=${() => setEdit('new')}>${icon('plus')}添加第一个代理</button><//></div>`
      : html`<div class="grid cards">${rows.map(r => html`<div class="card pcard ${r.disabled ? 'disabled' : ''}" key=${r.name}>
          <div class="top"><span class="badge type accent">${r.type}</span><span class="name" title=${r.name}>${r.name}</span><div class="spacer"></div>${statusBadge(r)}</div>
          <div class="flow">
            <div><div class="k">本地</div><div class="v" title=${local(r)}>${local(r)}</div></div>
            <div class="arrow">${icon('arrow')}</div>
            <div><div class="k">公网</div><div class="v" title=${remote(r)}>${remote(r)}</div></div>
          </div>
          ${r.err && html`<div class="err-text">${r.err}</div>`}
          <div class="row small muted">
            ${r.useEncryption && html`<span class="badge">加密</span>`}
            ${r.useCompression && html`<span class="badge">压缩</span>`}
            ${r.bandwidthLimit && html`<span class="badge">限速 ${r.bandwidthLimit}</span>`}
            ${r.httpUser && html`<span class="badge">${icon('lock')}密码保护</span>`}
          </div>
          <div class="actions">
            ${r.publicUrl && html`<button class="btn sm ghost" onClick=${() => copyText(r.publicUrl)}>${icon('copy')}复制地址</button>`}
            <div class="spacer"></div>
            <button class="btn sm ghost" disabled=${busy} onClick=${() => toggle(r)}>${r.disabled ? '启用' : '停用'}</button>
            <button class="btn sm ghost icon" title="编辑" onClick=${() => setEdit(r)}>${icon('edit')}</button>
            <button class="btn sm ghost icon danger" title="删除" disabled=${busy} onClick=${() => del(r)}>${icon('trash')}</button>
          </div>
        </div>`)}</div>`}
    ${edit && html`<${ProxyEditor} base=${base} initial=${edit === 'new' ? null : edit} onClose=${() => setEdit(null)} onSaved=${res.reload} />`}
  </div>`;
}

// ---------- join command ----------
export function JoinCard({ go }) {
  const join = usePoll(() => api('GET', '/server/join'));
  const [busy, run] = useAction();
  const reset = () => run(async () => {
    if (!confirm('重置后旧的接入命令立即失效（已接入的客户端不受影响）。继续？')) return;
    await api('POST', '/server/join/reset');
    join.reload();
  }, '接入命令已重置');
  const d = join.data;
  return html`<div class="card">
    <div class="card-head"><h2>添加客户端</h2><div class="grow"></div>
      <button class="btn sm ghost" disabled=${busy} onClick=${reset} title="让旧命令失效">${icon('refresh')}重置命令</button></div>
    <p class="desc">在内网机器（Ubuntu/Debian）上以 root 运行下面的命令。它会从本面板下载程序、自动注册，之后所有配置都在这里管理，客户端无需任何操作。同一条命令可以用于多台机器；一台机器也可以同时接入多台服务端。客户端本机用 <code>sudo frp-client</code> 管理（列表、日志、删除某台服务端、卸载），服务端失联时也能使用。</p>
    ${join.error && html`<${Alert} kind="err">${join.error.message}<//>`}
    ${d ? html`<div>
      ${!d.publicAddrSet && html`<${Alert} kind="warn">命令中的地址 ${d.host} 取自当前浏览器访问的地址。如果客户端访问不到它，请到「系统」页设置公网地址。<//>`}
      <${CodeBox} text=${d.command} />
      <div class="muted small mt">命令含接入密钥，请勿公开；泄露后点击「重置命令」。客户端需要能访问 ${d.panelUrl}（面板端口要在安全组放行）。</div>
      ${go && html`<div class="row mt"><button class="btn sm" onClick=${() => go('clients')}>${icon('clients')}查看客户端</button></div>`}
    </div>` : !join.error && html`<${Spinner} />`}
  </div>`;
}

// ---------- helpers ----------
function OnlineBadge({ c }) {
  if (!c.lastSeen || c.lastSeen.startsWith('0001')) return html`<span class="badge warn"><span class="dot warn"></span>未上线</span>`;
  return c.online ? html`<span class="badge ok"><span class="dot ok"></span>在线</span>` : html`<span class="badge"><span class="dot"></span>离线</span>`;
}

function SyncBadge({ c }) {
  if (!c.online) return null;
  if (c.error) return html`<span class="badge err" title=${c.error}>应用失败</span>`;
  return c.synced ? html`<span class="badge ok">${icon('check')}已同步</span>` : html`<span class="badge warn">同步中</span>`;
}

// ---------- list ----------
function ClientList({ go }) {
  const res = usePoll(() => api('GET', '/clients'), 5000);
  const list = (res.data && res.data.clients) || [];
  const open = c => go('clients/' + c.id);
  return html`<div>
    <div class="card">
      <div class="card-head"><h2>受管客户端</h2><span class="muted small">${list.filter(c => c.online).length} 在线 / ${list.length} 台</span></div>
      ${res.error && html`<${Alert} kind="err">${res.error.message}<//>`}
      ${!res.data && !res.error ? html`<${Spinner} />` : list.length === 0
        ? html`<${Empty} text="还没有客户端。在内网机器上运行下方的接入命令，它会自动出现在这里。" />`
        : html`<div class="table-wrap"><table>
          <thead><tr><th>名称</th><th>状态</th><th>frpc</th><th class="num">代理</th><th>IP</th><th>版本</th><th>最近通信</th></tr></thead>
          <tbody>${list.map(c => html`<tr key=${c.id} class="click" onClick=${() => open(c)}>
            <td><b>${c.name}</b><div class="muted mono small">${c.id}${c.hostname && c.hostname !== c.name ? ' · ' + c.hostname : ''}</div></td>
            <td><div class="row"><${OnlineBadge} c=${c} /><${SyncBadge} c=${c} /></div></td>
            <td>${c.online ? html`<${ServiceBadge} s=${c.service} />` : html`<span class="muted">-</span>`}</td>
            <td class="num">${c.proxyRunning}/${c.proxyTotal}${c.proxyFailed ? html` <span class="badge err">${c.proxyFailed} 失败</span>` : ''}</td>
            <td class="mono">${c.remoteIP || '-'}</td>
            <td class="small">${c.frpVersion ? 'frpc v' + c.frpVersion : '-'}<div class="muted">${c.arch || ''}</div></td>
            <td class="muted">${c.lastSeen && !c.lastSeen.startsWith('0001') ? since(c.lastSeen) + ' 前' : '从未'}</td>
          </tr>`)}</tbody></table></div>`}
    </div>
    <div class="mt"><${JoinCard} /></div>
  </div>`;
}

// ---------- detail ----------
function ClientSettings({ id, onSaved }) {
  const [d, setD] = useState(null);
  const [err, setErr] = useState('');
  const [busy, run] = useAction();
  useEffect(() => { api('GET', '/clients/' + id).then(setD, e => setErr(e.message)); }, [id]);
  if (!d) return html`<div class="card">${err ? html`<${Alert} kind="err">${err}<//>` : html`<${Spinner} />`}</div>`;
  const s = d.settings;
  const set = k => v => setD({ ...d, settings: { ...s, [k]: v } });
  const save = () => run(async () => {
    setErr('');
    try {
      const r = await api('PUT', '/clients/' + id, s);
      onSaved && onSaved();
      return r;
    } catch (e) { setErr(e.message); throw e; }
  }, r => r && r.message);
  return html`<div class="grid two">
    <div class="card">
      <h2>基本</h2><p class="desc">连接参数（服务器地址、端口、令牌）由服务端统一下发，无需为每台客户端设置。</p>
      ${err && html`<${Alert} kind="err"><span style="white-space:pre-wrap">${err}</span><//>`}
      <${Field} label="名称" help="只用于在面板中区分客户端"><${TextInput} value=${s.name} onInput=${set('name')} /><//>
      <dl class="kv">
        <dt>连接到</dt><dd class="mono">${d.server.addr}:${d.server.port}</dd>
        <dt>frps 用户名</dt><dd class="mono">${id}<span class="muted small">（代理在服务端显示为 ${id}.代理名）</span></dd>
      </dl>
      <button class="btn primary" disabled=${busy} onClick=${save}>${icon('check')}保存</button>
    </div>
    <div class="card">
      <h2>传输</h2><p class="desc">一般保持默认即可。修改后客户端的 frpc 会重启，代理会短暂中断。</p>
      <${Field} label="传输协议" help="KCP / QUIC 走 UDP，需要服务端开启对应端口">
        <${Seg} value=${s.protocol || 'tcp'} onChange=${set('protocol')} options=${['tcp', 'kcp', 'quic', 'websocket', 'wss'].map(v => ({ value: v, label: v.toUpperCase() }))} /><//>
      ${d.server.tlsForce
        ? html`<div class="muted small" style="margin-bottom:14px">${icon('lock')} 服务端已开启强制 TLS，客户端始终加密连接。</div>`
        : html`<${Check} checked=${s.tlsEnable} onChange=${set('tlsEnable')} label="启用 TLS 加密" hint="加密 frpc 与 frps 之间的全部流量，强烈建议开启" />`}
      <div class="fields">
        <${Field} label="日志级别">
          <select class="input" value=${s.logLevel || 'info'} onChange=${e => set('logLevel')(e.target.value)}>
            ${['trace', 'debug', 'info', 'warn', 'error'].map(l => html`<option value=${l}>${l}</option>`)}
          </select><//>
        <${Field} label="日志保留天数"><${NumInput} value=${s.logMaxDays} onInput=${set('logMaxDays')} /><//>
      </div>
    </div>
  </div>`;
}

const TABS = [
  { value: 'proxies', label: '代理' }, { value: 'settings', label: '设置' },
  { value: 'logs', label: '日志' }, { value: 'raw', label: '配置文件' },
];

function ClientDetail({ id, go }) {
  const res = usePoll(() => api('GET', '/clients/' + id), 5000, [id]);
  const [tab, setTab] = useState('proxies');
  const [busy, run] = useAction();
  const base = '/clients/' + encodeURIComponent(id);
  if (!res.data) {
    return html`<div class="card">${res.error ? html`<${Alert} kind="err">${res.error.message}<//>` : html`<${Spinner} />`}
      <div class="row mt"><button class="btn sm" onClick=${() => go('clients')}>${icon('arrow')}返回列表</button></div></div>`;
  }
  const c = res.data.client;
  const restart = () => run(async () => {
    if (!confirm(`重启「${c.name}」上的 frpc？它的代理会短暂中断。`)) return;
    return api('POST', base + '/restart');
  }, r => r && r.message);
  const remove = () => run(async () => {
    if (!confirm(`删除客户端「${c.name}」？它的所有代理配置会被删除，该机器上的 frpc 会停止。`)) return;
    const r = await api('DELETE', base);
    go('clients');
    return r;
  }, r => r && r.message);
  return html`<div>
    <div class="card">
      <div class="card-head">
        <button class="btn sm ghost" onClick=${() => go('clients')}>← 全部客户端</button>
        <h2 style="margin:0">${c.name}</h2><${OnlineBadge} c=${c} /><${SyncBadge} c=${c} />
        <div class="grow"></div>
        <button class="btn sm" disabled=${busy || !c.online} onClick=${restart}>${icon('refresh')}重启 frpc</button>
        <button class="btn sm danger" disabled=${busy} onClick=${remove}>${icon('trash')}删除</button>
      </div>
      ${c.error && html`<${Alert} kind="err">客户端报告：<span style="white-space:pre-wrap">${c.error}</span><//>`}
      <dl class="kv">
        <dt>主机名</dt><dd>${c.hostname || '-'} <span class="muted mono small">${c.id}</span></dd>
        <dt>frpc</dt><dd>${c.online ? html`<${ServiceBadge} s=${c.service} />` : '-'} ${c.frpVersion && html`<span class="badge">v${c.frpVersion}</span>`}</dd>
        <dt>来源 IP</dt><dd class="mono">${c.remoteIP || '-'}</dd>
        <dt>系统</dt><dd>linux/${c.arch || '?'} · agent ${c.agentVersion || '-'}</dd>
        <dt>最近通信</dt><dd>${c.lastSeen && !c.lastSeen.startsWith('0001') ? since(c.lastSeen) + ' 前' : '从未'}</dd>
      </dl>
    </div>
    <div class="row" style="margin:16px 0"><${Seg} value=${tab} onChange=${setTab} options=${TABS} /></div>
    ${tab === 'proxies' && html`<${ProxyList} base=${base} />`}
    ${tab === 'settings' && html`<${ClientSettings} id=${id} onSaved=${res.reload} />`}
    ${tab === 'logs' && html`<div>
      ${!c.online && html`<${Alert} kind="warn">客户端离线，下面是它上次上传的日志。<//>`}
      <${LogsPage} path=${base + '/logs'} />
    </div>`}
    ${tab === 'raw' && html`<${RawPage} path=${base + '/raw'} title="frpc.toml"
      note="这里是该客户端的完整 frpc 配置。serverAddr、serverPort、auth、user 由服务端统一填写，在这里修改会被忽略；webServer 固定监听 127.0.0.1。保存前会用 frpc verify 校验。" />`}
  </div>`;
}

export function ClientsPage({ sub, go }) {
  return sub ? html`<${ClientDetail} id=${sub} go=${go} />` : html`<${ClientList} go=${go} />`;
}
