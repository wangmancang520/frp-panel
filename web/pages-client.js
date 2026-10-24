// Client (frpc) pages.
import {
  html, useState, useEffect, api, usePoll, useAction, toast, icon, copyText, randomSecret,
  Modal, Field, TextInput, NumInput, SecretInput, Check, Seg, Alert, ServiceBadge, Stat, Spinner, Empty, since,
} from './lib.js';
import { ServiceControl } from './pages-common.js';

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
function ProxyEditor({ initial, onClose, onSaved }) {
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
        ? await api('PUT', '/client/proxies/' + encodeURIComponent(initial.name), body)
        : await api('POST', '/client/proxies', body);
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
    case 'wait start': return html`<span class="badge warn"><span class="dot warn"></span>等待启动</span>`;
    case 'new': return html`<span class="badge warn"><span class="dot warn"></span>启动中</span>`;
    case 'unknown': return html`<span class="badge warn">未加载</span>`;
    case 'start error': return html`<span class="badge err"><span class="dot err"></span>启动失败</span>`;
    case 'check failed': return html`<span class="badge err"><span class="dot err"></span>健康检查失败</span>`;
    case 'closed': return html`<span class="badge err"><span class="dot err"></span>已关闭</span>`;
    default: return html`<span class="badge err"><span class="dot err"></span>${r.status}</span>`;
  }
}

