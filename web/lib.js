// Shared helpers and UI primitives.
import { html, render, useState, useEffect, useRef, useCallback, useMemo } from './vendor/preact-htm.js';

export { html, render, useState, useEffect, useRef, useCallback, useMemo };

// ---------- API ----------
export class ApiError extends Error {
  constructor(msg, status) { super(msg); this.status = status; }
}
let onUnauthorized = () => {};
export function setUnauthorizedHandler(fn) { onUnauthorized = fn; }

export async function api(method, path, body) {
  // X-FRP-Panel is required by the server on every non-GET request (CSRF).
  const opt = { method, headers: { 'X-FRP-Panel': '1' }, credentials: 'same-origin', cache: 'no-store' };
  if (body !== undefined) {
    opt.headers['Content-Type'] = 'application/json';
    opt.body = JSON.stringify(body);
  }
  let res;
  try {
    res = await fetch('/api' + path, opt);
  } catch (e) {
    throw new ApiError('无法连接面板：' + e.message, 0);
  }
  let data = null;
  try { data = await res.json(); } catch { /* empty body */ }
  if (!res.ok) {
    if (res.status === 401 && path !== '/login') onUnauthorized();
    throw new ApiError((data && data.error) || res.statusText, res.status);
  }
  return data;
}

// usePoll loads fn() now and every `ms` (0 = once); reload() forces a refresh.
export function usePoll(fn, ms = 0, deps = []) {
  const [state, setState] = useState({ data: null, error: null, loading: true });
  const fnRef = useRef(fn);
  fnRef.current = fn;
  const load = useCallback(async () => {
    try {
      const data = await fnRef.current();
      setState({ data, error: null, loading: false });
    } catch (e) {
      setState(s => ({ data: s.data, error: e, loading: false }));
    }
  }, []);
  useEffect(() => {
    load();
    if (!ms) return;
    const t = setInterval(() => { if (!document.hidden) load(); }, ms);
    return () => clearInterval(t);
  }, deps);
  return { ...state, reload: load };
}

// ---------- toasts ----------
const toastSubs = new Set();
let toasts = [];
let toastId = 0;
function emitToasts() { toastSubs.forEach(f => f(toasts)); }
export function toast(msg, kind = 'ok') {
  const id = ++toastId;
  toasts = [...toasts, { id, msg, kind }];
  emitToasts();
  setTimeout(() => { toasts = toasts.filter(t => t.id !== id); emitToasts(); }, kind === 'err' ? 8000 : 3500);
}
export function Toasts() {
  const [list, set] = useState(toasts);
  useEffect(() => { toastSubs.add(set); return () => toastSubs.delete(set); }, []);
  return html`<div class="toasts" role="status">
    ${list.map(t => html`<div class="toast ${t.kind}" key=${t.id}>${icon(t.kind === 'err' ? 'alert' : 'check')}<div>${t.msg}</div></div>`)}
  </div>`;
}

// Runs an async action with a busy flag and toast on success/failure.
export function useAction() {
  const [busy, setBusy] = useState(false);
  const run = useCallback(async (fn, okMsg) => {
    setBusy(true);
    try {
      const r = await fn();
      const msg = typeof okMsg === 'function' ? okMsg(r) : okMsg;
      if (msg) toast(msg);
      return r;
    } catch (e) {
      toast(e.message, 'err');
      return undefined;
    } finally {
      setBusy(false);
    }
  }, []);
  return [busy, run];
}

