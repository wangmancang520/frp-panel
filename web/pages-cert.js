// HTTPS certificates: which certificate visitors get, Let's Encrypt for the
// panel's domain (with condition checks), uploaded certificates and the
// self-signed certificate that IP access and managed clients always use.
import {
  html, useState, useEffect, api, usePoll, useAction, icon, copyText,
  Field, TextInput, Seg, Alert, Spinner,
} from './lib.js';

const isIP = h => /^[\d.]+$/.test(h) || h.includes(':');
const date = s => s && !s.startsWith('0001') ? new Date(s).toLocaleDateString('zh-CN') : '-';
const daysLeft = s => Math.floor((new Date(s).getTime() - Date.now()) / 86400000);

function Expiry({ notAfter }) {
  if (!notAfter || notAfter.startsWith('0001')) return html`<span class="muted">-</span>`;
  const d = daysLeft(notAfter);
  const kind = d < 0 ? 'err' : d < 14 ? 'warn' : 'ok';
  return html`<span>${date(notAfter)} <span class="badge ${kind}">${d < 0 ? '已过期' : `剩余 ${d} 天`}</span></span>`;
}

const CHECK_ICON = { ok: 'check', warn: 'alert', fail: 'alert', info: 'info' };
const CHECK_KIND = { ok: 'ok', warn: 'warn', fail: 'err', info: '' };

function Checks({ list }) {
  return html`<div class="checks">${list.map(c => html`<div class="check-row">
    <span class="badge ${CHECK_KIND[c.status]}">${icon(CHECK_ICON[c.status])}${c.label}</span>
    <span class="small">${c.detail}</span>
  </div>`)}</div>`;
}

// What a visitor gets: by IP always the self-signed certificate; by domain
// whichever certificate mode is configured.
function InUse({ t }) {
  const domainCert = t.mode === 'acme' ? t.acme : t.mode === 'custom' ? t.custom : null;
  const label = { acme: "Let's Encrypt", custom: '上传的证书', selfsigned: '自签名证书' }[t.mode] || t.mode;
  return html`<div class="card">
    <h2>当前使用的证书</h2>
    <p class="desc">面板只提供 HTTPS。用 IP 访问时始终使用自签名证书（受管客户端也通过它连接，不受这里的设置影响）；用域名访问时使用下面配置的证书。</p>
    <div class="table-wrap"><table>
      <thead><tr><th>访问方式</th><th>证书</th><th>到期</th></tr></thead>
      <tbody>
        <tr><td>用 IP 访问</td><td>自签名证书 <span class="muted small">（浏览器会提示不受信任）</span></td><td><${Expiry} notAfter=${t.selfSigned.notAfter} /></td></tr>
        <tr><td>用域名访问${t.domain ? html` <span class="mono small">${t.domain}</span>` : ''}</td>
          <td>${t.mode === 'selfsigned' ? html`<span class="muted">未配置域名证书，使用自签名证书</span>`
            : domainCert ? html`${label} <span class="muted small">${(domainCert.issuer || '').replace(/^CN=/, '').split(',')[0]}</span>`
            : html`${label} <span class="badge warn">${t.acmeIssuing ? '申请中' : '尚未取得'}</span>`}</td>
          <td>${domainCert ? html`<${Expiry} notAfter=${domainCert.notAfter} />` : '-'}</td></tr>
      </tbody>
    </table></div>
  </div>`;
}

