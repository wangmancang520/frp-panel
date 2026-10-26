// Shared pages: logs, raw config (frps or a managed client), system, security.
import {
  html, useState, useEffect, useRef, useMemo, api, usePoll, useAction, toast, icon,
  Field, TextInput, SecretInput, Seg, Alert, CodeBox, ServiceBadge, Spinner,
  fmtDuration, since, copyText,
} from './lib.js';

// ---------- service control (used on dashboards and System page) ----------
export function ServiceControl({ status, onChange, role }) {
  const [busy, run] = useAction();
  const name = role === 'server' ? 'frps' : 'frpc';
  const act = (a, label) => run(async () => {
    if (a !== 'start' && !confirm(`确定要${label} ${name} 吗？${role === 'server' ? '所有客户端连接会短暂中断。' : ''}`)) return;
    await api('POST', '/service/' + a);
    onChange && onChange();
  }, `${name} 已${label}`);
  const running = status && status.running;
  return html`<div class="row">
    ${!running && html`<button class="btn primary sm" disabled=${busy} onClick=${() => act('start', '启动')}>${icon('play')}启动</button>`}
    ${running && html`<button class="btn sm" disabled=${busy} onClick=${() => act('restart', '重启')}>${icon('refresh', busy ? 'spin' : '')}重启</button>`}
    ${running && html`<button class="btn sm danger" disabled=${busy} onClick=${() => act('stop', '停止')}>${icon('stop')}停止</button>`}
  </div>`;
}

// ---------- logs ----------
function lineClass(l) {
  if (/\[E\]|error|failed|panic/i.test(l)) return 'l-err';
  if (/\[W\]|warn/i.test(l)) return 'l-warn';
  if (/\[panel\]/.test(l)) return 'l-dim';
  return '';
}

