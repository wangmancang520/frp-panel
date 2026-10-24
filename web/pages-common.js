// Pages shared by server and client panels: logs, raw config, system, security.
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

export function LogsPage() {
  const [auto, setAuto] = useState(true);
  const [filter, setFilter] = useState('');
  const [n, setN] = useState(300);
  const { data, error, reload } = usePoll(() => api('GET', '/logs?n=' + n), auto ? 3000 : 0, [auto, n]);
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
      <${Seg} value=${n} onChange=${setN} options=${[{ value: 300, label: '300 行' }, { value: 1000, label: '1000 行' }]} />
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
export function RawPage({ role }) {
  const [content, setContent] = useState(null);
  const [path, setPath] = useState('');
  const [err, setErr] = useState('');
  const [busy, run] = useAction();
  const load = async () => {
    const r = await api('GET', '/raw');
    setContent(r.content);
    setPath(r.path);
    setErr('');
  };
  useEffect(() => { load().catch(e => setErr(e.message)); }, []);
  const save = () => run(async () => {
    setErr('');
    try {
      const r = await api('PUT', '/raw', { content });
      await load();
      return r;
    } catch (e) { setErr(e.message); throw e; }
  }, r => r.message);
  if (content === null) return html`<div class="card">${err ? html`<${Alert} kind="err">${err}<//>` : html`<${Spinner} />`}</div>`;
  return html`<div class="card">
    <div class="card-head">
      <div><h2>${role === 'server' ? 'frps.toml' : 'frpc.toml'}</h2><div class="muted small mono">${path}</div></div>
      <div class="grow"></div>
      <button class="btn sm" onClick=${() => load()}>${icon('refresh')}重新载入</button>
      <button class="btn primary sm" disabled=${busy} onClick=${save}>${icon(busy ? 'loader' : 'check', busy ? 'spin' : '')}校验并保存</button>
    </div>
    <${Alert}>保存前会用 <code>${role === 'server' ? 'frps' : 'frpc'} verify</code> 校验配置，校验失败不会写入。为了安全，<code>webServer.addr</code> 会被强制设为 127.0.0.1。注释不会保留。<//>
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
    if (sys.data && !form) setForm({ githubMirror: sys.data.githubMirror || '', publicAddr: sys.data.publicAddr || '', installScriptURL: sys.data.installScriptURL || '' });
  }, [sys.data]);
  const s = sys.data;
  if (!s) return html`<div class="card">${sys.error ? html`<${Alert} kind="err">${sys.error.message}<//>` : html`<${Spinner} />`}</div>`;
  const name = role === 'server' ? 'frps' : 'frpc';
  const check = () => run(async () => setLatest(await api('GET', '/frp/latest')));
  const upgrade = () => run(async () => {
    if (!confirm(`将 ${name} 升级到 v${latest.latest}？服务会自动重启。`)) return;
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
      <h2>面板设置</h2><p class="desc">影响下载、客户端接入命令等。</p>
      ${form && html`<div>
        <${Field} label="GitHub 下载镜像" help="国内服务器访问 GitHub 慢时填写，如 https://ghfast.top/ ，留空则直连。">
          <${TextInput} value=${form.githubMirror} onInput=${set('githubMirror')} placeholder="https://ghfast.top/" /><//>
        ${role === 'server' && html`<${Field} label="公网地址" help="生成客户端接入命令时使用的 IP 或域名；留空则使用当前浏览器访问的地址。">
          <${TextInput} value=${form.publicAddr} onInput=${set('publicAddr')} placeholder="1.2.3.4 或 frp.example.com" /><//>
        <${Field} label="安装脚本地址" help="接入命令里下载 install.sh 的 URL，自己 fork 项目后修改。">
          <${TextInput} value=${form.installScriptURL} onInput=${set('installScriptURL')} placeholder="https://raw.githubusercontent.com/…/install.sh" /><//>`}
        <button class="btn primary" disabled=${busy} onClick=${savePanel}>保存</button>
      </div>`}
    </div>
    <div class="card">
      <h2>系统信息</h2><p class="desc">FRP Panel v${s.panelVersion}</p>
      <dl class="kv">
        <dt>角色</dt><dd>${role === 'server' ? '服务端（frps）' : '客户端（frpc）'}</dd>
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

// ---------- security / TLS ----------
export function SecurityPage({ me }) {
  const tls = usePoll(() => api('GET', '/tls'));
  const [busy, run] = useAction();
  const [mode, setMode] = useState(null);
  const [acme, setAcme] = useState({ domain: '', email: '' });
  const [custom, setCustom] = useState({ cert: '', key: '' });
  const [sans, setSans] = useState('');
  useEffect(() => {
    const t = tls.data;
    if (t && mode === null) {
      setMode(t.mode);
      setAcme({ domain: t.domain || '', email: t.email || '' });
      setSans((t.extraSANs || []).join(', '));
    }
  }, [tls.data]);
  const t = tls.data;
  const self = t && t.selfSigned;
  const regen = () => run(async () => {
    if (!confirm('重新生成自签名证书后，浏览器会再次提示证书不受信任，需要重新确认指纹。继续？')) return;
    await api('POST', '/tls/regenerate', { extraSANs: sans.split(/[\s,]+/).filter(Boolean) });
    tls.reload();
  }, '证书已重新生成，刷新页面后生效');
  const applyMode = () => run(async () => {
    if (mode === 'custom') await api('POST', '/tls/custom', custom);
    else await api('PUT', '/tls', { mode, domain: acme.domain, email: acme.email });
    tls.reload();
  }, '证书设置已应用');

  return html`<div class="grid two">
    <div>
      <div class="card">
        <h2>管理员账号</h2><p class="desc">修改密码会注销其他所有会话。连续输错 5 次会锁定 15 分钟。</p>
        <${PasswordForm} username=${me.username} />
      </div>
    </div>
    <div>
      <div class="card">
        <div class="card-head"><h2>HTTPS 证书</h2><span class="badge ok">${icon('lock')}强制 HTTPS</span></div>
        <${Alert}>面板只提供 HTTPS，用 http:// 访问会被重定向，不会以明文传输任何内容。<b>用 IP 访问时始终使用自签名证书</b>；配置域名证书后，用域名访问就不会再有浏览器警告。<//>
        ${!t ? html`<${Spinner} />` : html`<div>
          <h2 class="mt">自签名证书</h2>
          <p class="desc">浏览器提示“不安全”时，请核对证书指纹与下方一致，以防中间人攻击。</p>
          <dl class="kv">
            <dt>SHA-256 指纹</dt><dd class="fp">${self.fingerprint} <button class="btn ghost sm icon" onClick=${() => copyText(self.fingerprint)}>${icon('copy')}</button></dd>
            <dt>包含的 IP</dt><dd class="mono small">${(self.ips || []).join(', ')}</dd>
            <dt>包含的域名</dt><dd class="mono small">${(self.dnsNames || []).join(', ')}</dd>
            <dt>有效期至</dt><dd>${new Date(self.notAfter).toLocaleDateString('zh-CN')}</dd>
          </dl>
          <${Field} label="额外的 IP / 域名" help="例如 NAT 后的公网 IP，多个用逗号分隔。修改后需重新生成证书。">
            <${TextInput} value=${sans} onInput=${setSans} placeholder="1.2.3.4, panel.example.com" /><//>
          <button class="btn" disabled=${busy} onClick=${regen}>${icon('refresh')}重新生成自签名证书</button>

          <h2 class="mt" style="margin-top:28px">域名证书</h2>
          <p class="desc">可选。用域名访问时使用；用 IP 访问时仍然使用自签名证书。</p>
          <div class="field"><${Seg} value=${mode} onChange=${setMode} options=${[
            { value: 'selfsigned', label: '仅自签名' }, { value: 'acme', label: "Let's Encrypt 自动申请" }, { value: 'custom', label: '上传证书' }]} /></div>
          ${mode === 'acme' && html`<div>
            <${Alert} kind="warn">需要域名已解析到本机，并且 80 端口可用（HTTP-01 验证）；如果面板监听 443 端口，也可以用 TLS-ALPN 验证。<//>
            <div class="fields">
              <${Field} label="域名"><${TextInput} value=${acme.domain} onInput=${v => setAcme({ ...acme, domain: v })} placeholder="panel.example.com" /><//>
              <${Field} label="邮箱（可选）"><${TextInput} value=${acme.email} onInput=${v => setAcme({ ...acme, email: v })} placeholder="you@example.com" /><//>
            </div>
            ${t.acme && html`<div class="muted small">当前证书：${t.acme.issuer}，到期 ${new Date(t.acme.notAfter).toLocaleDateString('zh-CN')}</div>`}
            ${t.acmeError && html`<${Alert} kind="err">${t.acmeError}<//>`}
          </div>`}
          ${mode === 'custom' && html`<div>
            ${t.custom && html`<div class="muted small" style="margin-bottom:10px">当前证书：${(t.custom.dnsNames || []).join(', ')}，到期 ${new Date(t.custom.notAfter).toLocaleDateString('zh-CN')}</div>`}
            <${Field} label="证书（PEM，含中间证书链）"><textarea class="input" rows="5" value=${custom.cert} onInput=${e => setCustom({ ...custom, cert: e.target.value })} placeholder="-----BEGIN CERTIFICATE-----"></textarea><//>
            <${Field} label="私钥（PEM）"><textarea class="input" rows="4" value=${custom.key} onInput=${e => setCustom({ ...custom, key: e.target.value })} placeholder="-----BEGIN PRIVATE KEY-----"></textarea><//>
          </div>`}
          <button class="btn primary" disabled=${busy || (mode === 'custom' && (!custom.cert || !custom.key))} onClick=${applyMode}>应用证书设置</button>
        </div>`}
      </div>
    </div>
  </div>`;
}