function AcmeCard({ t, onChange }) {
  const here = location.hostname;
  const [domain, setDomain] = useState(t.domain || (!isIP(here) && here !== 'localhost' ? here : ''));
  const [email, setEmail] = useState(t.email || '');
  const [checks, setChecks] = useState(null);
  const [result, setResult] = useState(null);
  const [busy, run] = useAction();
  const [issuing, setIssuing] = useState(false);
  const check = () => run(async () => {
    setChecks(null);
    setChecks((await api('POST', '/tls/check', { domain })).checks);
  });
  const issue = () => run(async () => {
    setResult(null);
    setIssuing(true);
    try {
      const r = await api('POST', '/tls/issue', { domain, email });
      setResult(r);
      onChange();
      return r;
    } finally { setIssuing(false); }
  }, r => r && !r.issueError && '证书已取得');
  const ok = t.mode === 'acme' && t.acme && t.domain === domain && !t.acmeError;
  const err = result ? result.issueError : t.mode === 'acme' && t.acmeError;
  const port = t.panelPort || location.port;
  const url = 'https://' + domain + (port && port !== '443' ? ':' + port : '');
  const hint = result ? result.hint : '';
  return html`<div class="card">
    <h2>Let's Encrypt 免费证书</h2>
    <p class="desc">为面板的域名自动申请受信任的证书，到期前自动续期。需要：域名解析到本机，并且外网可以访问本机的 80 端口（用于验证域名）。</p>
    <div class="fields">
      <${Field} label="域名"><${TextInput} value=${domain} onInput=${setDomain} placeholder="panel.example.com" /><//>
      <${Field} label="邮箱（可选）" help="用于接收 Let's Encrypt 的到期提醒"><${TextInput} value=${email} onInput=${setEmail} placeholder="you@example.com" /><//>
    </div>
    <div class="row">
      <button class="btn" disabled=${busy || !domain} onClick=${check}>${icon('activity')}检查条件</button>
      <button class="btn primary" disabled=${busy || !domain} onClick=${issue}>${icon(issuing ? 'loader' : 'shield', issuing ? 'spin' : '')}${issuing ? '正在申请…' : ok ? '重新检查并续期' : '申请证书'}</button>
    </div>
    ${issuing && html`<div class="muted small mt">正在向 Let's Encrypt 申请，通常需要 10-60 秒，请不要关闭页面。</div>`}
    ${checks && html`<div class="mt"><${Checks} list=${checks} /></div>`}
    ${result && !result.issueError && html`<div class="mt"><${Alert} kind="ok">证书已取得。用 <a href=${url}>${url}</a> 访问就不会再有浏览器警告（如果当前页面就是这个地址，刷新即可）。<//></div>`}
    ${err && html`<div class="mt"><${Alert} kind=${result && result.pending ? 'warn' : 'err'}>
      ${hint && html`<div><b>${hint}</b></div>`}
      <div class="small mono" style="white-space:pre-wrap;word-break:break-all;margin-top:4px">${err}</div>
    <//></div>`}
  </div>`;
}

function CustomCard({ t, onChange }) {
  const [c, setC] = useState({ cert: '', key: '' });
  const [busy, run] = useAction();
  const save = () => run(async () => { await api('POST', '/tls/custom', c); setC({ cert: '', key: '' }); onChange(); }, '证书已上传并启用');
  return html`<div class="card">
    <h2>上传证书</h2>
    <p class="desc">已经有证书（例如通配符证书，或 80 端口无法使用）时，可以直接上传。上传后用任何域名访问都使用这张证书。</p>
    ${t.custom && html`<div class="muted small" style="margin-bottom:10px">当前上传的证书：${(t.custom.dnsNames || []).join(', ')}，到期 ${date(t.custom.notAfter)}${t.mode !== 'custom' ? '（未启用）' : ''}</div>`}
    <${Field} label="证书（PEM，含中间证书链）"><textarea class="input" rows="5" value=${c.cert} onInput=${e => setC({ ...c, cert: e.target.value })} placeholder="-----BEGIN CERTIFICATE-----"></textarea><//>
    <${Field} label="私钥（PEM）"><textarea class="input" rows="4" value=${c.key} onInput=${e => setC({ ...c, key: e.target.value })} placeholder="-----BEGIN PRIVATE KEY-----"></textarea><//>
    <button class="btn primary" disabled=${busy || !c.cert || !c.key} onClick=${save}>上传并启用</button>
  </div>`;
}

function SelfSignedCard({ t, onChange }) {
  const self = t.selfSigned;
  const [sans, setSans] = useState((t.extraSANs || []).join(', '));
  const [busy, run] = useAction();
  const regen = () => run(async () => {
    if (!confirm('重新生成自签名证书后，浏览器会再次提示证书不受信任，需要重新确认指纹。私钥保持不变，已接入的客户端不受影响。继续？')) return;
    await api('POST', '/tls/regenerate', { extraSANs: sans.split(/[\s,，]+/).filter(Boolean) });
    onChange();
  }, '证书已重新生成，刷新页面后生效');
  const off = () => run(async () => {
    if (!confirm('停用域名证书？用域名访问时也会改用自签名证书。')) return;
    await api('PUT', '/tls', { mode: 'selfsigned', domain: '', email: '' });
    onChange();
  }, '已改回自签名证书');
  return html`<div class="card">
    <div class="card-head"><h2>自签名证书</h2><div class="grow"></div>
      ${t.mode !== 'selfsigned' && html`<button class="btn sm" disabled=${busy} onClick=${off}>停用域名证书</button>`}</div>
    <p class="desc">安装时自动生成，用于 IP 访问和客户端连接。浏览器提示“不安全”时，请核对指纹与下面一致，以防中间人攻击。</p>
    <dl class="kv">
      <dt>SHA-256 指纹</dt><dd class="fp">${self.fingerprint} <button class="btn ghost sm icon" onClick=${() => copyText(self.fingerprint)}>${icon('copy')}</button></dd>
      <dt>包含的 IP</dt><dd class="mono small">${(self.ips || []).join(', ')}</dd>
      <dt>包含的域名</dt><dd class="mono small">${(self.dnsNames || []).join(', ')}</dd>
      <dt>有效期至</dt><dd>${date(self.notAfter)}</dd>
    </dl>
    <${Field} label="额外的 IP / 域名" help="例如 NAT 后的公网 IP，多个用逗号分隔。修改后需重新生成。">
      <${TextInput} value=${sans} onInput=${setSans} placeholder="1.2.3.4, panel.example.com" /><//>
    <button class="btn" disabled=${busy} onClick=${regen}>${icon('refresh')}重新生成自签名证书</button>
  </div>`;
}

export function CertPage() {
  const tls = usePoll(() => api('GET', '/tls'), 0);
  const t = tls.data;
  // While Let's Encrypt is working in the background, refresh until it's done.
  useEffect(() => {
    if (!t || !t.acmeIssuing) return;
    const id = setTimeout(tls.reload, 4000);
    return () => clearTimeout(id);
  }, [t]);
  if (!t) return html`<div class="card">${tls.error ? html`<${Alert} kind="err">${tls.error.message}<//>` : html`<${Spinner} />`}</div>`;
  const here = location.hostname;
  const untrustedDomain = !isIP(here) && here !== 'localhost' && !(t.mode === 'acme' && t.acme && t.domain === here) && t.mode !== 'custom';
  return html`<div>
    ${untrustedDomain && html`<${Alert} kind="warn">你正在通过域名 <b>${here}</b> 访问面板，但它还没有受信任的证书。在下方「Let's Encrypt 免费证书」中检查条件并申请即可。<//>`}
    <${InUse} t=${t} />
    <div class="grid two mt">
      <${AcmeCard} t=${t} onChange=${tls.reload} />
      <${CustomCard} t=${t} onChange=${tls.reload} />
    </div>
    <div class="mt"><${SelfSignedCard} t=${t} onChange=${tls.reload} /></div>
  </div>`;
}