// LogsPage shows frps logs, or a managed client's when path is given (those
// come from the agent, which sends its last 300 lines).
export function LogsPage({ path }) {
  const [auto, setAuto] = useState(true);
  const [filter, setFilter] = useState('');
  const [n, setN] = useState(300);
  const { data, error, reload } = usePoll(() => api('GET', path || '/logs?n=' + n), auto ? 3000 : 0, [auto, n, path]);
  const box = useRef();
  const stick = useRef(true);
  const lines = useMemo(() => {
    const all = (data && data.lines) || [];
    const f = filter.trim().toLowerCase();
    return f ? all.filter(l => l.toLowerCase().includes(f)) : all;
  }, [data, filter]);
  useEffect(() => {
    if (box.current && stick.current) box.current.scrollTop = box.current.scrollHeight;
  }, [lines]);
  return html`<div class="card">
    <div class="card-head">
      <input class="input" style="max-width:280px" placeholder="过滤日志…" value=${filter} onInput=${e => setFilter(e.target.value)} />
      ${!path && html`<${Seg} value=${n} onChange=${setN} options=${[{ value: 300, label: '300 行' }, { value: 1000, label: '1000 行' }]} />`}
      <div class="grow"></div>
      <label class="check mb0"><input type="checkbox" checked=${auto} onChange=${e => setAuto(e.target.checked)} /><span>自动刷新</span></label>
      <button class="btn sm" onClick=${reload}>${icon('refresh')}刷新</button>
    </div>
    ${error && html`<${Alert} kind="err">${error.message}<//>`}
    <div class="logs" ref=${box} onScroll=${e => { const t = e.target; stick.current = t.scrollHeight - t.scrollTop - t.clientHeight < 40; }}>
      ${lines.length ? lines.map((l, i) => html`<div key=${i} class=${lineClass(l)}>${l}</div>`) : html`<div class="l-dim">暂无日志</div>`}
    </div>
  </div>`;
}

// ---------- raw config ----------
export function RawPage({ path = '/raw', title = 'frps.toml', note }) {
  const [content, setContent] = useState(null);
  const [file, setFile] = useState('');
  const [err, setErr] = useState('');
  const [busy, run] = useAction();
  const load = async () => {
    const r = await api('GET', path);
    setContent(r.content);
    setFile(r.path);
    setErr('');
  };
  useEffect(() => { load().catch(e => setErr(e.message)); }, [path]);
  const save = () => run(async () => {
    setErr('');
    try {
      const r = await api('PUT', path, { content });
      await load();
      return r;
    } catch (e) { setErr(e.message); throw e; }
  }, r => r.message);
  if (content === null) return html`<div class="card">${err ? html`<${Alert} kind="err">${err}<//>` : html`<${Spinner} />`}</div>`;
  return html`<div class="card">
    <div class="card-head">
      <div><h2>${title}</h2><div class="muted small mono">${file}</div></div>
      <div class="grow"></div>
      <button class="btn sm" onClick=${() => load()}>${icon('refresh')}重新载入</button>
      <button class="btn primary sm" disabled=${busy} onClick=${save}>${icon(busy ? 'loader' : 'check', busy ? 'spin' : '')}校验并保存</button>
    </div>
    <${Alert}>${note || html`保存前会用 <code>frps verify</code> 校验配置，校验失败不会写入。为了安全，<code>webServer.addr</code> 会被强制设为 127.0.0.1。注释不会保留。`}<//>
    ${err && html`<${Alert} kind="err"><pre class="mono mb0" style="white-space:pre-wrap;margin:0">${err}</pre><//>`}
    <textarea class="input" rows="28" spellcheck="false" value=${content} onInput=${e => setContent(e.target.value)}></textarea>
  </div>`;
}

// ---------- system ----------
export function SystemPage({ role }) {
  const sys = usePoll(() => api('GET', '/system'), 5000);
  const [latest, setLatest] = useState(null);
  const [busy, run] = useAction();
  const [form, setForm] = useState(null);
  useEffect(() => {
    if (sys.data && !form) setForm({ githubMirror: sys.data.githubMirror || '', publicAddr: sys.data.publicAddr || '' });
  }, [sys.data]);
  const s = sys.data;
  if (!s) return html`<div class="card">${sys.error ? html`<${Alert} kind="err">${sys.error.message}<//>` : html`<${Spinner} />`}</div>`;
  const name = role === 'server' ? 'frps' : 'frpc';
  const check = () => run(async () => setLatest(await api('GET', '/frp/latest')));
  const upgrade = () => run(async () => {
    if (!confirm(`将 FRP 升级到 v${latest.latest}？frps 会重启，受管客户端随后会自动升级 frpc。`)) return;
    await api('POST', '/frp/upgrade', { version: latest.latest });
    setLatest(await api('GET', '/frp/latest'));
    sys.reload();
  }, '升级完成');
  const savePanel = () => run(() => api('PUT', '/panel/settings', form), '面板设置已保存');
  const set = k => v => setForm({ ...form, [k]: v });

  return html`<div class="grid two">
    <div class="card">
      <div class="card-head"><h2>${name} 服务</h2><${ServiceBadge} s=${s.service} /><div class="grow"></div><${ServiceControl} status=${s.service} role=${role} onChange=${sys.reload} /></div>
      <dl class="kv">
        <dt>状态</dt><dd class="mono">${s.service.state || '-'}</dd>
        <dt>PID</dt><dd>${s.service.pid || '-'}</dd>
        <dt>已运行</dt><dd>${s.service.running ? since(s.service.since) : '-'}</dd>
        <dt>进程管理</dt><dd>${s.procMode === 'systemd' ? 'systemd' : '面板托管（exec）'}</dd>
        <dt>配置文件</dt><dd class="mono">${s.configPath}</dd>
      </dl>
    </div>
    <div class="card">
      <div class="card-head"><h2>FRP 版本</h2><div class="grow"></div>
        <button class="btn sm" disabled=${busy} onClick=${check}>${icon('refresh', busy ? 'spin' : '')}检查更新</button></div>
      <dl class="kv">
        <dt>已安装</dt><dd>${s.frpVersion ? html`<span class="badge accent">v${s.frpVersion}</span>` : html`<span class="badge err">未找到 ${name}</span>`}</dd>
        <dt>最新版本</dt><dd>${latest ? html`<span class="badge">v${latest.latest}</span>` : html`<span class="muted">点击“检查更新”</span>`}</dd>
      </dl>
      ${latest && latest.latest !== latest.installed && html`<div class="mt"><button class="btn primary" disabled=${busy} onClick=${upgrade}>${icon('download')}升级到 v${latest.latest}</button>
        <div class="muted small mt">下载后会按官方发布的 SHA-256 校验文件完整性。</div></div>`}
      ${latest && latest.latest === latest.installed && html`<div class="mt"><span class="badge ok">${icon('check')}已是最新</span></div>`}
    </div>
    <div class="card">
      <h2>面板设置</h2><p class="desc">影响下载、客户端接入命令和客户端连接的服务器地址。</p>
      ${form && html`<div>
        <${Field} label="GitHub 下载镜像" help="国内服务器访问 GitHub 慢时填写，如 https://ghfast.top/ ，留空则直连。">
          <${TextInput} value=${form.githubMirror} onInput=${set('githubMirror')} placeholder="https://ghfast.top/" /><//>
        <${Field} label="公网地址" help="客户端连接 frps 和面板时使用的 IP 或域名；留空则使用客户端访问面板时的地址。修改后所有客户端会自动更新。">
          <${TextInput} value=${form.publicAddr} onInput=${set('publicAddr')} placeholder="1.2.3.4 或 frp.example.com" /><//>
        <button class="btn primary" disabled=${busy} onClick=${savePanel}>保存</button>
      </div>`}
    </div>
    <div class="card">
      <h2>系统信息</h2><p class="desc">FRP Panel v${s.panelVersion}</p>
      <dl class="kv">
        <dt>角色</dt><dd>服务端（frps）</dd>
        <dt>主机名</dt><dd>${s.hostname}</dd>
        <dt>系统</dt><dd>${s.os}</dd>
        <dt>面板监听</dt><dd class="mono">${s.listen}（仅 HTTPS）</dd>
        <dt>面板运行</dt><dd>${fmtDuration(s.panelUptime)}</dd>
      </dl>
    </div>
  </div>`;
}

// ---------- password ----------
function strength(p) {
  let s = 0;
  if (p.length >= 10) s++;
  if (p.length >= 14) s++;
  if (/[a-z]/.test(p) && /[A-Z]/.test(p)) s++;
  if (/\d/.test(p)) s++;
  if (/[^A-Za-z0-9]/.test(p)) s++;
  return Math.min(s, 4);
}

export function PasswordForm({ forced, username, onDone }) {
  const [f, setF] = useState({ old: '', new: '', confirm: '', username: username || 'admin' });
  const [err, setErr] = useState('');
  const [busy, run] = useAction();
  const st = strength(f.new);
  const colors = ['var(--err)', 'var(--err)', 'var(--warn)', 'var(--ok)', 'var(--ok)'];
  const submit = e => {
    e.preventDefault();
    setErr('');
    if (f.new !== f.confirm) { setErr('两次输入的新密码不一致'); return; }
    run(async () => {
      try {
        await api('POST', '/password', { old: f.old, new: f.new, username: f.username });
      } catch (e) { setErr(e.message); throw e; }
      setF({ ...f, old: '', new: '', confirm: '' });
      onDone && onDone();
    }, '密码已更新，其他会话已全部注销');
  };
  const set = k => e => setF({ ...f, [k]: e.target.value });
  return html`<form onSubmit=${submit} autocomplete="off">
    ${forced && html`<${Alert} kind="warn">这是首次登录（或密码已被重置），请先设置一个新密码。<//>`}
    ${err && html`<${Alert} kind="err">${err}<//>`}
    <div class="field"><label>用户名</label><input class="input" value=${f.username} onInput=${set('username')} autocomplete="username" /></div>
    <div class="field"><label>当前密码</label><input class="input" type="password" value=${f.old} onInput=${set('old')} autocomplete="current-password" required /></div>
    <div class="field"><label>新密码</label><input class="input" type="password" value=${f.new} onInput=${set('new')} autocomplete="new-password" required />
      <div class="help">至少 10 位，包含大写、小写、数字、符号中的三种</div></div>
    <div class="strength"><i style=${`width:${st * 25}%;background:${colors[st]}`}></i></div>
    <div class="field"><label>确认新密码</label><input class="input" type="password" value=${f.confirm} onInput=${set('confirm')} autocomplete="new-password" required /></div>
    <button class="btn primary" style="width:100%" disabled=${busy}>${busy ? '保存中…' : '更新密码'}</button>
  </form>`;
}

// ---------- security ----------
export function SecurityPage({ me, go }) {
  return html`<div class="grid two">
    <div class="card">
      <h2>管理员账号</h2><p class="desc">修改密码会注销其他所有会话。连续输错 5 次会锁定 15 分钟。</p>
      <${PasswordForm} username=${me.username} />
    </div>
    <div class="card">
      <h2>HTTPS 证书</h2>
      <p class="desc">面板只提供 HTTPS。域名证书（Let's Encrypt 或上传证书）和自签名证书都在「证书」页管理。</p>
      <button class="btn" onClick=${() => go('cert')}>${icon('lock')}前往证书页</button>
    </div>
  </div>`;
}
