// App shell: session check, login, navigation, theme.
import { html, render, useState, useEffect, api, setUnauthorizedHandler, Toasts, icon, Alert, Spinner } from './lib.js';
import { LogsPage, RawPage, SystemPage, SecurityPage, PasswordForm } from './pages-common.js';
import { ServerDashboard, ServerProxies, ServerSettings } from './pages-server.js';
import { ClientsPage } from './pages-clients.js';

const NAV = {
  server: [
    { id: 'dashboard', label: '概览', icon: 'dashboard', page: ServerDashboard },
    { id: 'clients', label: '客户端', icon: 'clients', page: ClientsPage },
    { id: 'proxies', label: '在线代理', icon: 'proxies', page: ServerProxies },
    { id: 'settings', label: '服务端设置', icon: 'settings', page: ServerSettings },
  ],
  common: [
    { id: 'logs', label: '日志', icon: 'logs', page: LogsPage },
    { id: 'raw', label: '配置文件', icon: 'code', page: RawPage },
    { id: 'system', label: '系统', icon: 'server', page: SystemPage },
    { id: 'security', label: '安全', icon: 'shield', page: SecurityPage },
  ],
};

const SUBTITLE = {
  dashboard: '运行状态一览', proxies: 'frps 上正在运行的代理', clients: '集中管理所有内网机器及其代理', settings: 'frps.toml 常用参数',
  logs: 'frps 实时运行日志', raw: '直接编辑 frps.toml', system: '服务控制与版本管理', security: '账号与 HTTPS 证书',
};

// ---------- theme ----------
function getTheme() { try { return localStorage.getItem('theme') || ''; } catch { return ''; } }
function applyTheme(t) {
  if (t) document.documentElement.dataset.theme = t; else delete document.documentElement.dataset.theme;
  try { t ? localStorage.setItem('theme', t) : localStorage.removeItem('theme'); } catch { /* private mode */ }
}
applyTheme(getTheme());
function ThemeButton() {
  const [t, setT] = useState(getTheme());
  const dark = t === 'dark' || (!t && matchMedia('(prefers-color-scheme: dark)').matches);
  return html`<button class="btn ghost icon" title="切换主题" onClick=${() => { const n = dark ? 'light' : 'dark'; applyTheme(n); setT(n); }}>${icon(dark ? 'sun' : 'moon')}</button>`;
}

// ---------- login ----------
function Login({ onLogin }) {
  const [u, setU] = useState('admin');
  const [p, setP] = useState('');
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  const submit = async e => {
    e.preventDefault();
    setBusy(true);
    setErr('');
    try {
      await api('POST', '/login', { username: u, password: p });
      onLogin();
    } catch (x) {
      setErr(x.message);
      setP('');
    } finally {
      setBusy(false);
    }
  };
  return html`<div class="auth-wrap"><div class="card auth-card">
    <div class="brand"><img src="favicon.svg" alt="" /><div><b>FRP Panel</b></div></div>
    <p class="lead">登录以管理你的内网穿透服务</p>
    ${err && html`<${Alert} kind="err">${err}<//>`}
    <form onSubmit=${submit}>
      <div class="field"><label>用户名</label><input class="input" value=${u} onInput=${e => setU(e.target.value)} autocomplete="username" required /></div>
      <div class="field"><label>密码</label><input class="input" type="password" value=${p} onInput=${e => setP(e.target.value)} autocomplete="current-password" autofocus required /></div>
      <button class="btn primary" style="width:100%;height:40px" disabled=${busy}>${busy ? '登录中…' : '登录'}</button>
    </form>
    <div class="secure-note">${icon('lock')}连接已通过 HTTPS 加密</div>
  </div></div>`;
}

function ForcedPassword({ me, onDone }) {
  return html`<div class="auth-wrap"><div class="card auth-card" style="width:min(440px,100%)">
    <div class="brand"><img src="favicon.svg" alt="" /><div><b>设置新密码</b></div></div>
    <p class="lead">为了安全，请先替换安装时生成的初始密码</p>
    <${PasswordForm} forced username=${me.username} onDone=${onDone} />
  </div></div>`;
}

// ---------- shell ----------
function routeFromHash() { return (location.hash || '#/dashboard').replace(/^#\/?/, '') || 'dashboard'; }

function Shell({ me, onLogout }) {
  const [route, setRoute] = useState(routeFromHash());
  useEffect(() => {
    const h = () => setRoute(routeFromHash());
    window.addEventListener('hashchange', h);
    return () => window.removeEventListener('hashchange', h);
  }, []);
  const items = [...NAV.server, ...NAV.common];
  // Routes may carry a sub-path, e.g. #/clients/<id>.
  const [top, ...rest] = route.split('/');
  const cur = items.find(i => i.id === top) || items[0];
  const sub = cur.id === top ? rest.join('/') : '';
  const go = id => { location.hash = '#/' + id; };
  const logout = async () => { try { await api('POST', '/logout'); } catch { /* ignore */ } onLogout(); };
  const navItem = i => html`<a class="nav-item ${i.id === cur.id ? 'active' : ''}" href=${'#/' + i.id}>${icon(i.icon)}<span>${i.label}</span></a>`;
  const Page = cur.page;
  return html`<div class="shell">
    <aside class="side">
      <div class="brand"><img src="favicon.svg" alt="" /><div><b>FRP Panel</b><small>服务端 · 集中管理</small></div></div>
      ${NAV.server.map(navItem)}
      <div class="nav-sep"></div>
      ${NAV.common.map(navItem)}
      <div class="side-foot"><${ThemeButton} /><div class="spacer"></div>
        <button class="btn ghost sm" onClick=${logout} title="退出登录">${icon('logout')}${me.username}</button></div>
    </aside>
    <main class="main">
      <nav class="mobile-nav">${items.map(navItem)}<${ThemeButton} /><button class="btn ghost icon" onClick=${logout}>${icon('logout')}</button></nav>
      <div class="topbar"><div><h1>${cur.label}</h1><div class="sub">${SUBTITLE[cur.id]}</div></div></div>
      <div class="content"><${Page} key=${cur.id} sub=${sub} role="server" me=${me} go=${go} /></div>
    </main>
  </div>`;
}

function App() {
  const [me, setMe] = useState(undefined); // undefined = loading, null = logged out
  const [bootErr, setBootErr] = useState('');
  const load = async () => {
    try {
      setMe(await api('GET', '/me'));
      setBootErr('');
    } catch (e) {
      if (e.status === 401) setMe(null); else setBootErr(e.message);
    }
  };
  useEffect(() => {
    setUnauthorizedHandler(() => setMe(null));
    load();
  }, []);
  let body;
  if (bootErr) body = html`<div class="auth-wrap"><div class="card auth-card"><${Alert} kind="err">${bootErr}<//><button class="btn" onClick=${load}>重试</button></div></div>`;
  else if (me === undefined) body = html`<div class="boot"><${Spinner} /></div>`;
  else if (me === null) body = html`<${Login} onLogin=${load} />`;
  else if (me.mustChange) body = html`<${ForcedPassword} me=${me} onDone=${load} />`;
  else body = html`<${Shell} me=${me} onLogout=${() => setMe(null)} />`;
  return html`${body}<${Toasts} />`;
}

render(html`<${App} />`, document.getElementById('app'));