// ---------- icons (24px stroke icons) ----------
const P = {
  dashboard: 'M3 3h7v9H3zM14 3h7v5h-7zM14 12h7v9h-7zM3 16h7v5H3z',
  proxies: 'M8 3 4 7l4 4M4 7h16M16 21l4-4-4-4M20 17H4',
  clients: 'M4 3h16a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2zM8 21h8M12 17v4',
  settings: 'M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6',
  logs: 'm4 17 6-6-6-6M12 19h8',
  code: 'm16 18 6-6-6-6M8 6l-6 6 6 6',
  shield: 'M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10zM9 12l2 2 4-4',
  server: 'M4 2h16a2 2 0 0 1 2 2v4a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2zM4 14h16a2 2 0 0 1 2 2v4a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2v-4a2 2 0 0 1 2-2zM6 6h.01M6 18h.01',
  power: 'M12 2v10M18.4 6.6a9 9 0 1 1-12.77.04',
  refresh: 'M21 12a9 9 0 1 1-3-6.7L21 8M21 3v5h-5',
  plus: 'M12 5v14M5 12h14',
  edit: 'M17 3a2.8 2.8 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5Z',
  trash: 'M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6M10 11v6M14 11v6',
  copy: 'M11 9h9a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2h-9a2 2 0 0 1-2-2v-9a2 2 0 0 1 2-2zM5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1',
  logout: 'M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9',
  sun: 'M12 8a4 4 0 1 0 0 8 4 4 0 1 0 0-8zM12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4',
  moon: 'M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z',
  check: 'M20 6 9 17l-5-5',
  alert: 'M12 2a10 10 0 1 0 0 20 10 10 0 1 0 0-20zM12 8v4M12 16h.01',
  info: 'M12 2a10 10 0 1 0 0 20 10 10 0 1 0 0-20zM12 16v-4M12 8h.01',
  lock: 'M5 11h14a2 2 0 0 1 2 2v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-7a2 2 0 0 1 2-2zM7 11V7a5 5 0 0 1 10 0v4',
  arrow: 'M5 12h14M12 5l7 7-7 7',
  activity: 'M22 12h-4l-3 9L9 3l-3 9H2',
  link: 'M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71',
  download: 'M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3',
  eye: 'M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12zM12 9a3 3 0 1 0 0 6 3 3 0 1 0 0-6z',
  eyeOff: 'M9.9 4.2A9 9 0 0 1 12 4c6.5 0 10 8 10 8a17 17 0 0 1-2.2 3.2M6.6 6.6C3.9 8.3 2 12 2 12s3.5 7 10 7a9.7 9.7 0 0 0 5.4-1.6M2 2l20 20M9.9 9.9a3 3 0 0 0 4.2 4.2',
  key: 'M15.5 7.5 19 4M21 2l-9.6 9.6M15.5 7.5l3 3L22 7l-3-3M7.5 13a5.5 5.5 0 1 0 0 11 5.5 5.5 0 1 0 0-11z',
  play: 'M6 4l14 8-14 8z',
  stop: 'M6 6h12v12H6z',
  loader: 'M21 12a9 9 0 1 1-6.22-8.56',
  globe: 'M12 2a10 10 0 1 0 0 20 10 10 0 1 0 0-20zM2 12h20M12 2a15 15 0 0 1 0 20M12 2a15 15 0 0 0 0 20',
  zap: 'M13 2 3 14h9l-1 8 10-12h-9z',
  inbox: 'M22 12h-6l-2 3h-4l-2-3H2M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z',
};
export function icon(name, cls = '') {
  return html`<svg class=${cls} viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d=${P[name] || P.info} /></svg>`;
}

// ---------- formatting ----------
export function fmtBytes(n) {
  if (!n || n < 0) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return `${n < 10 && i ? n.toFixed(2) : n < 100 && i ? n.toFixed(1) : Math.round(n)} ${u[i]}`;
}
export const fmtRate = n => fmtBytes(n) + '/s';
export function fmtDuration(sec) {
  if (sec == null || sec < 0) return '-';
  const d = Math.floor(sec / 86400), h = Math.floor(sec % 86400 / 3600), m = Math.floor(sec % 3600 / 60);
  if (d) return `${d} 天 ${h} 小时`;
  if (h) return `${h} 小时 ${m} 分`;
  if (m) return `${m} 分钟`;
  return `${Math.floor(sec)} 秒`;
}
export function since(iso) {
  if (!iso || iso.startsWith('0001')) return '-';
  return fmtDuration((Date.now() - new Date(iso).getTime()) / 1000);
}
export function fmtTime(unix) {
  if (!unix) return '-';
  return new Date(unix * 1000).toLocaleString('zh-CN', { hour12: false });
}

export async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    toast('已复制到剪贴板');
  } catch {
    toast('复制失败，请手动选择复制', 'err');
  }
}

// Random secret from an unambiguous alphabet, using the browser CSPRNG.
export function randomSecret(n = 24) {
  const a = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789';
  const buf = new Uint32Array(n);
  crypto.getRandomValues(buf);
  return Array.from(buf, x => a[x % a.length]).join('');
}