export function ClientProxies() {
  const res = usePoll(() => api('GET', '/client/proxies'), 4000);
  const [edit, setEdit] = useState(null); // null | 'new' | row
  const [busy, run] = useAction();
  const rows = (res.data && res.data.proxies) || [];
  const del = r => run(async () => {
    if (!confirm(`删除代理「${r.name}」？`)) return;
    const x = await api('DELETE', '/client/proxies/' + encodeURIComponent(r.name));
    res.reload();
    return x;
  }, x => x && x.message);
  const toggle = r => run(async () => {
    const { status, err, remoteAddr, publicUrl, plugin, ...body } = r;
    const x = await api('PUT', '/client/proxies/' + encodeURIComponent(r.name), { ...body, disabled: !r.disabled });
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
      <span class="muted">${rows.length} 个代理 · 保存后自动热重载，不会中断其他代理</span>
      <div class="spacer"></div>
      <button class="btn primary" onClick=${() => setEdit('new')}>${icon('plus')}添加代理</button>
    </div>
    ${res.error && html`<${Alert} kind="err">${res.error.message}<//>`}
    ${res.data && !res.data.running && rows.length > 0 && html`<${Alert} kind="warn">frpc 当前未运行，代理不会生效。请到「概览」页启动服务。<//>`}
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
    ${edit && html`<${ProxyEditor} initial=${edit === 'new' ? null : edit} onClose=${() => setEdit(null)} onSaved=${res.reload} />`}
  </div>`;
}

// ---------- dashboard ----------
export function ClientDashboard({ go }) {
  const ov = usePoll(() => api('GET', '/client/overview'), 5000);
  const d = ov.data;
  if (!d) return html`<div class="card">${ov.error ? html`<${Alert} kind="err">${ov.error.message}<//>` : html`<${Spinner} />`}</div>`;
  const c = d.common;
  return html`<div>
    ${d.service.running && d.proxyFailed > 0 && html`<${Alert} kind="err">有 ${d.proxyFailed} 个代理启动失败，请到「代理」页查看原因。<//>`}
    <div class="grid stats">
      <div class="card stat">
        <div class="label">frpc 服务</div>
        <div class="value" style="font-size:18px;margin-top:10px"><${ServiceBadge} s=${d.service} /></div>
        <div class="hint">${d.service.running ? '已运行 ' + since(d.service.since) : '未运行'}</div>
        <div style="margin-top:10px"><${ServiceControl} status=${d.service} role="client" onChange=${ov.reload} /></div>
      </div>
      <${Stat} label="服务器" value=${html`<span class="mono" style="font-size:17px">${c.serverAddr}:${c.serverPort}</span>`} ic="server"
        hint=${`${(c.protocol || 'tcp').toUpperCase()} · ${c.tlsEnable ? 'TLS 加密' : '未加密'}`} />
      <${Stat} label="运行中的代理" value=${d.proxyRunning} unit=${'/ ' + d.proxyTotal} ic="proxies" hint=${d.proxyFailed ? d.proxyFailed + ' 个失败' : '全部正常'} />
    </div>
    <div class="grid two">
      <div class="card">
        <h2>快速开始</h2>
        <p class="desc">frpc 已连接到服务器后，添加代理就能把内网服务暴露出去。</p>
        <div class="row">
          <button class="btn primary" onClick=${() => go('proxies')}>${icon('plus')}管理代理</button>
          <button class="btn" onClick=${() => go('connection')}>${icon('link')}连接设置</button>
          <button class="btn" onClick=${() => go('logs')}>${icon('logs')}查看日志</button>
        </div>
      </div>
      <div class="card">
        <h2>排查连接问题</h2>
        <ul class="muted small" style="margin:0;padding-left:18px;line-height:1.9">
          <li>服务器防火墙 / 安全组是否放行了 ${c.serverPort}/tcp</li>
          <li>令牌（token）是否和服务端一致</li>
          <li>服务端开启了「强制 TLS」时，这里必须启用 TLS</li>
          <li>代理的远程端口是否在服务端允许的范围内</li>
        </ul>
      </div>
    </div>
  </div>`;
}

// ---------- connection settings ----------
export function ClientConnection() {
  const [c, setC] = useState(null);
  const [err, setErr] = useState('');
  const [test, setTest] = useState(null);
  const [busy, run] = useAction();
  useEffect(() => { api('GET', '/client/common').then(setC, e => setErr(e.message)); }, []);
  if (!c) return html`<div class="card">${err ? html`<${Alert} kind="err">${err}<//>` : html`<${Spinner} />`}</div>`;
  const set = k => v => setC({ ...c, [k]: v });
  const doTest = () => run(async () => {
    setTest(null);
    setTest(await api('POST', '/client/test', { addr: c.serverAddr, port: c.serverPort }));
  });
  const save = () => run(async () => {
    setErr('');
    try { return await api('PUT', '/client/common', c); } catch (e) { setErr(e.message); throw e; }
  }, r => r && r.message);
  return html`<div class="grid two">
    <div class="card">
      <h2>服务器连接</h2><p class="desc">修改连接参数后 frpc 会重启，所有代理会短暂中断。</p>
      ${err && html`<${Alert} kind="err"><span style="white-space:pre-wrap">${err}</span><//>`}
      <div class="fields">
        <${Field} label="服务器地址"><${TextInput} value=${c.serverAddr} onInput=${set('serverAddr')} placeholder="1.2.3.4" /><//>
        <${Field} label="服务器端口"><${NumInput} value=${c.serverPort} onInput=${set('serverPort')} /><//>
      </div>
      <${Field} label="认证令牌（token）" help="必须和服务端一致"><${SecretInput} value=${c.token} onInput=${set('token')} /><//>
      <div class="row" style="margin-bottom:14px">
        <button class="btn sm" disabled=${busy} onClick=${doTest}>${icon('activity')}测试 TCP 连通性</button>
        ${test && (test.ok ? html`<span class="badge ok">${icon('check')}可以连接 · ${test.latency}</span>` : html`<span class="badge err">无法连接：${test.error}</span>`)}
      </div>
      <button class="btn primary" disabled=${busy} onClick=${save}>${icon('check')}保存并重启 frpc</button>
    </div>
    <div class="card">
      <h2>传输</h2><p class="desc">一般保持默认即可。</p>
      <${Field} label="传输协议" help="KCP / QUIC 走 UDP，需要服务端开启对应端口">
        <${Seg} value=${c.protocol || 'tcp'} onChange=${set('protocol')} options=${['tcp', 'kcp', 'quic', 'websocket', 'wss'].map(v => ({ value: v, label: v.toUpperCase() }))} /><//>
      <${Check} checked=${c.tlsEnable} onChange=${set('tlsEnable')} label="启用 TLS 加密" hint="加密 frpc 与 frps 之间的全部流量，强烈建议开启" />
      ${!c.tlsEnable && html`<${Alert} kind="warn">关闭 TLS 后，代理流量会在公网上明文传输。<//>`}
      <${Field} label="用户名（user）" help="可选，会作为代理名前缀，用于区分多个客户端"><${TextInput} value=${c.user} onInput=${set('user')} /><//>
      <div class="fields">
        <${Field} label="日志级别">
          <select class="input" value=${c.logLevel || 'info'} onChange=${e => set('logLevel')(e.target.value)}>
            ${['trace', 'debug', 'info', 'warn', 'error'].map(l => html`<option value=${l}>${l}</option>`)}
          </select><//>
        <${Field} label="日志保留天数"><${NumInput} value=${c.logMaxDays} onInput=${set('logMaxDays')} /><//>
      </div>
    </div>
  </div>`;
}