// ---------- components ----------
export function Modal({ title, onClose, children, footer, wide }) {
  useEffect(() => {
    const k = e => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', k);
    return () => window.removeEventListener('keydown', k);
  }, [onClose]);
  return html`<div class="overlay" onMouseDown=${e => { if (e.target === e.currentTarget) onClose(); }}>
    <div class="modal" style=${wide ? 'width:min(760px,100%)' : ''} role="dialog" aria-modal="true">
      <div class="modal-head"><h3>${title}</h3><button class="btn ghost icon" onClick=${onClose} aria-label="关闭">✕</button></div>
      <div class="modal-body">${children}</div>
      ${footer && html`<div class="modal-foot">${footer}</div>`}
    </div>
  </div>`;
}

export function Field({ label, help, children }) {
  return html`<div class="field">${label && html`<label>${label}</label>`}${children}${help && html`<div class="help">${help}</div>`}</div>`;
}

export function TextInput({ value, onInput, ...rest }) {
  return html`<input class="input" value=${value ?? ''} onInput=${e => onInput(e.target.value)} ...${rest} />`;
}

export function NumInput({ value, onInput, ...rest }) {
  return html`<input class="input" type="number" min="0" max="65535" value=${value || value === 0 ? value : ''}
    onInput=${e => onInput(e.target.value === '' ? 0 : parseInt(e.target.value, 10) || 0)} ...${rest} />`;
}

export function SecretInput({ value, onInput, generate, placeholder }) {
  const [show, setShow] = useState(false);
  return html`<div class="input-group">
    <input class="input mono" type=${show ? 'text' : 'password'} value=${value ?? ''} placeholder=${placeholder}
      autocomplete="new-password" spellcheck="false" onInput=${e => onInput(e.target.value)} />
    <button type="button" class="btn icon" title=${show ? '隐藏' : '显示'} onClick=${() => setShow(!show)}>${icon(show ? 'eyeOff' : 'eye')}</button>
    ${value && html`<button type="button" class="btn icon" title="复制" onClick=${() => copyText(value)}>${icon('copy')}</button>`}
    ${generate && html`<button type="button" class="btn icon" title="随机生成" onClick=${() => { onInput(randomSecret(generate)); setShow(true); }}>${icon('key')}</button>`}
  </div>`;
}

export function Check({ checked, onChange, label, hint }) {
  return html`<label class="check"><input type="checkbox" checked=${!!checked} onChange=${e => onChange(e.target.checked)} />
    <span>${label}${hint && html`<small>${hint}</small>`}</span></label>`;
}

export function Seg({ value, options, onChange }) {
  return html`<div class="seg">${options.map(o => html`<button type="button" class=${o.value === value ? 'on' : ''} onClick=${() => onChange(o.value)}>${o.label}</button>`)}</div>`;
}

export function Alert({ kind = 'info', children }) {
  const ic = { info: 'info', warn: 'alert', err: 'alert', ok: 'check' }[kind];
  return html`<div class="alert ${kind}">${icon(ic)}<div>${children}</div></div>`;
}

export function CodeBox({ text }) {
  return html`<div class="codebox">${text}<button class="btn sm icon copy" title="复制" onClick=${() => copyText(text)}>${icon('copy')}</button></div>`;
}

export function Empty({ text, children }) {
  return html`<div class="empty">${icon('inbox')}<div>${text}</div>${children && html`<div class="mt">${children}</div>`}</div>`;
}

export function Spinner() {
  return html`<span class="muted">${icon('loader', 'spin')}</span>`;
}

export function ServiceBadge({ s }) {
  if (!s) return html`<span class="badge">未知</span>`;
  if (s.running) return html`<span class="badge ok"><span class="dot ok"></span>运行中</span>`;
  if ((s.state || '').startsWith('activating')) return html`<span class="badge warn"><span class="dot warn"></span>启动中</span>`;
  if ((s.state || '').startsWith('failed')) return html`<span class="badge err"><span class="dot err"></span>已崩溃</span>`;
  return html`<span class="badge"><span class="dot"></span>已停止</span>`;
}

export function Stat({ label, value, unit, hint, ic }) {
  return html`<div class="card stat">
    <div class="label">${label}</div>
    <div class="value">${value}${unit && html`<small>${unit}</small>`}</div>
    ${hint && html`<div class="hint">${hint}</div>`}
    ${ic && html`<div class="icon">${icon(ic)}</div>`}
  </div>`;
}

export function confirmDialog(msg) { return window.confirm(msg); }
