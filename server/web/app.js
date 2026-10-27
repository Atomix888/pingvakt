/* PingVakt-server – webbgränssnitt */
'use strict';

/* ---------------- språk ---------------- */
// Källtexterna är engelska; window.PV_DICT (från i18n.js) innehåller översättningen.
function T(s, ...a) {
  const d = window.PV_DICT || {};
  let r = d[s];
  if (r == null) r = String(s).replace(/\|[a-z]{1,12}$/, '');
  return a.length ? r.replace(/\{(\d+)\}/g, (m, i) => a[i] == null ? '' : a[i]) : r;
}
// TE översätter meddelanden från servern/motorn, även när de har en dynamisk del på slutet.
function TE(msg) {
  msg = String(msg == null ? '' : msg);
  const d = window.PV_DICT || {};
  if (d[msg] != null) return d[msg];
  let best = '';
  for (const k in d) if (k.length > best.length && /[ :(]$/.test(k) && msg.startsWith(k)) best = k;
  return best ? d[best] + msg.slice(best.length) : msg;
}
const LANG = () => window.PV_LANG || 'en';

const $ = sel => document.querySelector(sel);
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  if (attrs) for (const [k, v] of Object.entries(attrs)) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'style') el.style.cssText = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'value') el.value = v;
    else if (k === 'checked') el.checked = !!v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const k of kids.flat()) if (k != null && k !== false) el.append(k.nodeType ? k : document.createTextNode(k));
  return el;
}

/* ---------------- API ---------------- */
async function api(method, path, body) {
  const opt = { method, headers: {}, credentials: 'same-origin' };
  if (body !== undefined) { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
  const res = await fetch(path, opt);
  let data = null;
  try { data = await res.json(); } catch (e) { /* tomt svar */ }
  if (!res.ok) {
    const err = new Error(TE((data && data.error) || `HTTP ${res.status}`));
    err.status = res.status; err.data = data;
    if (res.status === 401 && path !== '/api/me' && path !== '/api/login') showLogin();
    throw err;
  }
  return data;
}
const GET = p => api('GET', p);
const POST = (p, b) => api('POST', p, b === undefined ? {} : b);
const PUT = (p, b) => api('PUT', p, b);
const DEL = p => api('DELETE', p, {});

/* ---------------- tillstånd ---------------- */
const S = {
  me: null, customers: [], sites: [], agents: [], targets: [], rules: [], stats: {}, live: {}, active: {},
  monitor: {}, reportSec: 5, serverUrl: '', version: '',
  spark: {}, selected: null, range: 3600, chartData: null, chartMode: 'raw', zoomed: false, connected: false,
};
const SPARK_MAX = 400;
const STATUS_TEXT = { up: T('Up'), warn: T('Warning'), down: T('Down'), paused: T('Paused'), pending: T('Pending'), offline: 'Agent offline' };
const RANGES = [[900, '15 min'], [3600, '1 h'], [21600, '6 h'], [86400, '24 h'], [604800, '7 d'], [2592000, '30 d']];
const INTERVALS = [500, 1000, 2000, 5000, 10000, 30000, 60000, 300000];
const isAdmin = () => S.me && S.me.role === 'admin';
const byId = (list, id) => list.find(x => x.id === id);
const siteOf = id => byId(S.sites, id);
const agentOf = id => byId(S.agents, id);
const customerOfAgent = a => a && (siteOf(a.siteId) || {}).customerId;
const customerOfTarget = t => customerOfAgent(agentOf(t.agentId));

async function loadState() {
  const st = await GET('/api/state');
  Object.assign(S, {
    me: st.me, customers: st.customers, sites: st.sites, agents: st.agents, targets: st.targets, rules: st.rules,
    stats: st.stats || {}, live: st.live || {}, active: st.active || {}, monitor: st.monitor, reportSec: st.reportSec,
    serverUrl: st.serverUrl, version: st.version, serverHost: st.server || null, maint: st.maint || {}, acks: st.acks || {},
  });
}

/* ---------------- start ---------------- */
window.addEventListener('DOMContentLoaded', start);
async function start() {
  try { S.me = await GET('/api/me'); }
  catch (e) { if (e.status === 401) return e.data && e.data.setup ? showSetup() : showLogin(); throw e; }
  if (S.me.mfaRequired) return showMfaSetup();
  await boot();
}

// afterSignIn hämtar användaren igen (med krav på tvåstegsinloggning) och startar.
async function afterSignIn() {
  S.me = await GET('/api/me');
  location.hash = '';
  if (S.me.mfaRequired) return showMfaSetup();
  await boot();
}

// showMfaSetup visas när administratören kräver tvåstegsinloggning och användaren saknar den.
async function showMfaSetup() {
  const err = h('div', { class: 'form-error' });
  let setup;
  try { setup = await POST('/api/me/totp/setup'); } catch (e) { err.textContent = e.message; }
  const code = h('input', { type: 'text', inputmode: 'numeric', autocomplete: 'one-time-code', placeholder: '123456', style: 'text-align:center;font-size:20px;letter-spacing:4px' });
  const form = h('form', { onsubmit: async e => {
    e.preventDefault(); err.textContent = '';
    try {
      const r = await POST('/api/me/totp/enable', { code: code.value });
      authShell(T('Save your backup codes'), T('If you lose your phone you can sign in with one of these codes (each code works once). Keep them somewhere safe – they are only shown now.'),
        h('div', { class: 'auth-form' }, copyBox(r.recovery.join('  ')), h('button', { class: 'btn primary', onclick: () => location.reload() }, T('Continue'))));
    } catch (x) { err.textContent = x.message; }
  } },
    h('p', { class: 'muted', style: 'margin:0' }, T('Scan the QR code with an authenticator app (e.g. Microsoft Authenticator, Google Authenticator, 1Password or Bitwarden) and enter the code shown.')),
    setup ? h('img', { src: setup.qr, width: 200, height: 200, alt: T('QR code'), style: 'background:#fff;border-radius:8px;padding:6px;align-self:center' }) : null,
    setup ? h('code', { style: 'word-break:break-all;text-align:center' }, setup.secret) : null,
    code, err, h('button', { class: 'btn primary', type: 'submit' }, T('Enable')),
    h('button', { class: 'btn ghost', type: 'button', onclick: async () => { await POST('/api/logout'); location.reload(); } }, T('Sign out')));
  authShell(T('Two-factor authentication'), T('Your administrator requires two-factor authentication. Turn it on to continue.'), form);
  setTimeout(() => code.focus(), 30);
}

async function boot() {
  if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js', { scope: '/' }).catch(() => {});
  await loadState();
  renderShell();
  connectLive();
  window.addEventListener('hashchange', route);
  route();
  setInterval(() => { if (!S.selected) refreshTexts(); }, 15000);
}

/* ---------------- inloggning ---------------- */
function authShell(title, sub, form) {
  document.body.className = 'srv';
  $('#app').replaceChildren(h('div', { class: 'auth-wrap' }, h('div', { class: 'auth' },
    h('div', { class: 'brand' }, logo(), h('span', null, 'PingVakt')), h('p', null, sub), form)));
}
function logo() {
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg'); svg.setAttribute('viewBox', '0 0 32 32');
  svg.innerHTML = '<rect width="32" height="32" rx="8" fill="var(--brand-bg)"/><path d="M5 17h5l2.5-6 4 12 3-9 2 3H27" fill="none" stroke="var(--accent)" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"/>';
  return svg;
}
function showLogin() {
  if (S.es) { S.es.close(); S.es = null; }
  const email = h('input', { type: 'email', placeholder: T('Email'), autocomplete: 'username' });
  const pw = h('input', { type: 'password', placeholder: T('Password'), autocomplete: 'current-password' });
  const err = h('div', { class: 'form-error' });
  const form = h('form', { onsubmit: async e => {
    e.preventDefault(); err.textContent = '';
    try {
      const r = await POST('/api/login', { email: email.value, password: pw.value });
      if (r.needTotp) return showTotpLogin(r.ticket);
      await afterSignIn();
    }
    catch (x) { err.textContent = x.message; }
  } }, email, pw, err, h('button', { class: 'btn primary', type: 'submit' }, T('Sign in')));
  const sso = h('div', { class: 'sso' });
  form.append(sso);
  authShell(T('Sign in'), T('Sign in to view monitoring'), form);
  const m = /ssoerr=([^&]*)/.exec(location.hash);
  if (m) { err.textContent = TE(decodeURIComponent(m[1])); history.replaceState(null, '', location.pathname); }
  GET('/api/auth/providers').then(p => {
    const btns = [];
    if (p.microsoft) btns.push(h('a', { class: 'btn sso-btn', href: '/auth/microsoft/login' }, msLogo(), T('Sign in with Microsoft')));
    if (p.google) btns.push(h('a', { class: 'btn sso-btn', href: '/auth/google/login' }, gLogo(), T('Sign in with Google')));
    if (btns.length) sso.replaceChildren(h('div', { class: 'sso-or' }, h('span', null, T('or'))), ...btns);
  }).catch(() => {});
  setTimeout(() => email.focus(), 30);
}
function msLogo() {
  const s = document.createElementNS('http://www.w3.org/2000/svg', 'svg'); s.setAttribute('viewBox', '0 0 21 21'); s.setAttribute('width', '16'); s.setAttribute('height', '16');
  s.innerHTML = '<rect x="1" y="1" width="9" height="9" fill="#f25022"/><rect x="11" y="1" width="9" height="9" fill="#7fba00"/><rect x="1" y="11" width="9" height="9" fill="#00a4ef"/><rect x="11" y="11" width="9" height="9" fill="#ffb900"/>';
  return s;
}
function gLogo() {
  const s = document.createElementNS('http://www.w3.org/2000/svg', 'svg'); s.setAttribute('viewBox', '0 0 48 48'); s.setAttribute('width', '16'); s.setAttribute('height', '16');
  s.innerHTML = '<path fill="#FFC107" d="M43.6 20.5H42V20H24v8h11.3C33.7 32.7 29.2 36 24 36c-6.6 0-12-5.4-12-12s5.4-12 12-12c3.1 0 5.8 1.2 7.9 3.1l5.7-5.7C34 6.1 29.3 4 24 4 12.9 4 4 12.9 4 24s8.9 20 20 20 20-8.9 20-20c0-1.3-.1-2.4-.4-3.5z"/><path fill="#FF3D00" d="M6.3 14.7l6.6 4.8C14.7 15.1 19 12 24 12c3.1 0 5.8 1.2 7.9 3.1l5.7-5.7C34 6.1 29.3 4 24 4 16.3 4 9.7 8.3 6.3 14.7z"/><path fill="#4CAF50" d="M24 44c5.2 0 9.9-2 13.4-5.2l-6.2-5.2C29.2 35.1 26.7 36 24 36c-5.2 0-9.6-3.3-11.3-7.9l-6.5 5C9.5 39.6 16.2 44 24 44z"/><path fill="#1976D2" d="M43.6 20.5H42V20H24v8h11.3c-.8 2.2-2.2 4.2-4.1 5.6l6.2 5.2C37 39.2 44 34 44 24c0-1.3-.1-2.4-.4-3.5z"/>';
  return s;
}
function showTotpLogin(ticket) {
  const code = h('input', { type: 'text', inputmode: 'numeric', autocomplete: 'one-time-code', placeholder: '123456', style: 'text-align:center;font-size:20px;letter-spacing:4px' });
  const err = h('div', { class: 'form-error' });
  const form = h('form', { onsubmit: async e => {
    e.preventDefault(); err.textContent = '';
    try { await POST('/api/login/totp', { ticket, code: code.value }); await afterSignIn(); }
    catch (x) { err.textContent = x.message; if (x.status === 401 && /start over|börja om/.test(x.message)) setTimeout(showLogin, 1500); }
  } }, code, err, h('button', { class: 'btn primary', type: 'submit' }, T('Sign in')),
    h('small', { class: 'muted', style: 'text-align:center' }, T('Enter the code from your authenticator app, or a backup code (xxxx-xxxx).')));
  authShell(T('Two-factor authentication'), T('Enter the code from your authenticator app'), form);
  setTimeout(() => code.focus(), 30);
}

function showSetup() {
  const name = h('input', { type: 'text', placeholder: T('Your name') });
  const email = h('input', { type: 'email', placeholder: T('Email'), autocomplete: 'username' });
  const pw = h('input', { type: 'password', placeholder: T('Password (at least 8 characters)'), autocomplete: 'new-password' });
  const err = h('div', { class: 'form-error' });
  const form = h('form', { onsubmit: async e => {
    e.preventDefault(); err.textContent = '';
    try { S.me = await POST('/api/setup', { name: name.value, email: email.value, password: pw.value }); await boot(); }
    catch (x) { err.textContent = x.message; }
  } }, name, email, pw, err, h('button', { class: 'btn primary', type: 'submit' }, T('Create administrator')));
  authShell(T('Get started'), T('Create the first administrator account'), form);
  setTimeout(() => name.focus(), 30);
}

/* ---------------- ram ---------------- */
function renderShell() {
  document.body.className = 'srv';
  $('#app').replaceChildren(
    h('aside', { class: 'side' },
      h('div', { class: 'brand' }, logo(), h('span', null, 'PingVakt')),
      h('nav', { id: 'nav' }),
      h('div', { class: 'me' },
        h('div', { class: 'who' }, h('b', null, S.me.name || S.me.email), h('small', null, isAdmin() ? T('Administrator') : T('Customer (read-only)'))),
        h('button', { class: 'icon-btn', title: T('Account'), onclick: e => showMenu(e.currentTarget, [
          { label: T('Change password…'), action: passwordDialog },
          { label: T('Notifications…'), action: notifyDialog },
          { label: 'Language / Språk…', action: langDialog },
          { label: S.me.totp ? T('Two-factor authentication (on)…') : T('Enable two-factor authentication…'), action: totpDialog },
          { label: T('TV mode'), action: () => { location.hash = '#/tv'; } },
          { label: T('Sign out'), action: async () => { await POST('/api/logout'); location.reload(); } },
        ]) }, '⋯'))),
    h('div', { class: 'navscrim', onclick: () => document.body.classList.remove('nav-open') }),
    h('div', { class: 'mainwrap' },
      h('header', { class: 'mtop' },
        h('button', { class: 'icon-btn mburger', title: T('Menu'), 'aria-label': T('Menu'), onclick: () => document.body.classList.toggle('nav-open') }, '☰'),
        h('div', { class: 'brand' }, logo(), h('span', null, 'PingVakt')),
        h('span', { class: 'mtop-st', id: 'mtopSt' })),
      h('div', { class: 'page', id: 'page' }),
      h('section', { id: 'drawer', class: 'drawer hidden' })));
  renderNav();
}

function renderNav() {
  const route = location.hash || '#/';
  const link = (href, label, extra) => h('a', { href, class: route === href || (href !== '#/' && route.startsWith(href)) ? 'on' : '' }, label, extra || null);
  const items = [link('#/', T('Overview'))];
  items.push(h('div', { class: 'navhead' }, T('Customers')));
  for (const c of S.customers) {
    const cnt = counts(S.targets.filter(t => customerOfTarget(t) === c.id));
    const st = cnt.down ? 'down' : cnt.offline ? 'offline' : cnt.warn ? 'warn' : 'up';
    const a = link('#/c/' + c.id, h('span', { class: 'dot st-' + st }), h('span', { class: 'cnt' }, String(cnt.total)));
    a.classList.add('custlink');
    a.insertBefore(document.createTextNode(c.name), a.lastChild);
    items.push(a);
  }
  if (isAdmin() && !S.customers.length) items.push(h('a', { href: '#/', class: 'custlink', onclick: e => { e.preventDefault(); customerDialog(); } }, T('+ New customer')));
  items.push(h('div', { class: 'navhead' }, T('Manage')));
  items.push(link('#/agents', T('Agents')));
  items.push(link('#/alerts', T('Alerts')));
  if (isAdmin()) { items.push(link('#/users', T('Users'))); items.push(link('#/audit', T('Audit log'))); items.push(link('#/settings', T('Settings'))); }
  items.push(link('#/tv', T('TV mode')));
  $('#nav').replaceChildren(...items);
  const all = counts(S.targets);
  const ms = $('#mtopSt');
  if (ms) ms.replaceChildren(...[all.down ? h('span', { class: 'chip st-down' }, h('span', { class: 'dot' }), String(all.down)) : null,
    all.warn ? h('span', { class: 'chip st-warn' }, h('span', { class: 'dot' }), String(all.warn)) : null,
    !all.down && !all.warn && all.total ? h('span', { class: 'chip st-up' }, h('span', { class: 'dot' }), String(all.up)) : null].filter(Boolean));
}
window.addEventListener('hashchange', () => document.body.classList.remove('nav-open'));

const plural = (n, one, many) => `${n} ${n === 1 ? one : many}`;

function counts(targets) {
  const c = { total: targets.length, up: 0, warn: 0, down: 0, offline: 0, paused: 0, pending: 0 };
  for (const t of targets) { const s = (S.stats[t.id] || {}).status || 'pending'; if (s in c) c[s]++; }
  return c;
}

/* ---------------- routing ---------------- */
function route() {
  closeDrawer();
  document.body.classList.toggle('tv', location.hash === '#/tv');
  renderNav();
  const r = location.hash.replace(/^#/, '') || '/';
  const m = r.match(/^\/c\/([\w-]+)/);
  S.view = m ? { name: 'customer', id: m[1] } : { name: r.replace(/^\//, '') || 'overview' };
  renderPage();
}

function renderPage() {
  tiles.clear();
  const v = S.view;
  const page = $('#page');
  if (v.name === 'customer') return renderCustomer(page, v.id);
  if (v.name === 'agents') return renderAgents(page);
  if (v.name === 'alerts') return renderAlerts(page);
  if (v.name === 'users' && isAdmin()) return renderUsers(page);
  if (v.name === 'settings' && isAdmin()) return renderSettings(page);
  if (v.name === 'audit' && isAdmin()) return renderAudit(page);
  if (v.name === 'tv') return renderTV(page);
  return renderOverview(page);
}

function connBadge() {
  return h('span', { class: 'conn', id: 'connBadge' }, h('span', { class: 'dot ' + (S.connected ? 'st-up' : 'st-offline') }), S.connected ? 'Live' : T('Connecting…'));
}

/* ---------------- översikt ---------------- */
function renderOverview(page) {
  const all = counts(S.targets);
  const head = h('div', { class: 'page-head' }, h('h1', null, T('Overview')), connBadge(),
    h('div', { class: 'tools' }, isAdmin() ? h('button', { class: 'btn primary', onclick: () => customerDialog() }, T('+ New customer')) : null));
  const chip = (k, label) => h('span', { class: 'chip st-' + k + (all[k] ? '' : ' zero'), title: label }, h('span', { class: 'dot' }), `${all[k]} ${label}`);
  const summary = h('div', { class: 'summary', style: 'margin-bottom:16px;flex-wrap:wrap' },
    chip('up', T('up')), chip('warn', T('warning')), chip('down', T('down')), chip('offline', 'offline'), chip('paused', T('paused')));
  const cards = h('div', { class: 'cards' }, ...S.customers.map(c => {
    const ts = S.targets.filter(t => customerOfTarget(t) === c.id);
    const cnt = counts(ts);
    const ags = S.agents.filter(a => customerOfAgent(a) === c.id);
    const on = ags.filter(a => (S.live[a.id] || {}).online).length;
    const st = cnt.down ? 'down' : cnt.offline ? 'offline' : cnt.warn ? 'warn' : 'up';
    const n = (k, cls) => h('span', { class: cnt[k] ? '' : 'zero' }, h('span', { class: 'dot st-' + cls }), String(cnt[k]));
    return h('div', { class: 'card st-' + st, onclick: () => { location.hash = '#/c/' + c.id; } },
      h('h3', null, c.name),
      h('div', { class: 'meta' }, T('{0} · {1}/{2} agents online · {3} targets', plural(S.sites.filter(s => s.customerId === c.id).length, T('site'), T('sites')), on, ags.length, ts.length)),
      h('div', { class: 'counts' }, n('up', 'up'), n('warn', 'warn'), n('down', 'down'), n('offline', 'offline')));
  }));
  const problems = S.targets.filter(t => ['down', 'warn', 'offline'].includes((S.stats[t.id] || {}).status))
    .sort((a, b) => ({ down: 0, offline: 1, warn: 2 })[S.stats[a.id].status] - ({ down: 0, offline: 1, warn: 2 })[S.stats[b.id].status]);
  const plist = h('div', { class: 'problems', id: 'problems' }, ...(problems.length ? problems.slice(0, 30).map(t => {
    const st = S.stats[t.id];
    const a = agentOf(t.agentId), site = a && siteOf(a.siteId), c = site && byId(S.customers, site.customerId);
    return h('div', { class: 'row st-' + st.status, onclick: () => { location.hash = '#/c/' + c.id; setTimeout(() => openDrawer(t.id), 50); } },
      h('span', { class: 'dot' }),
      h('div', { class: 'what' }, h('b', null, displayName(t)), h('small', null, `${c ? c.name : ''} · ${site ? site.name : ''} · ${t.host}`)),
      h('span', { class: 'pill' }, STATUS_TEXT[st.status]), h('span', { class: 'muted', style: 'min-width:90px;text-align:right' }, TE(st.err) || dur(Date.now() - st.since)));
  }) : [h('div', { class: 'empty-ok' }, S.targets.length ? T('No problems right now.') : T('No targets yet.'))]));
  const content = [head, summary];
  if (!S.customers.length) {
    content.push(h('div', { class: 'empty', style: 'display:block' }, h('h2', null, T('Welcome to the PingVakt server')),
      h('p', null, isAdmin() ? T('Start by creating a customer. Then add an agent on the customer\'s network and choose what it should monitor.') : T('No customers are linked to your account.')),
      isAdmin() ? h('div', { class: 'quick' }, h('button', { class: 'btn primary', onclick: () => customerDialog() }, T('+ Create the first customer'))) : null));
  } else {
    content.push(cards, h('div', { class: 'section' }, h('div', { class: 'section-head' }, h('h2', null, T('Right now'))), plist));
  }
  if (isAdmin() && S.serverHost) {
    const sh = S.serverHost;
    content.push(h('div', { class: 'section' }, h('div', { class: 'section-head' }, h('h2', null, T('Server'))),
      h('div', { class: 'card server-card', onclick: () => hostView('_server') },
        h('div', { class: 'meters' }, meter('CPU', sh.cpu, T('{0} of {1} cores', fmtPct(sh.cpu), sh.cpus)), meter(T('Memory'), memPct(sh), T('{0} of {1}', gb(sh.memUsed), gb(sh.memTotal))),
          ...(sh.disks || []).slice(0, 3).map(d => meter('Disk ' + d.m, pct(d), T('{0} free of {1}', gb(d.t - d.u), gb(d.t))))),
        h('div', { class: 'meta' }, T('PingVakt server {0} · uptime {1}', S.version, dur(sh.uptime * 1000))))));
  }
  page.replaceChildren(...content);
}

/* ---------------- kund ---------------- */
const tiles = new Map();
function renderCustomer(page, id) {
  const c = byId(S.customers, id);
  if (!c) { page.replaceChildren(h('p', { class: 'muted' }, T('The customer does not exist.'))); return; }
  const sites = S.sites.filter(s => s.customerId === id);
  const head = h('div', { class: 'page-head' },
    h('div', null, h('h1', null, c.name), c.notes ? h('div', { class: 'sub' }, c.notes) : null), connBadge(),
    h('div', { class: 'tools' }, h('button', { class: 'btn', onclick: () => reportDialog(c) }, T('Report')), isAdmin() ? [
      h('button', { class: 'btn primary', onclick: () => addTargetsDialog(id) }, T('+ Add targets')),
      h('button', { class: 'btn', onclick: () => templateDialog(id) }, T('From template…')),
      h('button', { class: 'btn', onclick: () => agentDialog(id) }, T('+ New agent')),
      h('button', { class: 'btn', onclick: () => siteDialog(id) }, T('+ New site')),
      h('button', { class: 'btn ghost', onclick: e => showMenu(e.currentTarget, [
        { label: T('Add SNMP device…'), action: () => snmpDialog(null, c.id) },
        { label: T('Add integration (UniFi, Meraki, Aruba)…'), action: () => integrationDialog(null, c.id) },
        '-',
        { label: T('Edit customer…'), action: () => customerDialog(c) },
        { label: T('Maintenance windows…'), action: () => maintDialog(c.id) },
        { label: T('Public status page…'), action: () => statusPageDialog(c) },
        { label: T('Alert rules for customer…'), action: () => { location.hash = '#/alerts'; } },
        '-',
        { label: T('Delete customer…'), danger: true, action: () => deleteCustomer(c) },
      ]) }, '⋯')] : null));
  const sections = sites.map(site => {
    const ags = S.agents.filter(a => a.siteId === site.id);
    const ts = S.targets.filter(t => ags.some(a => a.id === t.agentId));
    const grid = h('div', { class: 'grid inline' });
    for (const t of ts) { const el = buildTile(t); tiles.set(t.id, el); grid.append(el); updateTile(t); }
    const agentChips = h('div', { class: 'agents-row' }, ...ags.map(a => agentChip(a)),
      !ags.length ? h('span', { class: 'muted' }, isAdmin() ? T('No agent on this site yet. ') : T('No agent.')) : null,
      !ags.length && isAdmin() ? h('button', { class: 'btn sm', onclick: () => agentDialog(id, site.id) }, T('+ Add agent')) : null);
    return h('div', { class: 'section' },
      h('div', { class: 'section-head' }, h('h2', null, site.name), h('span', { class: 'muted' }, T('{0} targets', ts.length)),
        isAdmin() ? h('div', { class: 'tools' }, h('button', { class: 'btn ghost sm', onclick: e => showMenu(e.currentTarget, [
          { label: T('Add SNMP device…'), action: () => snmpDialog(null, id, (ags.find(a => a.kind !== 'desktop') || {}).id) },
          { label: T('Rename…'), action: () => siteDialog(id, site) },
          { label: T('Delete site…'), danger: true, action: () => deleteSite(site) },
        ]) }, '⋯')) : null),
      agentChips, snmpRow(site),
      ts.length ? grid : (ags.length ? h('p', { class: 'muted' }, isAdmin() ? T('No targets yet – click "Add targets".') : T('No targets.')) : null));
  });
  page.replaceChildren(head, integRow(id), ...sections);
  loadSNMP().then(refreshSnmpRows);
  requestAnimationFrame(() => { for (const t of S.targets) if (tiles.has(t.id)) updateTile(t); });
  // fyll minigraferna med de senaste mätpunkterna
  const since = Date.now() - SPARK_MAX * 1000;
  for (const t of S.targets) {
    if (!tiles.has(t.id) || (S.spark[t.id] && S.spark[t.id].length)) continue;
    GET(`/api/targets/${t.id}/history?from=${since}&to=${Date.now()}`).then(hs => {
      if (hs.mode !== 'raw') return;
      const pts = hs.t.map((x, i) => ({ t: x, r: hs.r[i] }));
      const cur = S.spark[t.id] || [];
      S.spark[t.id] = pts.concat(cur.filter(p => !pts.length || p.t > pts[pts.length - 1].t)).slice(-SPARK_MAX);
      updateTile(t);
    }).catch(() => {});
  }
}

function agentChip(a) {
  const lv = S.live[a.id] || {};
  const st = !a.lastSeen && !lv.lastSeen ? 'pending' : lv.online ? 'up' : 'offline';
  const seen = lv.lastSeen || a.lastSeen;
  const kind = a.kind === 'desktop' ? T('desktop app') : 'agent';
  const el = h('span', { class: 'agent-chip st-' + st, 'data-agent': a.id, title: `${a.hostname || ''} ${a.os || ''} ${a.version ? 'v' + a.version : ''} ${a.ip || ''}`.trim() },
    h('span', { class: 'dot' }), h('b', null, a.name),
    h('small', null, st === 'pending' ? T('waiting for first contact') : st === 'up' ? `${kind} · online` : `offline ${dur(Date.now() - seen)}`),
    lv.host && st === 'up' ? hostMini(lv.host) : null,
    isAdmin() ? h('button', { class: 'icon-btn', title: 'Agent', onclick: e => { e.stopPropagation(); agentMenu(a, e.currentTarget); } }, '⋯') : null);
  if (!S.maint[a.id] && Object.keys(S.active).some(k => k.endsWith('|' + a.id) && !S.acks[k])) el.classList.add('alerting');
  if (S.maint[a.id]) { el.classList.add('in-maint'); el.title = '🔧 ' + S.maint[a.id]; }
  if (lv.host || a.kind === 'agent') { el.classList.add('clickable'); el.addEventListener('click', () => hostView(a.id)); }
  el.dataset.sig = st + '|' + (S.maint[a.id] || '') + '|' + el.classList.contains('alerting') + '|' + (lv.host ? [Math.round(lv.host.cpu), Math.round(memPct(lv.host)), Math.round(diskPct(lv.host))].join(',') : '');
  return el;
}

function agentMenu(a, anchor) {
  showMenu(anchor, [
    { label: T('CPU, memory and disk…'), action: () => hostView(a.id) },
    { label: T('Install / show commands…'), action: () => installDialog(a, null) },
    { label: T('New token…'), action: async () => {
      if (!await confirmDialog(T('New token'), T('The old token for "{0}" stops working immediately. The agent must be re-enrolled with the new one.', a.name), T('Create new token'))) return;
      const r = await POST(`/api/agents/${a.id}/token`); await afterChange(); installDialog(a, r.token);
    } },
    isAdmin() && a.kind ? { label: T('Scan network…'), action: () => openScan(a) } : null,
    a.kind === 'agent' ? { label: T('Add targets from template…'), action: () => templateDialog(customerOfAgent(a), a.id) } : null,
    a.kind === 'agent' ? { label: T('Add SNMP device…'), action: () => snmpDialog(null, customerOfAgent(a), a.id) } : null,
    S.targets.some(t => t.agentId === a.id) ? { label: T('Save targets as template…'), action: () => saveAsTemplateDialog(a) } : null,
    { label: T('Rename / move…'), action: () => editAgentDialog(a) },
    '-',
    { label: T('Delete agent…'), danger: true, action: async () => {
      if (!await confirmDialog(T('Delete agent'), T('Delete "{0}" and all its targets and measurements?', a.name), T('Delete'))) return;
      await DEL('/api/agents/' + a.id); await afterChange();
    } },
  ]);
}

/* ---------------- rutor ---------------- */
function displayName(t) { return t.name || t.host; }
const KINDS = [['icmp', 'ICMP (ping)'], ['tcp', 'TCP (port)'], ['http', T('HTTP(S) (web page)')], ['dns', T('DNS (name lookup)')]];
function kindText(t) { return t.kind === 'tcp' ? `TCP ${t.port}` : t.kind === 'http' ? (/^http:/.test(t.host) ? 'HTTP' : 'HTTPS') : t.kind === 'dns' ? 'DNS' + (t.dnsServer ? ' @' + t.dnsServer : '') : 'ICMP'; }
function certText(d) { return d < 0 ? T('expired') : d + ' d'; }
function warnFor(t) { return t.warnMs > 0 ? t.warnMs : (S.monitor.warnMs || 0); }

function buildTile(t) {
  const el = h('div', { class: 'tile st-pending', 'data-id': t.id },
    h('div', { class: 'tile-head' }, h('span', { class: 'dot' }),
      h('div', { class: 'names' }, h('div', { class: 'name' }), h('div', { class: 'host' })),
      isAdmin() ? h('button', { class: 'icon-btn menu-btn', title: T('Menu') }, '⋯') : null),
    h('div', { class: 'tile-main' }, h('span', { class: 'lat' }, '–'), h('span', { class: 'unit' }, 'ms'), h('span', { class: 'state-text' })),
    h('canvas', { class: 'spark' }), h('div', { class: 'tile-stats' }));
  el.addEventListener('click', e => {
    const mb = e.target.closest('.menu-btn');
    if (mb) { e.stopPropagation(); targetMenu(t.id, mb); return; }
    openDrawer(t.id);
  });
  return el;
}

function targetMenu(id, anchor) {
  const t = byId(S.targets, id);
  if (!t) return;
  const items = [{ label: T('Show graph'), action: () => openDrawer(id) }, { label: T('Edit…'), action: () => editTargetDialog(id) }];
  if (t.source !== 'agent') {
    items.push({ label: t.paused ? T('Start') : T('Pause'), action: () => togglePause(t) });
    items.push('-', { label: T('Delete'), danger: true, action: async () => {
      if (!await confirmDialog(T('Delete target'), T('Delete {0} and its measurements?', displayName(t)), T('Delete'))) return;
      await DEL('/api/targets/' + id); await afterChange();
    } });
  }
  showMenu(anchor, items);
}

async function togglePause(t) {
  await PUT('/api/targets/' + t.id, Object.assign({}, t, { paused: !t.paused }));
  await afterChange();
}

function updateTile(t) {
  const el = tiles.get(t.id);
  if (!el) return;
  const st = S.stats[t.id] || { status: 'pending', last: -1 };
  const status = st.status || 'pending';
  el.className = el.className.replace(/\bst-\w+/g, '').trim() + ' st-' + status;
  const keys = Object.keys(S.active).filter(k => k.endsWith('|' + t.id));
  el.classList.toggle('alerting', keys.some(k => !S.acks[k]) && !maintNote(t));
  el.classList.toggle('acked', keys.length > 0 && keys.every(k => S.acks[k]));
  el.classList.toggle('in-maint', !!maintNote(t));
  el.classList.toggle('selected', S.selected === t.id);
  const nm = el.querySelector('.name');
  nm.textContent = displayName(t); nm.title = displayName(t);
  const a = agentOf(t.agentId);
  const addr = st.addr && st.addr !== t.host ? ` → ${st.addr}` : '';
  el.querySelector('.host').textContent = `${t.host}${addr} · ${kindText(t)}${a ? ' · ' + a.name : ''}`;
  const lat = el.querySelector('.lat');
  const lost = st.window > 0 && st.last < 0;
  lat.textContent = status === 'paused' || status === 'offline' ? '–' : st.window ? (lost ? T('lost') : fmtMs(st.last)) : '…';
  lat.classList.toggle('lost', lost && status !== 'offline');
  el.querySelector('.unit').style.visibility = (!st.window || lost || status === 'paused' || status === 'offline') ? 'hidden' : '';
  tileText(el, st);
  el.querySelector('.tile-stats').replaceChildren(...[
    h('span', null, h('i', null, T('avg')), fmtMs(st.avg)),
    h('span', null, h('i', null, 'max'), fmtMs(st.max)),
    h('span', null, h('i', null, 'jitter'), fmtMs(st.jitter)),
    h('span', { class: st.loss > 0 ? 'bad' : '' }, h('i', null, T('loss')), (st.loss || 0).toFixed(st.loss && st.loss < 10 ? 1 : 0) + '%'),
    t.kind === 'http' && st.httpStatus ? h('span', { class: st.httpStatus >= 400 ? 'bad' : '' }, h('i', null, 'HTTP'), String(st.httpStatus)) : null,
    t.kind === 'http' && st.certDays >= 0 ? h('span', { class: st.certDays < 14 ? 'bad' : '' }, h('i', null, 'cert'), certText(st.certDays)) : null].filter(Boolean));
  drawSpark(el.querySelector('canvas'), S.spark[t.id] || [], warnFor(t));
}

function tileText(el, st) {
  let txt;
  switch (st.status) {
    case 'up': txt = T('Up ') + dur(Date.now() - st.since); break;
    case 'down': txt = (TE(st.err) || T('Down')) + ' · ' + dur(Date.now() - st.since); break;
    case 'warn': txt = st.consecLost ? (TE(st.err) || T('Packet lost')) : st.loss > (S.monitor.warnLossPct || 0) ? T('Loss {0}%', st.loss) : T('High latency'); break;
    case 'paused': txt = T('Paused'); break;
    case 'offline': txt = T('Agent is offline'); break;
    default: txt = T('Waiting for data…');
  }
  const tid = el.dataset.id, t = byId(S.targets, tid);
  const mn = t && maintNote(t);
  if (mn) txt = '🔧 ' + mn;
  else if (t && t.dependsOn && st.status === 'down') {
    const p = byId(S.targets, t.dependsOn);
    if (p && (S.stats[p.id] || {}).status === 'down') txt = T('Behind ') + displayName(p) + T(' (down)');
  }
  const e = el.querySelector('.state-text');
  e.textContent = txt; e.title = TE(st.err) || txt;
}
function maintNote(t) { return S.maint[t.id] || S.maint[t.agentId] || ''; }

function refreshTexts() {
  for (const t of S.targets) { const el = tiles.get(t.id); if (el) tileText(el, S.stats[t.id] || {}); }
  document.querySelectorAll('.agent-chip').forEach(ch => { const a = agentOf(ch.dataset.agent); if (a) ch.replaceWith(agentChip(a)); });
}

function fmtMs(v) {
  if (v == null || isNaN(v) || v < 0) return '–';
  if (v < 1) return v.toFixed(2);
  if (v < 10) return v.toFixed(1);
  return Math.round(v).toString();
}
function dur(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return s + ' s';
  const m = Math.floor(s / 60);
  if (m < 60) return m + ' min';
  const hh = Math.floor(m / 60);
  if (hh < 48) return hh + ' h ' + (m % 60) + ' min';
  return Math.floor(hh / 24) + ' d ' + (hh % 24) + ' h';
}
const clock = ms => new Date(ms).toLocaleTimeString('sv-SE');
const dateTime = ms => new Date(ms).toLocaleString('sv-SE');

let cssCache = null;
function css() {
  if (!cssCache) {
    const cs = getComputedStyle(document.documentElement);
    const v = n => cs.getPropertyValue(n).trim();
    cssCache = { bar: v('--bar'), warn: v('--bar-warn'), lost: v('--bar-lost'), muted: v('--muted'), border: v('--border'), accent: v('--accent') };
  }
  return cssCache;
}
matchMedia('(prefers-color-scheme: light)').addEventListener('change', () => { cssCache = null; if (S.me) { renderPage(); rebuildChart(); } });

function drawSpark(cv, data, warnMs) {
  const dpr = window.devicePixelRatio || 1;
  const w = cv.clientWidth, hgt = cv.clientHeight;
  if (!w || !hgt) return;
  if (cv.width !== Math.round(w * dpr) || cv.height !== Math.round(hgt * dpr)) { cv.width = Math.round(w * dpr); cv.height = Math.round(hgt * dpr); }
  const ctx = cv.getContext('2d');
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, w, hgt);
  const step = 3, n = Math.floor(w / step);
  const pts = data.slice(-n);
  let max = 0;
  for (const p of pts) if (p.r > max) max = p.r;
  max = Math.max(max * 1.15, 2);
  const c = css();
  const x0 = w - pts.length * step;
  for (let i = 0; i < pts.length; i++) {
    const p = pts[i], x = x0 + i * step;
    if (p.r < 0) { ctx.fillStyle = c.lost; ctx.globalAlpha = .85; ctx.fillRect(x, 0, step - 1, hgt); ctx.globalAlpha = 1; continue; }
    const bh = Math.max(1.5, (p.r / max) * (hgt - 3));
    ctx.fillStyle = warnMs && p.r > warnMs ? c.warn : c.bar;
    ctx.fillRect(x, hgt - bh, step - 1, bh);
  }
  if (warnMs && warnMs < max) {
    const y = Math.round(hgt - (warnMs / max) * (hgt - 3)) + .5;
    ctx.strokeStyle = c.warn; ctx.globalAlpha = .6; ctx.setLineDash([3, 3]);
    ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(w, y); ctx.stroke(); ctx.setLineDash([]); ctx.globalAlpha = 1;
  }
}

/* ---------------- liveflöde ---------------- */
function connectLive() {
  if (S.es) S.es.close();
  const es = new EventSource('/api/live');
  S.es = es;
  es.onopen = () => { S.connected = true; updateConn(); };
  es.onerror = async () => {
    S.connected = false; updateConn();
    if (es.readyState === EventSource.CLOSED) {
      try { await GET('/api/me'); setTimeout(connectLive, 3000); } catch (e) { /* utloggad */ }
    }
  };
  es.onmessage = ev => {
    let m; try { m = JSON.parse(ev.data); } catch (e) { return; }
    if (m.type === 'tick') onTick(m);
    else if (m.type === 'ack') { S.acks[m.key] = m.ack; if (S.view.name === 'alerts') renderPage(); }
    else if (m.type === 'alert') onAlert(m.event);
    else if (m.type === 'trace') onTrace(m.trace);
    else if (m.type === 'scan') onScan(m.scan);
    else if (m.type === 'snmp') onSnmpEvent(m);
    else if (m.type === 'integration') onIntegrationEvent(m);
    else if (m.type === 'survey') { if (S.hostView && S.hostView.id === m.agentId && !document.querySelector('#hvSurvey select:focus')) renderSurvey(m.agentId); }
    else if (m.type === 'speed') { if (S.hostView && S.hostView.id === m.agentId) renderSpeed(m.agentId); }
  };
}
function updateConn() { const b = $('#connBadge'); if (b) b.replaceWith(connBadge()); }

let structTimer = null;
function onTick(m) {
  Object.assign(S.stats, m.stats || {});
  Object.assign(S.live, m.agents || {});
  if (m.server) S.serverHost = m.server;
  S.maint = m.maint || {};
  S.acks = m.acks || {};
  if (S.hostView) refreshHostView();
  S.active = m.active || {};
  for (const [id, arr] of Object.entries(m.samples || {})) {
    const sp = S.spark[id] || (S.spark[id] = []);
    sp.push(...arr);
    if (sp.length > SPARK_MAX) sp.splice(0, sp.length - SPARK_MAX);
    if (id === S.selected && S.chartData && S.chartMode === 'raw') appendChart(arr);
  }
  // nya mål eller agenter som dykt upp (t.ex. från en skrivbordsapp) → hämta om strukturen
  const unknown = Object.keys(m.stats || {}).some(id => !byId(S.targets, id)) || Object.keys(m.agents || {}).some(id => !byId(S.agents, id));
  if (unknown && !structTimer) structTimer = setTimeout(async () => { structTimer = null; await afterChange(); }, 500);
  if (S.view && S.view.name === 'overview' && !document.querySelector('.backdrop')) renderOverview($('#page'));
  else if (S.view && S.view.name === 'tv') renderTV($('#page'));
  else for (const t of S.targets) if (tiles.has(t.id)) updateTile(t);
  document.querySelectorAll('.agent-chip').forEach(ch => {
    const a = agentOf(ch.dataset.agent);
    if (a) { const n = agentChip(a); if (n.dataset.sig !== ch.dataset.sig) ch.replaceWith(n); }
  });
  if (S.selected) updateDrawerHead();
  const snmpSig = Object.keys(S.active).filter(k => k.includes('|snmp:')).sort().join();
  if (snmpSig !== S.snmpSig) { S.snmpSig = snmpSig; refreshSnmpRows(); }
  renderNavThrottled();
}
let navT = 0;
function renderNavThrottled() { const n = Date.now(); if (n - navT > 5000) { navT = n; renderNav(); } }

function onAlert(ev) {
  const title = { fire: T('Alerts'), repeat: T('Alert ongoing'), clear: T('Alert cleared'), test: T('Test alert') }[ev.kind] || T('Alerts');
  if (ev.suppressed) return;
  if (ev.popup || ev.kind === 'test') toast({ kind: ev.kind, title: `${title}: ${ev.targetName}`, text: `${ev.alertName} – ${ev.detail}`, time: ev.time, sticky: ev.kind === 'fire', target: byId(S.targets, ev.targetId) ? ev.targetId : null,
    ackKey: isAdmin() && (ev.kind === 'fire' || ev.kind === 'repeat') ? ev.alertId + '|' + ev.targetId : null });
  if (ev.sound) playSound(ev.soundName);
}

/* ---------------- detaljvy ---------------- */
let chart = null;
async function openDrawer(id) {
  const t = byId(S.targets, id);
  if (!t) return;
  const cust = customerOfTarget(t);
  if (S.view.name !== 'customer' || S.view.id !== cust) { location.hash = '#/c/' + cust; await new Promise(r => setTimeout(r, 30)); }
  S.selected = id;
  tiles.forEach((el, tid) => el.classList.toggle('selected', tid === id));
  const d = $('#drawer');
  d.classList.remove('hidden');
  d.replaceChildren(
    h('div', { class: 'drawer-grip', id: 'drawerGrip' }),
    h('div', { class: 'drawer-head', id: 'dHead' },
      h('span', { class: 'dot' }), h('div', { class: 'names' }, h('div', { class: 'name', id: 'dName' }), h('div', { class: 'host', id: 'dHost' })),
      h('div', { class: 'ranges', id: 'dRanges' }, ...RANGES.map(([sec, label]) => h('button', { 'data-r': sec, onclick: () => { S.range = sec; loadChart(); } }, label))),
      h('div', { class: 'drawer-actions' },
        isAdmin() ? h('button', { class: 'btn ghost sm', onclick: () => editTargetDialog(S.selected) }, T('Edit')) : null,
        h('button', { class: 'btn ghost sm', onclick: () => traceDialog(S.selected) }, 'Traceroute'),
        h('button', { class: 'btn ghost sm', onclick: () => { const to = Date.now(); location.href = `/api/targets/${S.selected}/export.csv?from=${to - S.range * 1000}&to=${to}`; } }, T('Export CSV')),
        h('button', { class: 'icon-btn', title: T('Close (Esc)'), onclick: closeDrawer }, '✕'))),
    h('div', { class: 'drawer-body' }, h('div', { class: 'dstats', id: 'dStats' }), h('div', { class: 'chart', id: 'dChart' })));
  const grip = $('#drawerGrip');
  grip.addEventListener('mousedown', e => {
    e.preventDefault();
    const startY = e.clientY, startH = d.offsetHeight;
    const move = ev => { d.style.height = Math.max(240, Math.min(window.innerHeight * .8, startH + startY - ev.clientY)) + 'px'; resizeChart(); };
    const up = () => { window.removeEventListener('mousemove', move); window.removeEventListener('mouseup', up); };
    window.addEventListener('mousemove', move); window.addEventListener('mouseup', up);
  });
  updateDrawerHead();
  await loadChart();
  const el = tiles.get(id); if (el) el.scrollIntoView({ block: 'nearest' });
}

function closeDrawer() {
  S.selected = null; S.chartData = null;
  tiles.forEach(el => el.classList.remove('selected'));
  const d = $('#drawer'); if (d) d.classList.add('hidden');
  if (chart) { chart.destroy(); chart = null; }
}

function updateDrawerHead() {
  const t = byId(S.targets, S.selected);
  if (!t || !$('#dHead')) return;
  const st = S.stats[t.id] || {};
  $('#dHead').className = 'drawer-head st-' + (st.status || 'pending');
  $('#dName').textContent = displayName(t);
  const a = agentOf(t.agentId);
  $('#dHost').textContent = T('{0}{1} · {2} · {3} · every {4}', t.host, st.addr && st.addr !== t.host ? ' → ' + st.addr : '', kindText(t), a ? a.name : '', t.intervalMs >= 1000 ? t.intervalMs / 1000 + ' s' : t.intervalMs + ' ms');
  document.querySelectorAll('#dRanges button').forEach(b => b.classList.toggle('on', +b.dataset.r === S.range));
  renderDrawerStats();
}

function renderDrawerStats() {
  const d = S.chartData, t = byId(S.targets, S.selected);
  if (!d || !t || !$('#dStats')) return;
  const st = S.stats[t.id] || {};
  const row = (k, v) => [h('dt', null, k), h('dd', null, v)];
  let rows;
  if (S.chartMode === 'raw') {
    const vals = []; let lost = 0;
    for (let i = 0; i < d[0].length; i++) { if (d[2][i]) lost++; else if (d[1][i] != null) vals.push(d[1][i]); }
    const n = vals.length + lost, sorted = vals.slice().sort((a, b) => a - b);
    const avg = vals.length ? vals.reduce((a, b) => a + b, 0) / vals.length : NaN;
    let jit = 0; for (let i = 1; i < vals.length; i++) jit += Math.abs(vals[i] - vals[i - 1]); jit = vals.length > 1 ? jit / (vals.length - 1) : NaN;
    const pct = p => sorted.length ? sorted[Math.min(sorted.length - 1, Math.floor(p * sorted.length))] : NaN;
    rows = [row(T('Packets'), String(n)), row(T('Lost'), `${lost} (${n ? +(lost * 100 / n).toFixed(2) : 0}%)`), row(T('Avg'), fmtMs(avg) + ' ms'),
      row('Min', fmtMs(sorted[0]) + ' ms'), row('Max', fmtMs(sorted[sorted.length - 1]) + ' ms'), row('Median', fmtMs(pct(.5)) + ' ms'),
      row(T('95th pct'), fmtMs(pct(.95)) + ' ms'), row('Jitter', fmtMs(jit) + ' ms')];
  } else {
    const n = S.chartN || [];
    let tot = 0, lostTot = 0, sum = 0, okN = 0, mn = Infinity, mx = -Infinity;
    for (let i = 0; i < d[0].length; i++) {
      const cnt = n[i] || 0, lossP = d[4][i] || 0; tot += cnt; const l = Math.round(cnt * lossP / 100); lostTot += l;
      if (d[1][i] != null) { sum += d[1][i] * (cnt - l); okN += cnt - l; }
      if (d[2][i] != null) mn = Math.min(mn, d[2][i]);
      if (d[3][i] != null) mx = Math.max(mx, d[3][i]);
    }
    rows = [row(T('Packets'), String(tot)), row(T('Lost'), `${lostTot} (${tot ? +(lostTot * 100 / tot).toFixed(2) : 0}%)`),
      row(T('Avg'), fmtMs(okN ? sum / okN : NaN) + ' ms'), row('Min', fmtMs(isFinite(mn) ? mn : NaN) + ' ms'), row('Max', fmtMs(isFinite(mx) ? mx : NaN) + ' ms'),
      row(T('Resolution'), S.chartBucket >= 3600000 ? S.chartBucket / 3600000 + ' h' : S.chartBucket / 60000 + ' min')];
  }
  $('#dStats').replaceChildren(h('dl', null, row('Status', STATUS_TEXT[st.status] || '–'), row(T('Since'), st.since ? dur(Date.now() - st.since) : '–'),
    row(T('Last'), st.last >= 0 ? fmtMs(st.last) + ' ms' : T('lost')), ...rows), ...(st.err ? [h('div', { class: 'err' }, TE(st.err))] : []));
}

async function loadChart() {
  const id = S.selected;
  if (!id) return;
  const to = Date.now(), from = to - S.range * 1000;
  const hs = await GET(`/api/targets/${id}/history?from=${from}&to=${to}`);
  if (id !== S.selected) return;
  S.chartMode = hs.mode; S.chartBucket = hs.bucket; S.chartN = hs.n;
  const sec = (hs.t || []).map(x => x / 1000);
  if (hs.mode === 'raw') {
    // lucka i grafen där agenten inte rapporterat (i stället för ett rakt streck)
    const t = byId(S.targets, id), gap = Math.max(10, 3 * ((t && t.intervalMs) || 1000) / 1000);
    const xs = [], ys = [], ls = [], r = hs.r || [];
    for (let i = 0; i < sec.length; i++) {
      if (i && sec[i] - sec[i - 1] > gap) { xs.push(sec[i - 1] + gap / 3); ys.push(null); ls.push(0); }
      xs.push(sec[i]); ys.push(r[i] < 0 ? null : r[i]); ls.push(r[i] < 0 ? 1 : 0);
    }
    S.chartData = [xs, ys, ls];
  }
  else S.chartData = [sec, hs.avg || [], hs.min || [], hs.max || [], hs.loss || []];
  updateDrawerHead();
  rebuildChart();
}

function appendChart(samples) {
  const d = S.chartData;
  for (const s of samples) { d[0].push(s.t / 1000); d[1].push(s.r < 0 ? null : s.r); d[2].push(s.r < 0 ? 1 : 0); }
  const cutoff = Date.now() / 1000 - S.range;
  let k = 0; while (k < d[0].length && d[0][k] < cutoff) k++;
  if (k) for (const a of d) a.splice(0, k);
  if (chart) chart.setData(d, !S.zoomed);
  renderDrawerStats();
}

function timeLabel(sec, range) {
  const d = new Date(sec * 1000), p = n => String(n).padStart(2, '0');
  if (range > 172800) return `${d.getDate()}/${d.getMonth() + 1} ${p(d.getHours())}:${p(d.getMinutes())}`;
  return range <= 900 ? `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}` : `${p(d.getHours())}:${p(d.getMinutes())}`;
}
function hexA(color, a) {
  const m = /^#?([\da-f]{2})([\da-f]{2})([\da-f]{2})$/i.exec(color.trim());
  return m ? `rgba(${parseInt(m[1], 16)},${parseInt(m[2], 16)},${parseInt(m[3], 16)},${a})` : color;
}

function rebuildChart() {
  const box = $('#dChart');
  if (chart) { chart.destroy(); chart = null; }
  if (!box || !S.selected || !S.chartData) return;
  const c = css(), t = byId(S.targets, S.selected), warn = t ? warnFor(t) : 0;
  const raw = S.chartMode === 'raw';
  const lossPlugin = { hooks: {
    drawClear: u => {
      const ctx = u.ctx, d = u.data, top = u.bbox.top, hgt = u.bbox.height;
      ctx.save(); ctx.fillStyle = c.lost;
      if (raw) {
        ctx.globalAlpha = .28;
        const ivl = t ? t.intervalMs / 1000 : 1;
        const w = Math.max(1.5 * devicePixelRatio, ivl * u.bbox.width / S.range * .9);
        for (let i = 0; i < d[0].length; i++) if (d[2][i]) { const x = u.valToPos(d[0][i], 'x', true); ctx.fillRect(x - w / 2, top, w, hgt); }
      } else {
        ctx.globalAlpha = .45;
        const w = Math.max(1.5 * devicePixelRatio, (S.chartBucket / 1000) * u.bbox.width / S.range * .85);
        for (let i = 0; i < d[0].length; i++) if (d[4][i] > 0) {
          const x = u.valToPos(d[0][i] + S.chartBucket / 2000, 'x', true);
          const bh = Math.max(2, hgt * Math.min(100, d[4][i]) / 100);
          ctx.fillRect(x - w / 2, top + hgt - bh, w, bh);
        }
      }
      ctx.restore();
    },
    draw: u => {
      if (!warn) return;
      const y = u.valToPos(warn, 'y', true);
      if (y < u.bbox.top || y > u.bbox.top + u.bbox.height) return;
      const ctx = u.ctx; ctx.save(); ctx.strokeStyle = c.warn; ctx.setLineDash([5, 4]); ctx.lineWidth = devicePixelRatio; ctx.globalAlpha = .8;
      ctx.beginPath(); ctx.moveTo(u.bbox.left, y); ctx.lineTo(u.bbox.left + u.bbox.width, y); ctx.stroke(); ctx.restore();
    },
  } };
  const axis = { stroke: c.muted, grid: { stroke: c.border, width: 1 }, ticks: { stroke: c.border, width: 1 }, font: '11px ' + getComputedStyle(document.body).fontFamily };
  const val = (u, v, si, idx) => idx == null ? '–' : v == null ? '–' : fmtMs(v) + ' ms';
  const series = raw ? [
    { label: T('Time'), value: (u, v) => v == null ? '–' : new Date(v * 1000).toLocaleString('sv-SE') },
    { label: T('Latency'), stroke: c.accent, width: 1.5, fill: hexA(c.accent, .12), points: { show: false }, spanGaps: false, value: (u, v, si, idx) => idx == null ? '–' : v == null ? T('lost') : fmtMs(v) + ' ms' },
    { label: T('Lost|one'), scale: 'loss', stroke: c.lost, width: 0, points: { show: false }, paths: () => null, value: (u, v, si, idx) => idx == null ? '–' : v ? T('yes') : T('no') },
  ] : [
    { label: T('Time'), value: (u, v) => v == null ? '–' : new Date(v * 1000).toLocaleString('sv-SE') },
    { label: T('Avg'), stroke: c.accent, width: 1.6, points: { show: false }, value: val },
    { label: 'Min', stroke: hexA(c.accent, .35), width: 1, points: { show: false }, value: val },
    { label: 'Max', stroke: hexA(c.accent, .35), width: 1, points: { show: false }, value: val },
    { label: T('Loss'), scale: 'loss', stroke: c.lost, width: 0, points: { show: false }, paths: () => null, value: (u, v, si, idx) => idx == null || v == null ? '–' : (+v).toFixed(2) + ' %' },
  ];
  const opts = {
    width: box.clientWidth - 14, height: Math.max(120, box.clientHeight - 40),
    cursor: { drag: { x: true, y: false } },
    scales: { x: { time: true, range: () => { const n = Date.now() / 1000; return [n - S.range, n]; } }, y: { range: (u, min, max) => [0, Math.max((max || 0) * 1.15, 0.1)] }, loss: { range: [0, 100] } },
    axes: [Object.assign({}, axis, { values: (u, splits) => splits.map(v => timeLabel(v, S.range)) }), Object.assign({}, axis, { label: 'ms', labelSize: 18, size: 50 })],
    series, legend: { live: true }, plugins: [lossPlugin],
    bands: raw ? [] : [{ series: [3, 2], fill: hexA(c.accent, .12) }],
    hooks: { setSelect: [u => { if (u.select.width > 4) S.zoomed = true; }] },
  };
  S.zoomed = false;
  chart = new uPlot(opts, S.chartData, box);
  chart.over.addEventListener('dblclick', () => { S.zoomed = false; chart.setData(S.chartData, true); });
}
function resizeChart() { if (chart) { const box = $('#dChart'); chart.setSize({ width: box.clientWidth - 14, height: Math.max(120, box.clientHeight - 40) }); } }
window.addEventListener('resize', () => { resizeChart(); for (const t of S.targets) if (tiles.has(t.id)) updateTile(t); });



/* ---------------- rapporter ---------------- */
function reportDialog(c) {
  const period = select([['last-month', T('Last month')], ['this-month', T('This month')], ['last-week', T('Last week')], ['this-week', T('This week')], ['last-30', T('Last 30 days')], ['custom', T('Choose dates…')]], 'last-month');
  const pad = n => String(n).padStart(2, '0');
  const d0 = new Date(Date.now() - 7 * 864e5), d1 = new Date();
  const fromD = h('input', { type: 'date', value: `${d0.getFullYear()}-${pad(d0.getMonth() + 1)}-${pad(d0.getDate())}` });
  const toD = h('input', { type: 'date', value: `${d1.getFullYear()}-${pad(d1.getMonth() + 1)}-${pad(d1.getDate())}` });
  const custom = h('div', { class: 'form', style: 'grid-column:1/-1;display:none' }, field(T('From'), fromD), field(T('To (inclusive)'), toD));
  period.onchange = () => { custom.style.display = period.value === 'custom' ? '' : 'none'; };
  const range = () => {
    if (period.value !== 'custom') return { period: period.value };
    return { period: 'custom', from: new Date(fromD.value + 'T00:00').getTime(), to: new Date(toD.value + 'T00:00').getTime() + 864e5 };
  };
  const url = dl => { const r = range(); const q = new URLSearchParams({ period: r.period }); if (r.from) { q.set('from', r.from); q.set('to', r.to); } if (dl) q.set('download', '1'); return `/api/customers/${c.id}/report.pdf?${q}`; };
  const fields = [field('Period', period, null, true), custom,
    h('div', { class: 'field full' }, h('div', { class: 'row' },
      h('button', { class: 'btn primary', onclick: () => window.open(url(false), '_blank') }, T('View PDF')),
      h('button', { class: 'btn', onclick: () => { location.href = url(true); } }, T('Download')))),
    h('small', { class: 'field full' }, T('The report shows availability, downtime, packet loss and latency per target, daily graphs, the agents\' CPU, memory and disk, maintenance windows and alert events.'))];
  if (isAdmin()) {
    const to = h('input', { type: 'text', value: c.reportTo || '', placeholder: T('customer@example.com, you@yourcompany.com') });
    const freq = select([['', T('Off')], ['weekly', T('Weekly (Monday morning, previous week)')], ['monthly', T('Monthly (on the 1st, previous month)')]], c.reportFreq || '');
    const res = h('span');
    fields.push(h('div', { class: 'section-title' }, T('Send by email')), field(T('Recipients'), to, null, true), field(T('Automatic'), freq, null, true),
      h('div', { class: 'field full' }, h('div', { class: 'row' },
        h('button', { class: 'btn', onclick: async () => {
          await PUT('/api/customers/' + c.id, Object.assign({}, c, { reportTo: to.value, reportFreq: freq.value }));
          res.className = 'form-ok'; res.textContent = T('Saved.'); await loadState();
        } }, T('Save setting')),
        h('button', { class: 'btn', onclick: async () => {
          res.className = ''; res.replaceChildren(h('span', { class: 'spin' }), T(' Sending…'));
          const r = range();
          try { await POST(`/api/customers/${c.id}/report/send`, { to: to.value, period: r.period, from: r.from || 0, toT: r.to || 0 }); res.className = 'form-ok'; res.textContent = T('The report was sent.'); }
          catch (e) { res.className = 'form-error'; res.textContent = e.message; }
        } }, T('Send now')), res)),
      h('small', { class: 'field full' }, T('Requires email to be configured under Settings.')));
  }
  modal({ title: T('Report – {0}', c.name), body: h('div', { class: 'form' }, ...fields) });
}

/* ---------------- statussida ---------------- */
function statusPageDialog(c) {
  const body = h('div');
  const render = () => {
    const cc = byId(S.customers, c.id) || c;
    const link = cc.statusKey ? `${S.serverUrl}/status/${cc.statusKey}` : '';
    body.replaceChildren(h('div', { class: 'form' },
      h('p', { class: 'field full', style: 'margin:0' }, T('A public page showing the customer\'s targets with status and availability (24 h and 30 d) – no sign-in, IP addresses or details. The link is secret; share it with the customer.')),
      link ? field(T('Link'), copyBox(link), null, true) : h('p', { class: 'muted field full' }, T('The status page is turned off.')),
      h('div', { class: 'field full' }, h('div', { class: 'row' },
        link ? h('a', { class: 'btn', href: link, target: '_blank' }, T('Open')) : null,
        h('button', { class: 'btn' + (link ? '' : ' primary'), onclick: async () => { await POST(`/api/customers/${c.id}/status-key`, { enable: true }); await loadState(); render(); } }, link ? T('Create new link') : T('Turn on status page')),
        link ? h('button', { class: 'btn danger', onclick: async () => { await POST(`/api/customers/${c.id}/status-key`, { enable: false }); await loadState(); render(); } }, T('Turn off')) : null)),
      link ? h('small', { class: 'field full' }, T('A new link makes the old one stop working.')) : null));
  };
  render();
  modal({ title: T('Status page – {0}', c.name), wide: true, body });
}

/* ---------------- tvåstegsinloggning ---------------- */
async function totpDialog() {
  const me = await GET('/api/me'); S.me = me;
  if (me.totp) {
    const pw = h('input', { type: 'password', autocomplete: 'current-password' });
    formDialog({ title: T('Two-factor authentication is on'), saveLabel: T('Turn off'), fields: [
      h('p', { class: 'field full', style: 'margin:0' }, T('You sign in with your password and a code from your authenticator app. Enter your password to turn it off.')),
      field(T('Password'), pw, null, true)],
      onSave: async () => { await POST('/api/me/totp/disable', { password: pw.value }); S.me.totp = false; renderShell(); renderPage(); toast({ kind: 'info', title: T('Two-factor authentication turned off') }); } });
    return;
  }
  const setup = await POST('/api/me/totp/setup');
  const code = h('input', { type: 'text', inputmode: 'numeric', autocomplete: 'one-time-code', placeholder: '123456' });
  const err = h('span', { class: 'form-error' });
  const body = h('div', { class: 'form' },
    h('p', { class: 'field full', style: 'margin:0' }, T('Scan the QR code with an authenticator app (e.g. Microsoft Authenticator, Google Authenticator, 1Password or Bitwarden) and enter the code shown.')),
    h('div', { class: 'field', style: 'align-items:center' }, h('img', { src: setup.qr, width: 200, height: 200, alt: T('QR code'), style: 'background:#fff;border-radius:8px;padding:6px' })),
    h('div', { class: 'field' }, h('span', null, T('Or enter the key manually')), h('code', { style: 'word-break:break-all' }, setup.secret),
      h('span', { style: 'margin-top:12px' }, T('Code from the app')), code));
  const close = modal({ title: T('Enable two-factor authentication'), body,
    foot: [h('div', { class: 'left' }, err), h('button', { class: 'btn', onclick: () => close() }, T('Cancel')),
      h('button', { class: 'btn primary', onclick: async () => {
        err.textContent = '';
        try {
          const r = await POST('/api/me/totp/enable', { code: code.value });
          close(); S.me.totp = true; renderShell(); renderPage();
          modal({ title: T('Save your backup codes'), body: h('div', null,
            h('p', null, T('If you lose your phone you can sign in with one of these codes (each code works once). Keep them somewhere safe – they are only shown now.')),
            copyBox(r.recovery.join('  '))) });
        } catch (e) { err.textContent = e.message; }
      } }, T('Enable'))] });
}

/* ---------------- ändringslogg ---------------- */
async function renderAudit(page) {
  const q = h('input', { type: 'search', placeholder: T('Search by user, type or text…'), style: 'width:300px' });
  const box = h('div');
  const load = async () => {
    const list = await GET('/api/audit?q=' + encodeURIComponent(q.value));
    box.replaceChildren(list.length ? h('table', { class: 'tbl' },
      h('thead', null, h('tr', null, ...[T('Time'), T('Users'), T('Type'), T('Event'), 'IP'].map(x => h('th', null, x)))),
      h('tbody', null, ...list.map(a => h('tr', null, h('td', { class: 'mono' }, dateTime(a.t)), h('td', null, a.user), h('td', null, T(a.kind)),
        h('td', null, a.text), h('td', { class: 'mono muted' }, a.ip))))) : h('p', { class: 'muted' }, T('No events.')));
  };
  let tm; q.oninput = () => { clearTimeout(tm); tm = setTimeout(load, 300); };
  page.replaceChildren(h('div', { class: 'page-head' }, h('h1', null, T('Audit log')), h('div', { class: 'tools' }, q)), box,
    h('p', { class: 'ro-note', style: 'margin-top:12px' }, T('Sign-ins and all changes to customers, agents, targets, alerts, users and settings. Kept as long as the minute values.')));
  await load();
}

/* ---------------- TV-läge ---------------- */
function renderTV(page) {
  const all = counts(S.targets);
  const clockEl = h('div', { class: 'tv-clock' }, new Date().toLocaleTimeString('sv-SE', { hour: '2-digit', minute: '2-digit' }));
  const cards = S.customers.map(c => {
    const ts = S.targets.filter(t => customerOfTarget(t) === c.id);
    const cnt = counts(ts);
    const st = cnt.down ? 'down' : cnt.offline ? 'offline' : cnt.warn ? 'warn' : 'up';
    return h('div', { class: 'tv-card st-' + st, onclick: () => { location.hash = '#/c/' + c.id; } },
      h('div', { class: 'tv-name' }, h('span', { class: 'dot' }), c.name),
      h('div', { class: 'tv-counts' }, ...[['up', T('up')], ['warn', T('warning')], ['down', T('down')], ['offline', 'offline']].filter(([k]) => cnt[k] || k === 'up').map(([k, l]) => h('span', { class: 'st-' + k }, h('b', null, String(cnt[k])), ' ' + l))));
  });
  const problems = S.targets.filter(t => ['down', 'offline', 'warn'].includes((S.stats[t.id] || {}).status) && !maintNote(t))
    .sort((a, b) => ({ down: 0, offline: 1, warn: 2 })[S.stats[a.id].status] - ({ down: 0, offline: 1, warn: 2 })[S.stats[b.id].status]).slice(0, 14);
  page.replaceChildren(h('div', { class: 'tv' },
    h('div', { class: 'tv-head' }, h('div', { class: 'brand' }, logo(), h('span', null, 'PingVakt')),
      h('div', { class: 'tv-sum' }, ...[['up', T('up')], ['warn', T('warning')], ['down', T('down')], ['offline', 'offline']].map(([k, l]) => h('span', { class: 'chip st-' + k + (all[k] ? '' : ' zero') }, h('span', { class: 'dot' }), `${all[k]} ${l}`))),
      clockEl, h('button', { class: 'btn ghost', onclick: () => { location.hash = '#/'; } }, T('Exit'))),
    h('div', { class: 'tv-grid' }, ...cards),
    h('div', { class: 'tv-problems' }, h('h2', null, problems.length ? T('Right now') : T('All systems working')),
      ...problems.map(t => { const st = S.stats[t.id]; const a = agentOf(t.agentId); const site = a && siteOf(a.siteId); const c = site && byId(S.customers, site.customerId);
        return h('div', { class: 'tv-row st-' + st.status }, h('span', { class: 'dot' }), h('b', null, displayName(t)), h('span', { class: 'muted' }, `${c ? c.name : ''} · ${site ? site.name : ''}`),
          h('span', { class: 'tv-since' }, `${STATUS_TEXT[st.status]} ${dur(Date.now() - st.since)}`)); }))));
}

/* ---------------- kvittering och servicefönster ---------------- */
function ackAlert(key) {
  return new Promise(resolve => {
    const note = h('input', { type: 'text', placeholder: T('Optional comment, e.g. "Technician on the way"') });
    formDialog({ title: T('Acknowledge alert'), saveLabel: T('Acknowledge'), fields: [field(T('Comment'), note, T('The alert is not repeated while acknowledged. The acknowledgement is removed when the alert clears.'), true)],
      onSave: async () => { const a = await POST('/api/alerts/ack', { key, note: note.value }); S.acks[key] = a; resolve(true); } });
  });
}

async function maintDialog(customerId) {
  const c = byId(S.customers, customerId);
  const listBox = h('div');
  const sites = S.sites.filter(s => s.customerId === customerId);
  const ags = S.agents.filter(a => customerOfAgent(a) === customerId);
  const tgs = S.targets.filter(t => customerOfTarget(t) === customerId);
  const scope = select([['', T('Entire customer')], ...sites.map(s => ['s:' + s.id, 'Site: ' + s.name]), ...ags.map(a => ['a:' + a.id, 'Agent: ' + a.name]), ...tgs.map(t => ['t:' + t.id, T('Target: ') + displayName(t)])], '');
  const type = select([['once', T('Once')], ['weekly', T('Weekly')]], 'once');
  const pad = n => String(n).padStart(2, '0');
  const local = d => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  const start = h('input', { type: 'datetime-local', value: local(new Date()) });
  const end = h('input', { type: 'datetime-local', value: local(new Date(Date.now() + 2 * 3600e3)) });
  const days = new Set([6]);
  const dayNames = [[1, T('Mon')], [2, T('Tue')], [3, T('Wed')], [4, T('Thu')], [5, T('Fri')], [6, T('Sat')], [0, T('Sun')]];
  const dayBox = h('div', { class: 'checks' }, ...dayNames.map(([d, n]) => check(n, days.has(d), e => e.target.checked ? days.add(d) : days.delete(d))));
  const wStart = h('input', { type: 'time', value: '22:00' });
  const wDur = num(4, 0.25, 168, 0.25);
  const note = h('input', { type: 'text', placeholder: T('e.g. Switch upgrade') });
  const once = h('div', { class: 'form', style: 'grid-column:1/-1' }, field('Start', start), field(T('End'), end));
  const weekly = h('div', { class: 'form', style: 'grid-column:1/-1;display:none' }, field(T('Days'), dayBox, null, true), field(T('Start time'), wStart), field(T('Duration (hours)'), wDur));
  type.onchange = () => { once.style.display = type.value === 'once' ? '' : 'none'; weekly.style.display = type.value === 'weekly' ? '' : 'none'; };
  const err = h('span', { class: 'form-error' });
  const scopeText = m => m.targetId ? T('Target: ') + displayName(byId(S.targets, m.targetId) || { host: '?' }) : m.agentId ? 'Agent: ' + ((agentOf(m.agentId) || {}).name || '?') : m.siteId ? 'Site: ' + ((siteOf(m.siteId) || {}).name || '?') : T('Entire customer');
  const when = m => {
    if (!m.weekly) return `${dateTime(m.start)} – ${dateTime(m.end)}`;
    const ds = dayNames.filter(([d]) => (m.days || []).includes(d)).map(x => x[1]).join(', ');
    return `${ds} ${pad(Math.floor(m.startMin / 60))}:${pad(m.startMin % 60)}, ${+(m.durationMin / 60).toFixed(2)} h`;
  };
  const load = async () => {
    const all = (await GET('/api/maintenance')).filter(x => x.window.customerId === customerId);
    listBox.replaceChildren(all.length ? h('table', { class: 'tbl' }, h('tbody', null, ...all.map(x => h('tr', null,
      h('td', null, x.active ? h('span', { class: 'pill st-warn' }, h('span', { class: 'dot' }), T('Ongoing')) : x.expired ? h('span', { class: 'muted' }, T('Past')) : h('span', { class: 'muted' }, T('Planned'))),
      h('td', null, h('b', null, x.window.note || T('Maintenance windows')), h('div', { class: 'muted' }, scopeText(x.window))),
      h('td', null, when(x.window)),
      h('td', { class: 'actions' }, isAdmin() ? h('button', { class: 'btn sm danger', onclick: async () => { await DEL('/api/maintenance/' + x.window.id); await load(); } }, T('Delete')) : null))))) :
      h('p', { class: 'muted' }, T('No maintenance windows.')));
  };
  const form = isAdmin() ? h('div', { class: 'form', style: 'margin-top:14px' }, h('div', { class: 'section-title' }, T('New maintenance window')),
    field(T('Applies to'), scope), field(T('Type'), type), once, weekly, field(T('Note'), note, null, true),
    h('div', { class: 'field full' }, h('div', { class: 'row' }, h('button', { class: 'btn primary', onclick: async () => {
      err.textContent = '';
      const [k, id] = scope.value.split(':');
      const body = { customerId, note: note.value, siteId: k === 's' ? id : '', agentId: k === 'a' ? id : '', targetId: k === 't' ? id : '', weekly: type.value === 'weekly' };
      if (body.weekly) { const [hh, mm] = wStart.value.split(':').map(Number); Object.assign(body, { days: [...days], startMin: hh * 60 + mm, durationMin: Math.round(+wDur.value * 60) }); }
      else Object.assign(body, { start: new Date(start.value).getTime(), end: new Date(end.value).getTime() });
      try { await POST('/api/maintenance', body); note.value = ''; await load(); } catch (e) { err.textContent = e.message; }
    } }, T('Add')), err)),
    h('small', { class: 'field full' }, T('During a maintenance window no alerts are sent (email, webhook, popup). Events are still saved in the event log.'))) : null;
  modal({ title: T('Maintenance windows – {0}', c ? c.name : ''), wide: true, body: h('div', null, listBox, form) });
  await load();
}

/* ---------------- CPU, minne och disk ---------------- */
const pct = d => d && d.t ? d.u * 100 / d.t : 0;
const memPct = m => m && m.memTotal ? m.memUsed * 100 / m.memTotal : 0;
const diskPct = m => Math.max(0, ...((m && m.disks) || []).map(pct));
const fmtPct = v => v == null || isNaN(v) ? '–' : (v < 10 ? v.toFixed(1) : Math.round(v)) + ' %';
function gb(b) { const v = (b || 0) / 1073741824; return v >= 100 ? Math.round(v) + ' GB' : v >= 10 ? v.toFixed(1) + ' GB' : v.toFixed(2) + ' GB'; }
const level = v => v >= 90 ? 'bad' : v >= 75 ? 'high' : '';
function hostMini(m) {
  const it = (label, v) => h('span', { class: 'hm ' + level(v), title: label }, h('i', null, label), fmtPct(v));
  return h('span', { class: 'hostmini' }, it('CPU', m.cpu), it('RAM', memPct(m)), it('Disk', diskPct(m)));
}
function hostCells(m, online) {
  if (!m) return [h('td', null, '–'), h('td', null, '–'), h('td', null, '–')];
  const c = v => h('td', { class: 'mono ' + (online ? 'lvl-' + (level(v) || 'ok') : 'muted') }, fmtPct(v));
  return [c(m.cpu), c(memPct(m)), c(diskPct(m))];
}
function meter(label, v, sub) {
  return h('div', { class: 'meter ' + level(v) },
    h('div', { class: 'meter-head' }, h('span', null, label), h('b', null, fmtPct(v))),
    h('div', { class: 'bar' }, h('span', { style: `width:${Math.max(0, Math.min(100, v || 0))}%` })),
    sub ? h('small', null, sub) : null);
}

const HOST_RANGES = [[3600, '1 h'], [21600, '6 h'], [86400, '24 h'], [604800, '7 d'], [2592000, '30 d']];
let hostCharts = [];
function hostView(id) {
  const isSrv = id === '_server';
  const a = isSrv ? null : agentOf(id);
  if (!isSrv && !a) return;
  S.hostView = { id, range: S.hostRange || 3600 };
  const now = h('div', { class: 'meters', id: 'hvNow' });
  const facts = h('div', { class: 'facts', id: 'hvFacts' });
  const ranges = h('div', { class: 'ranges' }, ...HOST_RANGES.map(([sec, label]) => h('button', { 'data-r': sec, onclick: () => { S.hostView.range = S.hostRange = sec; loadHostCharts(); } }, label)));
  const charts = h('div', { class: 'hostcharts' },
    h('div', null, h('h4', null, 'CPU'), h('div', { class: 'hchart', id: 'hcCpu' })),
    h('div', null, h('h4', null, T('Memory')), h('div', { class: 'hchart', id: 'hcMem' })),
    h('div', { class: 'full' }, h('h4', null, 'Disk'), h('div', { class: 'hchart', id: 'hcDisk' })),
    h('div', { class: 'full', id: 'hvWifi' }),
    h('div', { class: 'full', id: 'hvSurvey' }),
    h('div', { class: 'full', id: 'hvSpeed' }));
  const title = isSrv ? T('PingVakt server') : `${a.name} – ${customerName(a)} / ${(siteOf(a.siteId) || {}).name || ''}`;
  modal({ title, wide: true, body: h('div', null, facts, now, h('div', { class: 'section-head', style: 'margin-top:16px' }, h('h2', null, T('History')), h('div', { class: 'tools' }, ranges)), charts),
    onClose: () => { S.hostView = null; surveyState = null; hostCharts.forEach(c => c.destroy()); hostCharts = []; } });
  refreshHostView();
  loadHostCharts();
}

function refreshHostView() {
  const hv = S.hostView;
  if (!hv || !$('#hvNow')) return;
  const isSrv = hv.id === '_server';
  const a = isSrv ? null : agentOf(hv.id);
  const lv = isSrv ? { host: S.serverHost, online: true } : (S.live[hv.id] || {});
  const m = lv.host;
  const fact = (k, v) => h('div', null, h('small', null, k), h('b', null, v || '–'));
  $('#hvFacts').replaceChildren(...[
    isSrv ? fact('Version', S.version) : fact('Status', lv.online ? 'Online' : (lv.lastSeen || a.lastSeen) ? 'Offline' : T('Not connected')),
    isSrv ? null : fact(T('Host'), a.hostname), isSrv ? null : fact('System', a.os), isSrv ? null : fact('IP', a.ip), isSrv ? null : fact(T('Agent version'), a.version),
    m ? fact(T('CPU cores'), String(m.cpus)) : null, m ? fact(T('Memory'), gb(m.memTotal)) : null, m ? fact(T('Uptime'), dur(m.uptime * 1000)) : null,
    m && m.load1 ? fact(T('Load (1 min)'), String(m.load1)) : null,
    isSrv ? null : fact(T('Last seen'), (lv.lastSeen || a.lastSeen) ? dateTime(lv.lastSeen || a.lastSeen) : T('never'))].filter(Boolean));
  $('#hvNow').replaceChildren(...(m ? [meter('CPU', m.cpu, T('{0} cores', m.cpus)), meter(T('Memory'), memPct(m), T('{0} of {1}', gb(m.memUsed), gb(m.memTotal))),
    ...(m.disks || []).map(d => meter('Disk ' + d.m, pct(d), T('{0} free of {1}', gb(d.t - d.u), gb(d.t))))]
    : [h('p', { class: 'muted' }, a && a.kind === 'desktop' ? T('The desktop app does not report CPU, memory and disk.') : T('No values yet – the agent reports CPU, memory and disk from version 0.3.'))]));
  document.querySelectorAll('.modal .ranges button').forEach(b => b.classList.toggle('on', +b.dataset.r === hv.range));
}

async function loadHostCharts() {
  const hv = S.hostView;
  if (!hv) return;
  refreshHostView();
  const to = Date.now(), from = to - hv.range * 1000;
  const res = await GET(`/api/agents/${hv.id}/metrics?from=${from}&to=${to}`);
  if (!S.hostView || S.hostView.id !== hv.id) return;
  const hs = res.history;
  hostCharts.forEach(c => c.destroy()); hostCharts = [];
  const c = css();
  const x = hs.t.map(t => t / 1000);
  const axis = { stroke: c.muted, grid: { stroke: c.border, width: 1 }, ticks: { stroke: c.border, width: 1 }, font: '11px ' + getComputedStyle(document.body).fontFamily };
  const pv = (u, v) => v == null ? '–' : fmtPct(v);
  const pts = { show: x.length < 90, size: 4 };
  const mk = (box, series, data) => {
    const el = $(box); if (!el) return;
    el.replaceChildren();
    if (!x.length) { el.append(h('p', { class: 'muted' }, T('No history for the period.'))); return; }
    const u = new uPlot({
      width: el.clientWidth || 400, height: 170,
      scales: { x: { time: true, range: () => [from / 1000, to / 1000] }, y: { range: [0, 100] } },
      axes: [Object.assign({}, axis, { values: (u, sp) => sp.map(v => timeLabel(v, hv.range)) }), Object.assign({}, axis, { size: 44, values: (u, sp) => sp.map(v => v + '%') })],
      series: [{ label: T('Time'), value: (u, v) => v == null ? '–' : new Date(v * 1000).toLocaleString('sv-SE') }, ...series],
      legend: { live: true }, cursor: { drag: { x: false, y: false } },
    }, [x, ...data], el);
    hostCharts.push(u);
  };
  const pal = [c.accent, '#8b7cf6', '#f2b33d', '#5b9cf5', '#e46aa8', '#3ccf7a', '#ef8a50', '#9aa5b1'];
  mk('#hcCpu', [{ label: T('Avg'), stroke: c.accent, width: 1.6, fill: hexA(c.accent, .12), value: pv, points: pts },
    { label: 'Max', stroke: hexA(c.accent, .45), width: 1, value: pv, points: pts }], [hs.cpu, hs.cpuMax]);
  mk('#hcMem', [{ label: T('Used{0}', hs.memTotal ? T(' of ') + gb(hs.memTotal) : ''), stroke: '#8b7cf6', width: 1.6, fill: 'rgba(139,124,246,.12)', value: pv, points: pts }], [hs.mem]);
  renderWifi(res, from, to, axis, hv);
  if (hv.id !== '_server') { renderSpeed(hv.id); if (!surveyState || surveyState.agentId !== hv.id) renderSurvey(hv.id); else { const b = $('#hvSurvey'); if (b && !b.childNodes.length) renderSurvey(hv.id); } }
  mk('#hcDisk', hs.disks.map((d, i) => ({ label: `${d.mount} (${gb(d.total)})`, stroke: pal[i % pal.length], width: 1.6, value: pv, points: pts })), hs.disks.map(d => d.pct));
}

/* ---------------- Wi-Fi ---------------- */
const rssiLevel = v => v == null ? '' : v >= -67 ? 'ok' : v >= -75 ? 'high' : 'bad';
function wifiFacts(w) {
  const fact = (k, v, cls) => h('div', null, h('small', null, k), h('b', { class: cls || '' }, v || '–'));
  const snr = w.noise ? w.rssi - w.noise : null;
  const band = w.freq >= 5925 ? '6 GHz' : w.freq >= 4900 ? '5 GHz' : w.freq ? T('2.4 GHz') : '';
  return h('div', { class: 'facts' }, fact(T('Network (SSID)'), w.ssid || T('hidden')), fact(T('Access point (BSSID)'), w.bssid),
    fact('Signal', w.rssi + ' dBm', 'lvl-' + rssiLevel(w.rssi)), fact(T('Noise'), w.noise ? w.noise + ' dBm' : T('unknown')),
    fact('SNR', snr != null ? snr + ' dB' : '–', snr == null ? '' : snr >= 25 ? 'lvl-ok' : snr >= 15 ? 'lvl-high' : 'lvl-bad'),
    fact(T('Channel'), w.ch ? `${w.ch} (${band}${w.width ? ', ' + w.width + ' MHz' : ''})` : '–'),
    fact(T('Speed'), w.tx ? `${Math.round(w.tx)}${w.rx ? ' / ' + Math.round(w.rx) : ''} Mbit/s` : '–'), w.std ? fact('Standard', w.std) : null);
}
function renderWifi(res, from, to, axis, hv) {
  const box = $('#hvWifi');
  if (!box) return;
  const wh = res.wifi || { t: [], roams: [], aps: [] }, wl = res.wifiLatest;
  if (!wh.t.length && !wl) { box.replaceChildren(); return; }
  const roams = wh.roams || [];
  const chartBox = h('div', { class: 'hchart' });
  box.replaceChildren(...[h('h4', null, 'Wi-Fi'), wl ? wifiFacts(wl) : h('p', { class: 'muted' }, T('Not connected to Wi-Fi right now.')),
    h('div', { class: 'wifi-legend muted' }, T('{0} roam{1} during the period', roams.length, roams.length === 1 ? '' : T('s')), roams.length ? T(' – marked with dashed lines in the graph') : ''),
    chartBox,
    roams.length ? h('table', { class: 'tbl compact' }, h('thead', null, h('tr', null, h('th', null, T('Time')), h('th', null, T('From')), h('th', null, T('To')), h('th', null, T('Signal before → after')))),
      h('tbody', null, ...roams.slice(-20).reverse().map(r => h('tr', null, h('td', { class: 'mono' }, dateTime(r.t)),
        h('td', { class: 'mono' }, `${r.from} (k ${r.fromCh})`), h('td', { class: 'mono' }, `${r.to} (k ${r.toCh})`),
        h('td', { class: 'mono' }, `${r.fromRssi} → ${r.toRssi} dBm`))))) : null,
    wh.aps && wh.aps.length > 1 ? h('table', { class: 'tbl compact' }, h('thead', null, h('tr', null, h('th', null, T('Access point')), h('th', null, 'SSID'), h('th', null, T('Channel')), h('th', null, T('Time')), h('th', null, T('Avg signal')))),
      h('tbody', null, ...wh.aps.map(a => h('tr', null, h('td', { class: 'mono' }, a.bssid), h('td', null, a.ssid || '–'), h('td', { class: 'mono' }, String(a.ch || '–')),
        h('td', { class: 'mono' }, dur(a.minutes * 60000)), h('td', { class: 'mono lvl-' + rssiLevel(a.rssi) }, a.rssi + ' dBm'))))) : null].filter(Boolean));
  if (!wh.t.length) { chartBox.append(h('p', { class: 'muted' }, T('No history for the period.'))); return; }
  const c = css();
  const x = wh.t.map(t => t / 1000);
  const pv = (u, v) => v == null ? '–' : Math.round(v) + ' dBm';
  const pts = { show: x.length < 90, size: 4 };
  const marks = roams.map(r => r.t / 1000);
  const u = new uPlot({
    width: chartBox.clientWidth || 600, height: 190,
    scales: { x: { time: true, range: () => [from / 1000, to / 1000] }, y: { range: (u, mn, mx) => [Math.min(-90, Math.floor((mn || -90) / 10) * 10), Math.max(-30, Math.ceil((mx || -30) / 10) * 10)] } },
    axes: [Object.assign({}, axis, { values: (u, sp) => sp.map(v => timeLabel(v, hv.range)) }), Object.assign({}, axis, { size: 70, values: (u, sp) => sp.map(v => v + ' dBm') })],
    series: [{ label: T('Time'), value: (u, v) => v == null ? '–' : new Date(v * 1000).toLocaleString('sv-SE') },
      { label: 'Signal', stroke: '#3ccf7a', width: 1.6, fill: 'rgba(60,207,122,.10)', value: pv, points: pts },
      { label: T('Lowest'), stroke: 'rgba(60,207,122,.45)', width: 1, value: pv, points: pts },
      { label: T('Noise'), stroke: '#9aa5b1', width: 1, dash: [4, 3], value: pv, points: pts }],
    legend: { live: true }, cursor: { drag: { x: false, y: false } },
    hooks: { draw: [u => {
      const ctx = u.ctx; ctx.save(); ctx.strokeStyle = '#f2b33d'; ctx.lineWidth = 1.5 * devicePixelRatio; ctx.setLineDash([4 * devicePixelRatio, 3 * devicePixelRatio]);
      for (const m of marks) { const px = u.valToPos(m, 'x', true); ctx.beginPath(); ctx.moveTo(px, u.bbox.top); ctx.lineTo(px, u.bbox.top + u.bbox.height); ctx.stroke(); }
      ctx.restore();
    }] },
  }, [x, wh.rssi, wh.rssiMin, wh.noise], chartBox);
  hostCharts.push(u);
}

/* ---------------- traceroute ---------------- */
let traceView = null;
function traceDialog(id) {
  const t = byId(S.targets, id);
  if (!t) return;
  const body = h('div', { class: 'trace-view' });
  traceView = { id, body, runs: [], show: null };
  modal({ title: `Traceroute – ${displayName(t)}`, wide: true, body,
    foot: [h('div', { class: 'left muted' }, T('Runs from {0}', (agentOf(t.agentId) || {}).name || T('the agent'))),
      h('button', { class: 'btn primary', onclick: () => runTrace(id) }, T('Run traceroute'))],
    onClose: () => { traceView = null; } });
  loadTraces(id, true);
}
async function loadTraces(id, autoStart) {
  try {
    const runs = await GET(`/api/targets/${id}/traces`);
    if (!traceView || traceView.id !== id) return;
    traceView.runs = runs;
    if (!traceView.show && runs.length) traceView.show = runs[0].id;
    renderTrace();
    if (autoStart && (!runs.length || Date.now() - runs[0].t > 10 * 60000)) runTrace(id);
  } catch (e) { if (traceView) traceView.body.replaceChildren(h('p', { class: 'form-error' }, e.message)); }
}
async function runTrace(id) {
  try {
    const r = await POST(`/api/targets/${id}/trace`, {});
    if (!traceView) return;
    traceView.show = r.id;
    if (!traceView.runs.find(x => x.id === r.id)) traceView.runs.unshift(r);
    renderTrace();
  } catch (e) { toast({ kind: 'error', title: 'Traceroute', text: e.message }); }
}
function onTrace(tr) {
  if (!traceView || traceView.id !== tr.targetId) return;
  const i = traceView.runs.findIndex(x => x.id === tr.id);
  if (i >= 0) traceView.runs[i] = tr; else traceView.runs.unshift(tr);
  renderTrace();
}
function renderTrace() {
  const v = traceView;
  if (!v) return;
  const t = byId(S.targets, v.id) || {};
  const run = v.runs.find(x => x.id === v.show) || v.runs[0];
  const prevHops = new Set(run && run.prevPath ? run.prevPath.split('>') : []);
  let main;
  if (!run) main = h('p', { class: 'muted' }, T('No traceroute run yet.'));
  else if (run.pending) main = h('p', { class: 'muted' }, h('span', { class: 'spinner' }), T(' Waiting for the agent… (the answer arrives within seconds to a couple of minutes)'));
  else main = h('div', null,
    run.err ? h('p', { class: 'form-error' }, TE(run.err)) : null,
    run.changed ? h('div', { class: 'notice' }, T('The path changed in this run. New hops are highlighted.')) : null,
    h('table', { class: 'tbl compact' }, h('thead', null, h('tr', null, h('th', null, '#'), h('th', null, T('Address')), h('th', null, T('Name')), h('th', null, T('Response time')))),
      h('tbody', null, ...(run.hops || []).map(hp => h('tr', { class: run.changed && hp.addr && hp.addr !== '*' && !prevHops.has(hp.addr) ? 'hop-new' : '' },
        h('td', { class: 'mono' }, String(hp.n)), h('td', { class: 'mono' }, hp.addr || '*'), h('td', null, hp.name || ''),
        h('td', { class: 'mono' }, hp.rtt >= 0 ? fmtMs(hp.rtt) + ' ms' : '–'))))),
    h('small', { class: 'muted' }, T('{0} · {1} · {2} hops{3}', dateTime(run.t), run.auto ? T('automatic') : T('manual'), (run.hops || []).length, run.end ? T(' · took ') + dur(run.end - run.t) : '')));
  const hist = v.runs.filter(r => !r.pending);
  const side = h('div', { class: 'trace-hist' }, h('h4', null, t.traceMin ? T('Path monitoring every {0} min', t.traceMin) : T('Previous runs')),
    t.traceMin ? null : h('small', { class: 'muted' }, T('Turn on path monitoring under Edit to get alerts when the path changes.')),
    hist.length ? h('ul', null, ...hist.slice(0, 30).map(r => h('li', { class: (r.id === (run && run.id) ? 'on ' : '') + (r.changed ? 'changed' : ''), onclick: () => { v.show = r.id; renderTrace(); } },
      h('span', { class: 'mono' }, dateTime(r.t)), r.changed ? h('b', null, T(' path change')) : r.err ? h('span', { class: 'muted' }, T(' error')) : h('span', { class: 'muted' }, T(' {0} hops', (r.hops || []).length))))) : null);
  v.body.replaceChildren(h('div', { class: 'trace-grid' }, main, side));
}

/* ---------------- nätverksskanning ---------------- */
let scanView = null;
async function openScan(a) {
  let info;
  try { info = await GET(`/api/agents/${a.id}/scan`); } catch (e) { toast({ kind: 'error', title: T('Scan network'), text: e.message }); return; }
  const cust = customerOfAgent(a);
  const known = new Set(S.targets.filter(t => customerOfTarget(t) === cust).map(t => t.host));
  const v = scanView = { agentId: a.id, runId: null, ctl: null };
  v.ctl = scanDialog({
    title: T('Scan network – {0}', a.name), sub: T('The scan runs from the agent "{0}" on the customer\'s network.', a.name), networks: info.networks, known, live: false,
    start: async cidr => { const r = await POST(`/api/agents/${a.id}/scan`, { cidr }); v.runId = r.id; },
    stop: () => { v.runId = null; },
    add: a.kind === 'desktop' ? null : async list => {
      const names = {}; for (const d of list) names[d.ip] = d.name || (d.vendor && d.vendor !== 'Private address' ? d.vendor : '');
      await POST('/api/targets', { agentId: a.id, hosts: list.map(d => d.ip).join(','), kind: 'icmp', port: 0, group: '', names });
      await afterChange();
    },
    onClose: () => { if (scanView === v) scanView = null; },
  });
  const last = (info.runs || []).find(r => !r.pending);
  if (last) v.ctl.show(last);
}
function onScan(run) {
  const v = scanView;
  if (!v || v.agentId !== run.agentId || v.runId !== run.id) return;
  v.ctl.done(run.devices, run.err ? TE(run.err) : '');
}

/* ---------------- agenter ---------------- */
function renderAgents(page) {
  const head = h('div', { class: 'page-head' }, h('h1', null, T('Agents')), h('span', { class: 'sub' }, `${S.agents.length} st`), connBadge());
  if (!S.agents.length) { page.replaceChildren(head, h('p', { class: 'muted' }, T('No agents yet. Open a customer and click "New agent".'))); return; }
  const rows = S.agents.slice().sort((a, b) => (customerName(a) + a.name).localeCompare(customerName(b) + b.name, 'sv')).map(a => {
    const lv = S.live[a.id] || {}, seen = lv.lastSeen || a.lastSeen;
    const st = !seen ? 'pending' : lv.online ? 'up' : 'offline';
    const site = siteOf(a.siteId);
    return h('tr', { class: 'clickable', onclick: e => { if (!e.target.closest('button,a')) hostView(a.id); } },
      h('td', null, h('span', { class: 'pill st-' + st }, h('span', { class: 'dot' }), st === 'up' ? 'Online' : st === 'offline' ? 'Offline' : T('Not connected'))),
      h('td', null, h('b', null, a.name), h('div', { class: 'muted' }, a.kind === 'desktop' ? T('Desktop app') : a.kind === 'agent' ? 'Agent' : '–')),
      h('td', null, h('a', { href: '#/c/' + (site ? site.customerId : '') }, customerName(a)), h('div', { class: 'muted' }, site ? site.name : '')),
      h('td', { class: 'mono' }, a.hostname || '–', h('div', { class: 'muted' }, a.os || '')),
      h('td', { class: 'mono' }, a.ip || '–'), h('td', { class: 'mono' + (a.kind === 'agent' && a.version && a.version !== S.version.replace(/-.*$/, '') && st === 'up' ? ' lvl-high' : ''), title: a.kind === 'agent' && a.version !== S.version.replace(/-.*$/, '') ? T('Updates automatically to ') + S.version : '' }, a.version || '–'),
      h('td', null, seen ? dateTime(seen) : T('never')),
      ...hostCells(lv.host, st === 'up'),
      h('td', { class: 'mono ' + (lv.wifi && st === 'up' ? 'lvl-' + rssiLevel(lv.wifi.rssi) : 'muted'), title: lv.wifi ? `${lv.wifi.ssid || ''} ${lv.wifi.bssid || ''} ${T('channel')} ${lv.wifi.ch || '?'}` : T('Not connected via Wi-Fi') }, lv.wifi ? lv.wifi.rssi + ' dBm' : '–'),
      h('td', null, String(S.targets.filter(t => t.agentId === a.id).length)),
      h('td', { class: 'actions' }, isAdmin() ? h('button', { class: 'icon-btn', onclick: e => agentMenu(a, e.currentTarget) }, '⋯') : null));
  });
  page.replaceChildren(head, h('table', { class: 'tbl' },
    h('thead', null, h('tr', null, ...['Status', T('Name'), T('Customer / site'), T('Host'), 'IP', 'Version', T('Last seen'), 'CPU', T('Memory'), 'Disk', 'Wi-Fi', T('Targets'), ''].map(x => h('th', null, x)))),
    h('tbody', null, ...rows)));
}
function customerName(a) { const s = siteOf(a.siteId); const c = s && byId(S.customers, s.customerId); return c ? c.name : ''; }

/* ---------------- larm ---------------- */
async function renderAlerts(page) {
  if (isAdmin()) {
    try { [S.oncalls, S.userList] = await Promise.all([GET('/api/oncall'), GET('/api/users')]); } catch (e) { S.oncalls = S.oncalls || []; }
  }
  const head = h('div', { class: 'page-head' }, h('h1', null, T('Alerts')),
    h('div', { class: 'tools' }, isAdmin() ? h('button', { class: 'btn', onclick: () => onCallDialog(null) }, T('+ On-call schedule')) : null,
      isAdmin() ? h('button', { class: 'btn primary', onclick: () => ruleDialog(null) }, T('+ New alert rule')) : null));
  const groups = [['', T('Applies to all customers')], ...S.customers.map(c => [c.id, c.name])];
  const ruleTables = groups.map(([cid, label]) => {
    const rs = S.rules.filter(r => (r.customerId || '') === cid);
    if (!rs.length && (cid !== '' || !isAdmin())) return null;
    return h('div', { class: 'section' }, h('div', { class: 'section-head' }, h('h2', null, label)),
      rs.length ? h('table', { class: 'tbl' }, h('thead', null, h('tr', null, ...[T('Name'), T('Condition'), T('Actions'), T('Targets'), ''].map(x => h('th', null, x)))),
        h('tbody', null, ...rs.map(r => h('tr', null, h('td', null, h('b', null, r.name)), h('td', null, describe(r)),
          h('td', null, actionsText(r)), h('td', null, r.condition === 'cert' ? T('all HTTPS targets') : r.condition === 'path' ? T('targets with path monitoring') : ['snmp', 'ifdown', 'ifutil', 'iferrors'].includes(r.condition) ? T('SNMP devices') : r.condition === 'vendor' ? T('integrations') : !['down', 'latency', 'loss'].includes(r.condition) ? T('all agents') : String(S.targets.filter(t => (t.alertIds || []).includes(r.id)).length)),
          h('td', { class: 'actions' }, isAdmin() ? h('button', { class: 'btn sm', onclick: () => ruleDialog(r) }, T('Edit')) : null)))))
        : h('p', { class: 'muted' }, T('No rules.')));
  }).filter(Boolean);
  const evBox = h('div', null, h('p', { class: 'muted' }, T('Loading…')));
  const actBox = h('div', null, h('p', { class: 'muted' }, T('Loading…')));
  GET('/api/alerts/active').then(list => {
    actBox.replaceChildren(list.length ? h('table', { class: 'tbl' },
      h('thead', null, h('tr', null, ...[T('Since'), T('Alert'), T('Applies to'), 'Status', ''].map(x => h('th', null, x)))),
      h('tbody', null, ...list.map(a => h('tr', null,
        h('td', { class: 'mono' }, dateTime(a.since), h('div', { class: 'muted' }, dur(Date.now() - a.since))),
        h('td', null, h('b', null, a.ruleName)), h('td', null, a.targetName),
        h('td', null, a.ack ? h('span', { class: 'pill' }, T('Acknowledged by {0} {1}{2}', a.ack.user, clock(a.ack.t), a.ack.note ? ' – ' + a.ack.note : '')) : a.note ? h('span', { class: 'pill' }, a.note) : h('span', { class: 'pill st-down' }, h('span', { class: 'dot' }), T('Unacknowledged'))),
        h('td', { class: 'actions' }, isAdmin() ? (a.ack ? h('button', { class: 'btn sm', onclick: async () => { await POST('/api/alerts/ack', { key: a.key, undo: true }); delete S.acks[a.key]; renderPage(); } }, T('Undo')) :
          h('button', { class: 'btn sm primary', onclick: async () => { await ackAlert(a.key); renderPage(); } }, T('Acknowledge'))) : null)))))
      : h('p', { class: 'muted' }, T('No active alerts.')));
  }).catch(e => actBox.replaceChildren(h('p', { class: 'form-error' }, e.message)));
  const ocBox = isAdmin() && (S.oncalls || []).length ? h('div', { class: 'section' }, h('div', { class: 'section-head' }, h('h2', null, T('On-call schedules'))),
    h('table', { class: 'tbl' }, h('thead', null, h('tr', null, ...[T('Name'), T('On call now'), T('Next'), T('Channels'), ''].map(x => h('th', null, x)))),
      h('tbody', null, ...S.oncalls.map(o => {
        const nx = (o.upcoming || []).find(u => u.from > Date.now());
        return h('tr', null, h('td', null, h('b', null, o.name), h('div', { class: 'muted' }, o.customerId ? (byId(S.customers, o.customerId) || {}).name : T('All customers'))),
          h('td', null, userName(o.now) || h('span', { class: 'muted' }, '–')),
          h('td', null, nx ? `${userName(nx.userId)} · ${dateTime(nx.from)}` : '–'),
          h('td', null, [o.email ? T('email') : '', o.sms ? 'SMS' : '', o.push ? 'push' : ''].filter(Boolean).join(', ') || '–'),
          h('td', { class: 'actions' }, h('button', { class: 'btn sm', onclick: () => onCallDialog(o) }, T('Edit'))));
      })))) : null;
  page.replaceChildren(head, h('div', { class: 'section', style: 'margin-top:0' }, h('div', { class: 'section-head' }, h('h2', null, T('Active alerts'))), actBox), ...ruleTables, ocBox, h('div', { class: 'section' }, h('div', { class: 'section-head' }, h('h2', null, T('Recent events'))), evBox));
  try {
    const evs = await GET('/api/events?limit=300');
    const kt = { fire: T('Fired'), repeat: T('Ongoing'), clear: T('Cleared'), test: 'Test' };
    evBox.replaceChildren(evs.length ? h('table', { class: 'tbl' },
      h('thead', null, h('tr', null, ...[T('Time'), T('Event'), T('Target'), T('Alert'), T('Detail')].map(x => h('th', null, x)))),
      h('tbody', null, ...evs.map(e => h('tr', null, h('td', { class: 'mono' }, dateTime(e.time)), h('td', null, h('span', { class: 'tag ' + e.kind }, kt[e.kind] || e.kind)),
        h('td', null, e.targetName), h('td', null, e.alertName), h('td', null, e.detail)))))
      : h('p', { class: 'muted' }, T('No alert events yet.')));
  } catch (e) { evBox.replaceChildren(h('p', { class: 'form-error' }, e.message)); }
}
function describe(a) {
  if (a.condition === 'latency') return T('Latency > {0} ms, {1} of {2}', a.threshold, a.count, a.samples);
  if (a.condition === 'loss') return T('Loss > {0} % (last {1})', a.threshold, a.samples);
  if (a.condition === 'offline') return 'Agent offline';
  if (a.condition === 'cpu') return T('CPU > {0} % for {1} min', a.threshold, a.samples);
  if (a.condition === 'mem') return T('Memory > {0} % for {1} min', a.threshold, a.samples);
  if (a.condition === 'disk') return `Disk > ${a.threshold} % full`;
  if (a.condition === 'rssi') return T('Wi-Fi signal < {0} dBm for {1} min', a.threshold, a.samples);
  if (a.condition === 'path') return T('Path to target changes');
  if (a.condition === 'cert') return T('Certificate expires within {0} days', a.threshold);
  if (a.condition === 'vendor') return T('{0} offline ({1} polls)', [T('UniFi/Meraki/Aruba device'), T('Access point'), T('UniFi/Meraki/Aruba device except AP')][a.threshold || 0], a.samples);
  if (a.condition === 'sticky') return T('Sticky client: below {0} dBm, another AP {1} dB stronger for {2} min', a.threshold, a.count, a.samples);
  if (a.condition === 'roamrate') return T('More than {0} roams in {1} min', a.threshold, a.samples);
  if (a.condition === 'snmp') return T('SNMP device not responding ({0} polls)', a.samples);
  if (a.condition === 'ifdown') return T('Watched port down');
  if (a.condition === 'ifutil') return T('Port traffic > {0} % for {1} min', a.threshold, a.samples);
  if (a.condition === 'iferrors') return T('Port errors > {0} per minute', a.threshold);
  if (a.condition === 'speed') return T('Download < {0} Mbit/s ({1} tests in a row)', a.threshold, a.samples);
  return T('Down: {0} of {1} lost', a.count, a.samples);
}
function actionsText(r) {
  const a = [];
  if (r.email) a.push(T('email')); if (r.push) a.push('push'); if (r.sms) a.push('SMS'); if (r.teams) a.push('Teams'); if (r.webhook) a.push('webhook');
  if (r.onCall) { const o = (S.oncalls || []).find(x => x.id === r.onCall); a.push(T('on call') + (o ? ': ' + o.name : '')); }
  if (r.popup) a.push('popup'); if (r.sound) a.push(T('sound'));
  return a.join(', ') || '–';
}

/* ---------------- jourscheman ---------------- */
function userName(id) { const u = (S.userList || []).find(x => x.id === id); return u ? (u.name || u.email) : ''; }
function onCallDialog(o) {
  const r = o ? JSON.parse(JSON.stringify(o)) : { name: '', customerId: '', members: [], start: 0, periodDays: 7, email: true, sms: true, push: true, overrides: [] };
  const users = (S.userList || []).filter(u => u.role === 'admin' || !r.customerId || u.customerId === r.customerId);
  const name = h('input', { type: 'text', value: r.name, placeholder: T('e.g. Weekend on-call') });
  const cust = select([['', T('All customers')], ...S.customers.map(c => [c.id, c.name])], r.customerId || '');
  const startD = new Date(r.start || nextMonday());
  const pad = n => String(n).padStart(2, '0');
  const date = h('input', { type: 'date', value: `${startD.getFullYear()}-${pad(startD.getMonth() + 1)}-${pad(startD.getDate())}` });
  const time = h('input', { type: 'time', value: `${pad(startD.getHours())}:${pad(startD.getMinutes())}` });
  const period = select([[1, T('Every day')], [7, T('Every week')], [14, T('Every two weeks')]], r.periodDays || 7);
  const members = [...(r.members || [])];
  const list = h('div', { class: 'oc-members' });
  const addSel = h('select');
  const renderMembers = () => {
    list.replaceChildren(...(members.length ? members.map((id, i) => h('div', { class: 'oc-member' },
      h('span', { class: 'oc-n' }, String(i + 1)), h('span', { style: 'flex:1' }, userName(id) || id,
        (S.userList.find(u => u.id === id) || {}).phone ? null : h('small', { class: 'muted' }, ' – ' + T('no phone number'))),
      h('button', { class: 'icon-btn', title: T('Move up'), disabled: i === 0, onclick: () => { [members[i - 1], members[i]] = [members[i], members[i - 1]]; renderMembers(); } }, '↑'),
      h('button', { class: 'icon-btn', title: T('Delete'), onclick: () => { members.splice(i, 1); renderMembers(); } }, '✕')))
      : [h('small', { class: 'muted' }, T('Add the people who take turns, in order.'))]));
    addSel.replaceChildren(h('option', { value: '' }, T('+ Add person…')), ...users.filter(u => !members.includes(u.id)).map(u => h('option', { value: u.id }, u.name || u.email)));
  };
  addSel.onchange = () => { if (addSel.value) { members.push(addSel.value); renderMembers(); } };
  renderMembers();
  const flags = { email: r.email, sms: r.sms, push: r.push };
  const ovBox = h('div');
  const overrides = [...(r.overrides || [])];
  const renderOv = () => ovBox.replaceChildren(...overrides.map((ov, i) => h('div', { class: 'row', style: 'margin-bottom:4px' },
    h('span', { class: 'mono' }, `${dateTime(ov.from)} – ${dateTime(ov.to)}`), h('b', null, userName(ov.userId)),
    h('button', { class: 'icon-btn', onclick: () => { overrides.splice(i, 1); renderOv(); } }, '✕'))),
    h('button', { class: 'btn sm', onclick: () => overrideDialog(ov => { overrides.push(ov); renderOv(); }, users) }, T('+ Replacement (e.g. holiday)')));
  renderOv();
  formDialog({ title: o ? T('Edit on-call schedule') : T('New on-call schedule'), wide: true, fields: [
    field(T('Name'), name), field(T('Applies to'), cust),
    field(T('First shift starts'), h('div', { class: 'row' }, date, time)), field(T('Shift length'), period),
    field(T('People in turn'), h('div', null, list, addSel), null, true),
    h('div', { class: 'field full' }, h('span', null, T('The person on call gets')), h('div', { class: 'checks' },
      check(T('Email'), flags.email, e => { flags.email = e.target.checked; }), check('SMS', flags.sms, e => { flags.sms = e.target.checked; }),
      check(T('Push'), flags.push, e => { flags.push = e.target.checked; }))),
    field(T('Replacements'), ovBox, null, true),
    h('small', { class: 'field full muted' }, T('Choose the schedule under "Notify on call" in an alert rule. Phone numbers and push are set per user (account menu → Notifications).')),
    o ? h('div', { class: 'field full' }, h('button', { class: 'btn danger sm', onclick: async e => {
      if (!await confirmDialog(T('Delete'), T('Delete "{0}"?', o.name), T('Delete'))) return;
      await DEL('/api/oncall/' + o.id); e.target.closest('.backdrop')._close(); renderPage(); } }, T('Delete'))) : null],
    onSave: async () => {
      const start = new Date(date.value + 'T' + (time.value || '08:00')).getTime();
      const body = Object.assign(r, { name: name.value, customerId: cust.value, start, periodDays: +period.value, members, overrides }, flags);
      if (o) await PUT('/api/oncall/' + o.id, body); else await POST('/api/oncall', body);
      renderPage();
    } });
}
function nextMonday() { const d = new Date(); d.setHours(8, 0, 0, 0); d.setDate(d.getDate() + ((8 - d.getDay()) % 7 || 7)); return d.getTime(); }
function overrideDialog(onAdd, users) {
  const today = new Date().toISOString().slice(0, 10);
  const from = h('input', { type: 'date', value: today }), to = h('input', { type: 'date', value: today });
  const who = select(users.map(u => [u.id, u.name || u.email]), users.length ? users[0].id : '');
  formDialog({ title: T('Replacement'), saveLabel: T('Add'), fields: [field(T('From'), from), field(T('To (inclusive)'), to), field(T('On call instead'), who, null, true)],
    onSave: async () => { onAdd({ from: new Date(from.value + 'T00:00').getTime(), to: new Date(to.value + 'T00:00').getTime() + 86400000, userId: who.value }); } });
}

/* ---------------- användare ---------------- */
async function renderUsers(page) {
  const head = h('div', { class: 'page-head' }, h('h1', null, T('Users')), h('div', { class: 'tools' }, h('button', { class: 'btn primary', onclick: () => userDialog(null) }, T('+ New user'))));
  const box = h('div');
  page.replaceChildren(head, box, h('p', { class: 'ro-note', style: 'margin-top:12px' }, T('Customer users only see their own customer and cannot change anything.')));
  const us = await GET('/api/users');
  box.replaceChildren(h('table', { class: 'tbl' }, h('thead', null, h('tr', null, ...[T('Name'), T('Email'), T('Role'), T('Customer'), T('2FA'), ''].map(x => h('th', null, x)))),
    h('tbody', null, ...us.map(u => h('tr', null, h('td', null, u.name || '–'), h('td', null, u.email),
      h('td', null, u.role === 'admin' ? T('Administrator') : T('Customer (read)')), h('td', null, u.customerId ? ((byId(S.customers, u.customerId) || {}).name || '–') : '–'),
      h('td', null, u.totp ? h('span', { class: 'pill st-up' }, h('span', { class: 'dot' }), T('On')) : h('span', { class: 'muted' }, T('Off'))),
      h('td', { class: 'actions' }, h('button', { class: 'btn sm', onclick: () => userDialog(u) }, T('Edit'))))))));
}

/* ---------------- inställningar ---------------- */
async function renderSettings(page) {
  const st = await GET('/api/settings');
  GET('/api/backups').then(b => {
    const box = $('#backupList'); if (!box) return;
    box.replaceChildren(h('span', null, T('Saved copies')), b.files.length ? h('div', { class: 'checks', style: 'flex-direction:column' }, ...b.files.map(f =>
      h('a', { href: '/api/backup?file=' + encodeURIComponent(f.name) }, `${f.name} (${(f.size / 1048576).toFixed(1)} MB)`))) : h('small', null, T('None yet.')),
      h('small', null, T('Folder: {0}. Restore by stopping the server and replacing pingvakt.db with a copy.', b.dir)));
  }).catch(() => {});
  const pub = h('input', { type: 'text', value: st.publicUrl, placeholder: S.serverUrl });
  const lang = select([['en', 'English'], ['sv', 'Svenska']], st.language || 'en');
  const repName = h('input', { type: 'text', value: st.reportName || '', placeholder: T('Your company name') });
  const rep = num(st.reportSec, 2, 300), raw = num(st.rawDays, 1, 365), agg = num(st.aggDays, 7, 3650);
  const autoUpd = h('input', { type: 'checkbox', checked: !st.noAutoUpdate });
  const backupKeep = num(st.backupKeep, 0, 365);
  const m = st.monitor;
  const win = num(m.statsWindow, 5, 3600), warnMs = num(m.warnMs, 0, 10000), warnLoss = num(m.warnLossPct, 0, 100, .5), downAfter = num(m.downAfter, 1, 100);
  const sm = st.smtp;
  const sHost = h('input', { type: 'text', value: sm.host, placeholder: 'smtp.example.com' }), sPort = num(sm.port || 587, 1, 65535);
  const sSec = select([['starttls', 'STARTTLS (587)'], ['tls', 'TLS/SSL (465)'], ['none', T('No encryption (25)')]], sm.security || 'starttls');
  const sUser = h('input', { type: 'text', value: sm.username }), sPass = h('input', { type: 'password', placeholder: st.hasPassword ? T('•••••••• (saved – leave empty to keep)') : '', autocomplete: 'new-password' });
  const sFrom = h('input', { type: 'email', value: sm.from, placeholder: 'pingvakt@example.com' }), sMin = num(sm.minMinutes ?? 5, 0, 1440);
  sSec.onchange = () => { sPort.value = { starttls: 587, tls: 465, none: 25 }[sSec.value]; };
  const smtpVal = () => ({ host: sHost.value.trim(), port: +sPort.value, security: sSec.value, username: sUser.value.trim(), password: sPass.value, from: sFrom.value.trim(), minMinutes: +sMin.value });
  const testTo = h('input', { type: 'email', placeholder: 'recipient@example.com' }), testRes = h('span');
  // push och SMS
  const nt = st.notify || {};
  const inp = (v, ph, type) => h('input', { type: type || 'text', value: v || '', placeholder: ph || '', autocomplete: 'off' });
  const ntUrl = inp(nt.ntfyUrl, 'https://ntfy.sh'), ntTopic = inp(nt.ntfyTopic, T('e.g. pingvakt-alerts-x7k2')), ntTok = inp(nt.ntfyToken, T('Only for a protected ntfy server'), 'password');
  const poTok = inp(nt.pushoverToken, T('Application API token'), 'password'), poUser = inp(nt.pushoverUser, T('User or group key (optional)'));
  const smsProv = select([['', T('Off')], ['46elks', '46elks'], ['twilio', 'Twilio']], nt.smsProvider || '');
  const smsUser = inp(nt.smsUser), smsPass = inp(nt.smsPass, '', 'password'), smsFrom = inp(nt.smsFrom, 'PingVakt'), smsMin = num(nt.smsMinMinutes || 10, 1, 1440);
  const smsUserF = field('', smsUser), smsPassF = field('', smsPass);
  const syncSms = () => {
    const v = smsProv.value;
    [smsUserF, smsPassF].forEach(f => { f.style.display = v ? '' : 'none'; });
    smsUserF.firstChild.textContent = v === 'twilio' ? 'Account SID' : T('API username');
    smsPassF.firstChild.textContent = v === 'twilio' ? 'Auth Token' : T('API password');
  };
  smsProv.onchange = syncSms; syncSms();
  const notifyVal = () => ({ ntfyUrl: ntUrl.value.trim(), ntfyTopic: ntTopic.value.trim(), ntfyToken: ntTok.value, pushoverToken: poTok.value.trim(), pushoverUser: poUser.value.trim(),
    smsProvider: smsProv.value, smsUser: smsUser.value.trim(), smsPass: smsPass.value, smsFrom: smsFrom.value.trim(), smsMinMinutes: +smsMin.value });
  const notifyFields = [
    h('div', { class: 'section-title' }, T('Push and SMS')),
    field(T('ntfy server'), ntUrl, T('Free app for Android/iPhone. Empty = ntfy.sh')), field(T('ntfy topic for all alerts'), ntTopic, T('Optional – users can also have their own topic')),
    field(T('ntfy token'), ntTok), h('span'),
    field(T('Pushover application token'), poTok, T('Create an application at pushover.net')), field(T('Pushover user/group key'), poUser),
    field(T('SMS provider'), smsProv), field(T('SMS sender'), smsFrom, T('Name (max 11 characters) or phone number')),
    smsUserF, smsPassF,
    field(T('At most one SMS per alert and target every (minutes)'), smsMin),
    h('small', { class: 'field full muted' }, T('Push in the web app needs no account: each user turns it on under the account menu → Notifications.'))];
  // inloggning
  const au = st.auth || {};
  const mfa = select([['', T('Optional')], ['admins', T('Required for administrators')], ['all', T('Required for everyone')]], au.requireMfa || '');
  const ms = au.microsoft || {}, gg = au.google || {};
  const msOn = h('input', { type: 'checkbox', checked: !!ms.enabled }), msId = inp(ms.clientId, 'Application (client) ID'), msSec = inp(ms.secret, 'Client secret', 'password'), msTen = inp(ms.tenant, T('Directory (tenant) ID or "organizations"'));
  const gOn = h('input', { type: 'checkbox', checked: !!gg.enabled }), gId = inp(gg.clientId, 'Client ID'), gSec = inp(gg.secret, 'Client secret', 'password');
  const base = (st.publicUrl || location.origin).replace(/\/$/, '');
  const authVal = () => ({ requireMfa: mfa.value, microsoft: { enabled: msOn.checked, clientId: msId.value.trim(), secret: msSec.value, tenant: msTen.value.trim() },
    google: { enabled: gOn.checked, clientId: gId.value.trim(), secret: gSec.value } });
  const authFields = [
    h('div', { class: 'section-title' }, T('Sign-in')),
    field(T('Two-factor authentication'), mfa, T('Users without it must turn it on at their next sign-in. Sign-in with Microsoft/Google counts as two-factor.')), h('span'),
    h('div', { class: 'field full' }, h('label', { class: 'check' }, msOn, h('b', null, T('Sign in with Microsoft (Entra ID)')))),
    field('Client ID', msId), field('Client secret', msSec), field(T('Tenant'), msTen, T('Your directory ID, or "organizations" for any work account')),
    field(T('Redirect URI'), copyBox(base + '/auth/microsoft/callback')),
    h('div', { class: 'field full' }, h('label', { class: 'check' }, gOn, h('b', null, T('Sign in with Google')))),
    field('Client ID', gId), field('Client secret', gSec),
    field(T('Redirect URI'), copyBox(base + '/auth/google/callback'), null, true),
    h('small', { class: 'field full muted' }, T('Only existing PingVakt users can sign in – the email address must match the account. Set Public address first so the redirect URI is correct.'))];
  const msg = h('span');
  page.replaceChildren(h('div', { class: 'page-head' }, h('h1', null, T('Settings'))),
    h('div', { class: 'modal', style: 'width:auto;max-width:860px;max-height:none' }, h('div', { class: 'modal-body', style: 'padding-top:16px' }, h('div', { class: 'form' },
      h('div', { class: 'section-title' }, 'Server'),
      field(T('Default language'), lang, T('For users, reports and status pages that have no language of their own, and for alert texts and emails.')),
      h('span'),
      field(T('Public address'), pub, T('Used in install commands and emails. Empty = the address you are browsing'), true),
      field(T('Company name on reports and status pages'), repName, null, true),
      field(T('Agents report every (seconds)'), rep), h('span'),
      field(T('Keep raw data (days)'), raw, T('Every single ping')), field(T('Keep minute values (days)'), agg, T('Avg/min/max/loss per minute')),
      h('div', { class: 'field full' }, h('label', { class: 'check' }, autoUpd, T('Update agents automatically when the server gets a new version'))),
      h('div', { class: 'section-title' }, T('Backups')),
      field(T('Keep nightly copies (count)'), backupKeep, T('Taken every night at 03:15. 0 = off.')),
      h('div', { class: 'field' }, h('span', null, T('Download')), h('div', { class: 'row' }, h('a', { class: 'btn', href: '/api/backup' }, T('Download backup now')))),
      h('div', { class: 'field full', id: 'backupList' }),
      h('div', { class: 'section-title' }, T('Measurement (sent to the agents)')),
      field(T('Statistics window (samples)'), win), field(T('Down after (lost in a row)'), downAfter),
      field(T('Latency warning threshold (ms)'), warnMs), field(T('Loss warning threshold (%)'), warnLoss),
      h('div', { class: 'section-title' }, T('Email for alerts')),
      field(T('SMTP server'), sHost), field(T('Security'), sSec), field('Port', sPort), field(T('Sender'), sFrom),
      field(T('Username'), sUser), field(T('Password'), sPass), field(T('At most one email per alert every (minutes)'), sMin),
      h('div', { class: 'field full' }, h('span', null, T('Test email')), h('div', { class: 'row' }, testTo, h('button', { class: 'btn', onclick: async () => {
        testRes.className = ''; testRes.replaceChildren(h('span', { class: 'spin' }), T(' Sending…'));
        try { await POST('/api/settings/test-email', { to: testTo.value, smtp: smtpVal(), newPassword: sPass.value !== '' }); testRes.className = 'form-ok'; testRes.textContent = T('The test email was sent.'); }
        catch (e) { testRes.className = 'form-error'; testRes.textContent = e.message; }
      } }, T('Send test email'))), testRes),
      ...notifyFields, ...authFields,
      h('div', { class: 'section-title', id: 'tplSection' }, T('Target templates')),
      h('div', { class: 'field full', id: 'tplBox' }))),
    h('div', { class: 'modal-foot' }, h('div', { class: 'left' }, msg, h('span', { class: 'muted' }, T('PingVakt server {0}', S.version))),
      h('button', { class: 'btn primary', onclick: async () => {
        msg.className = ''; msg.textContent = '';
        try {
          const langChanged = lang.value !== (st.language || 'en');
          await PUT('/api/settings', { language: lang.value, publicUrl: pub.value.trim(), reportSec: +rep.value, rawDays: +raw.value, aggDays: +agg.value,
            noAutoUpdate: !autoUpd.checked, backupKeep: +backupKeep.value, reportName: repName.value.trim(),
            monitor: { statsWindow: +win.value, warnMs: +warnMs.value, warnLossPct: +warnLoss.value, downAfter: +downAfter.value, historySize: m.historySize },
            smtp: smtpVal(), newPassword: sPass.value !== '', notify: notifyVal(), auth: authVal() });
          msg.className = 'form-ok'; msg.textContent = T('Saved.'); await loadState();
          if (langChanged && !S.me.lang) location.reload();
        } catch (e) { msg.className = 'form-error'; msg.textContent = e.message; }
      } }, T('Save')))));
  templateSection().then(b => { const el = $('#tplBox'); if (el) el.replaceChildren(b); });
}

/* ---------------- dialoger ---------------- */
function modal({ title, body, foot, wide, onClose }) {
  const close = () => { bd.remove(); onClose && onClose(); };
  const bd = h('div', { class: 'backdrop' }, h('div', { class: 'modal' + (wide ? ' wide' : ''), role: 'dialog', 'aria-label': title },
    h('div', { class: 'modal-head' }, h('h3', null, title), h('button', { class: 'icon-btn', title: T('Close'), onclick: close }, '✕')),
    h('div', { class: 'modal-body' }, body), foot ? h('div', { class: 'modal-foot' }, foot) : null));
  bd._close = close;
  bd.addEventListener('mousedown', e => { if (e.target === bd) close(); });
  $('#modalRoot').append(bd);
  const f = bd.querySelector('input:not([type=checkbox]),select,textarea'); if (f) setTimeout(() => f.focus(), 30);
  return close;
}
function confirmDialog(title, text, okLabel) {
  return new Promise(resolve => {
    let done = false;
    const fin = v => { if (!done) { done = true; resolve(v); close(); } };
    const close = modal({ title, body: h('p', null, text),
      foot: [h('button', { class: 'btn', onclick: () => fin(false) }, T('Cancel')), h('button', { class: 'btn primary', onclick: () => fin(true) }, okLabel || 'OK')],
      onClose: () => { if (!done) { done = true; resolve(false); } } });
  });
}
const field = (label, input, hint, full) => h('label', { class: 'field' + (full ? ' full' : '') }, h('span', null, label), input, hint ? h('small', null, hint) : null);
const check = (label, checked, onchange) => h('label', { class: 'check' }, h('input', { type: 'checkbox', checked, onchange }), label);
const select = (opts, value) => h('select', null, ...opts.map(([v, l]) => h('option', { value: v, selected: String(v) === String(value) }, l)));
const num = (value, min, max, step) => h('input', { type: 'number', value, min, max, step: step || 1 });
function formDialog({ title, fields, onSave, saveLabel, wide }) {
  const err = h('span', { class: 'form-error' });
  const close = modal({ title, wide, body: h('div', { class: 'form' }, ...fields),
    foot: [h('div', { class: 'left' }, err), h('button', { class: 'btn', onclick: () => close() }, T('Cancel')),
      h('button', { class: 'btn primary', onclick: async () => { err.textContent = ''; try { await onSave(); close(); } catch (e) { err.textContent = e.message; } } }, saveLabel || T('Save'))] });
  return close;
}

async function afterChange() {
  await loadState();
  renderNav();
  const keep = S.selected;
  if (!document.querySelector('.backdrop') || S.view.name !== 'customer') renderPage();
  else renderPage();
  if (keep && byId(S.targets, keep)) { S.selected = keep; tiles.forEach((el, tid) => el.classList.toggle('selected', tid === keep)); updateDrawerHead(); }
  else if (keep) closeDrawer();
}

function langDialog() {
  const lang = select([['', T('Server default')], ['en', 'English'], ['sv', 'Svenska']], S.me.lang || '');
  formDialog({ title: 'Language / Språk', saveLabel: T('Save'), fields: [field('Language / Språk', lang, null, true)],
    onSave: async () => { await PUT('/api/me/lang', { lang: lang.value }); location.reload(); } });
}

function customerDialog(c) {
  const name = h('input', { type: 'text', value: c ? c.name : '', placeholder: T('Customer name') });
  const notes = h('input', { type: 'text', value: c ? c.notes : '', placeholder: T('Optional note, e.g. contact person') });
  const lang = select([['', T('Server default')], ...[['en', 'English'], ['sv', 'Svenska']]], c ? c.lang || '' : '');
  formDialog({ title: c ? T('Edit customer') : T('New customer'), fields: [field(T('Name'), name, null, true), field(T('Note'), notes, null, true),
    field(T('Language for reports and status page'), lang),
    c ? null : h('small', { class: 'field full' }, T('A first site ("Head office") is created automatically.'))],
    onSave: async () => {
      const body = Object.assign({}, c || {}, { name: name.value, notes: notes.value, lang: lang.value });
      const r = c ? await PUT('/api/customers/' + c.id, body) : await POST('/api/customers', body);
      await afterChange(); if (!c) location.hash = '#/c/' + r.id;
    } });
}
async function deleteCustomer(c) {
  if (!await confirmDialog(T('Delete customer'), T('Delete "{0}" with all sites, agents, targets, measurements, alert rules and customer users? This cannot be undone.', c.name), T('Delete'))) return;
  await DEL('/api/customers/' + c.id); location.hash = '#/'; await afterChange();
}
function siteDialog(customerId, site) {
  const name = h('input', { type: 'text', value: site ? site.name : '', placeholder: T('e.g. Warehouse Borås') });
  formDialog({ title: site ? T('Rename site') : T('New site'), fields: [field(T('Name'), name, null, true)],
    onSave: async () => { site ? await PUT('/api/sites/' + site.id, { name: name.value }) : await POST('/api/sites', { customerId, name: name.value }); await afterChange(); } });
}
async function deleteSite(site) {
  if (!await confirmDialog(T('Delete site'), T('Delete "{0}" with its agents, targets and measurements?', site.name), T('Delete'))) return;
  await DEL('/api/sites/' + site.id); await afterChange();
}
function agentDialog(customerId, siteId) {
  const sites = S.sites.filter(s => s.customerId === customerId);
  const site = select(sites.map(s => [s.id, s.name]), siteId || (sites[0] || {}).id);
  const name = h('input', { type: 'text', placeholder: T('e.g. Raspberry Pi in the server room') });
  formDialog({ title: T('New agent'), saveLabel: T('Create agent'), fields: [field('Site', site, null, true), field(T('Name'), name, null, true),
    h('small', { class: 'field full' }, T('After the agent is created, a token and ready-made install commands are shown. The same token can be used in the desktop app (Settings → Server).'))],
    onSave: async () => { const r = await POST('/api/agents', { siteId: site.value, name: name.value }); await afterChange(); installDialog(r.agent, r.token); } });
}
function editAgentDialog(a) {
  const cust = customerOfAgent(a);
  const name = h('input', { type: 'text', value: a.name });
  const site = select(S.sites.filter(s => s.customerId === cust).map(s => [s.id, s.name]), a.siteId);
  formDialog({ title: T('Edit agent'), fields: [field(T('Name'), name, null, true), field('Site', site, null, true)],
    onSave: async () => { await PUT('/api/agents/' + a.id, { name: name.value, siteId: site.value }); await afterChange(); } });
}
function copyBox(text) {
  const b = h('button', { class: 'btn sm', onclick: async () => { try { await navigator.clipboard.writeText(text); b.textContent = T('Copied'); setTimeout(() => { b.textContent = T('Copy'); }, 1500); } catch (e) { /* ignorera */ } } }, T('Copy'));
  return h('div', { class: 'codebox' }, text, b);
}
function installDialog(a, token) {
  const url = S.serverUrl;
  const tok = token || '<TOKEN>';
  const body = h('div', { class: 'form' },
    token ? h('div', { class: 'notice field full' }, T('The token is only shown now. Copy it or run the command directly. It can be replaced later from the agent\'s menu ("New token").'))
      : h('div', { class: 'notice field full' }, T('The token is only shown when it is created. Create a new token from the agent\'s menu if you need to reinstall.')),
    token ? field('Token', copyBox(token), null, true) : null,
    field(T('Linux (including Raspberry Pi) and macOS – in the terminal'), copyBox(`curl -fsSL ${url}/install.sh | sudo sh -s -- --token ${tok}`), null, true),
    field(T('Windows – in PowerShell as administrator'), copyBox(`& ([scriptblock]::Create((irm ${url}/install.ps1))) -Token ${tok}`), null, true),
    field(T('The PingVakt desktop app'), h('div', null, T('Settings → Server: enter the address '), h('code', null, url), T(' and the token.')), null, true),
    h('small', { class: 'field full' }, T('The agent connects outbound to the server over HTTPS – no ports need to be opened at the customer. The agent files are downloaded from this server.')));
  modal({ title: T('Install the agent "{0}"', a.name), wide: true, body, foot: [h('button', { class: 'btn primary', onclick: e => e.target.closest('.backdrop')._close() }, T('Done'))] });
}

function addTargetsDialog(customerId) {
  const ags = S.agents.filter(a => customerOfAgent(a) === customerId && a.kind !== 'desktop');
  if (!ags.length) { toast({ kind: 'info', title: T('No agent'), text: T('Create an agent first – the agent is what measures the targets.') }); return agentDialog(customerId); }
  const agent = select(ags.map(a => [a.id, `${a.name} (${(siteOf(a.siteId) || {}).name || ''})`]), ags[0].id);
  const hosts = h('textarea', { rows: 4, placeholder: '192.168.1.1, 10.0.0.1\nprinter.local\nweb.customer.com:443', spellcheck: 'false' });
  const kind = select(KINDS, 'icmp');
  const port = num(443, 1, 65535);
  const group = h('input', { type: 'text', placeholder: T('Optional, e.g. Access points') });
  const kw = h('input', { type: 'text', placeholder: T('Optional – e.g. Sign in') });
  const dns = h('input', { type: 'text', placeholder: T('Empty = the agent\'s normal DNS, e.g. 8.8.8.8') });
  const pf = field('Port', port), kf = field(T('Required text'), kw), df = field(T('DNS server'), dns);
  kind.onchange = () => { pf.style.display = kind.value === 'tcp' ? '' : 'none'; kf.style.display = kind.value === 'http' ? '' : 'none'; df.style.display = kind.value === 'dns' ? '' : 'none';
    hosts.placeholder = { http: T('https://web.customer.com\nhttps://intranet.local/login'), dns: T('customer.com\nmail.customer.com') }[kind.value] || '192.168.1.1, 10.0.0.1\nprinter.local\nweb.customer.com:443'; };
  kind.onchange();
  formDialog({ title: T('Add targets'), saveLabel: T('Add'), fields: [field(T('Measuring agent'), agent, null, true),
    field(T('Hosts'), hosts, T('One or more, separated by commas or line breaks. Write host:port for TCP.'), true),
    h('div', { class: 'field full' }, h('div', { class: 'row' }, h('button', { class: 'btn sm', type: 'button', onclick: e => { const ag = ags.find(x => x.id === agent.value); e.target.closest('.backdrop')._close(); if (ag) openScan(ag); } }, T('Find devices with a network scan…')),
      h('button', { class: 'btn sm', type: 'button', onclick: e => { e.target.closest('.backdrop')._close(); templateDialog(customerId, agent.value); } }, T('Use a template…')))),
    field(T('Check type'), kind), pf, kf, df, field(T('Group'), group, null, true)],
    onSave: async () => { await POST('/api/targets', { agentId: agent.value, hosts: hosts.value, kind: kind.value, port: +port.value, group: group.value, keyword: kw.value.trim(), dnsServer: dns.value.trim() }); await afterChange(); } });
}

function editTargetDialog(id) {
  const t = JSON.parse(JSON.stringify(byId(S.targets, id) || {}));
  if (!t.id) return;
  const cust = customerOfTarget(t);
  const fromApp = t.source === 'agent';
  const host = h('input', { type: 'text', value: t.host, disabled: fromApp });
  const name = h('input', { type: 'text', value: t.name, disabled: fromApp });
  const kind = select(KINDS, t.kind); kind.disabled = fromApp;
  const kw = h('input', { type: 'text', value: t.keyword || '', disabled: fromApp, placeholder: T('Optional') });
  const dns = h('input', { type: 'text', value: t.dnsServer || '', disabled: fromApp, placeholder: T('Empty = the agent\'s normal DNS') });
  const port = num(t.port || 443, 1, 65535); port.disabled = fromApp;
  const interval = select(INTERVALS.map(v => [v, v >= 60000 ? `${v / 60000} min` : v >= 1000 ? `${v / 1000} s` : `${v} ms`]), t.intervalMs); interval.disabled = fromApp;
  const timeout = num(t.timeoutMs, 100, 60000, 100); timeout.disabled = fromApp;
  const size = num(t.size, 1, 65000); size.disabled = fromApp;
  const ipMode = select([['auto', T('Automatic')], ['4', 'IPv4'], ['6', 'IPv6']], t.ipMode); ipMode.disabled = fromApp;
  const group = h('input', { type: 'text', value: t.group || '', disabled: fromApp });
  const warn = h('input', { type: 'number', value: t.warnMs || '', min: 0, placeholder: T('Global ({0} ms)', S.monitor.warnMs) });
  const ags = S.agents.filter(a => customerOfAgent(a) === cust && a.kind !== 'desktop');
  const agent = select(ags.map(a => [a.id, a.name]), t.agentId); agent.disabled = fromApp;
  const traceMin = select([[0, T('Off')], [5, T('Every 5 minutes')], [15, T('Every 15 minutes')], [30, T('Every 30 minutes')], [60, T('Every hour')]], t.traceMin || 0);
  const depends = select([['', T('– none –')], ...S.targets.filter(x => x.id !== t.id && customerOfTarget(x) === cust).map(x => [x.id, `${displayName(x)} (${x.host})`])], t.dependsOn || '');
  const sel = new Set(t.alertIds || []);
  const rules = S.rules.filter(r => ['down', 'latency', 'loss'].includes(r.condition) && (!r.customerId || r.customerId === cust));
  const pf = field('Port', port), kf = field(T('Required text'), kw), df = field(T('DNS server'), dns), sf = field(T('Packet size (bytes)'), size), ipf = field(T('IP version'), ipMode);
  const syncKind = () => { const k = kind.value; pf.style.display = k === 'tcp' ? '' : 'none'; kf.style.display = k === 'http' ? '' : 'none'; df.style.display = k === 'dns' ? '' : 'none';
    sf.style.display = k === 'icmp' ? '' : 'none'; ipf.style.display = k === 'http' || k === 'dns' ? 'none' : ''; }; kind.onchange = syncKind; syncKind();
  formDialog({ title: T('Edit target'), fields: [
    fromApp ? h('div', { class: 'notice field full' }, T('This target comes from a desktop app. Host and measurement settings are changed in the app – here you choose alerts and the warning threshold.')) : null,
    field(T('Host / address'), host), field(T('Name'), name), field(T('Check type'), kind), pf, kf, df, fromApp ? null : field('Agent', agent),
    field(T('Interval'), interval), field(T('Timeout (ms)'), timeout), sf, ipf,
    field(T('Group'), group), field(T('Latency warning threshold (ms)'), warn, T('Empty = global value')),
    field(T('Path monitoring'), traceMin, T('Runs traceroute regularly from the agent and alerts when the path to the target changes (rule "Path changes").'), true),
    field(T('Depends on'), depends, T('No alerts are sent for this target when the target it sits behind is down – you get one alert instead of many.'), true),
    h('div', { class: 'section-title' }, T('Alerts')),
    h('div', { class: 'field full' }, rules.length ? h('div', { class: 'checks' }, ...rules.map(r => check(r.name, sel.has(r.id), e => e.target.checked ? sel.add(r.id) : sel.delete(r.id))))
      : h('small', null, T('There are no alert rules for the customer.')))],
    onSave: async () => {
      Object.assign(t, { host: host.value.trim(), name: name.value.trim(), kind: kind.value, port: +port.value || 0, intervalMs: +interval.value, timeoutMs: +timeout.value,
        size: +size.value, ipMode: ipMode.value, keyword: kw.value.trim(), dnsServer: dns.value.trim(), group: group.value.trim(), warnMs: +warn.value || 0, alertIds: [...sel], agentId: agent.value || t.agentId, dependsOn: depends.value, traceMin: +traceMin.value });
      await PUT('/api/targets/' + id, t); S.spark[id] = S.spark[id] || []; await afterChange(); if (S.selected === id) loadChart();
    } });
}

function ruleDialog(rule) {
  const r = rule ? JSON.parse(JSON.stringify(rule)) : { id: '', name: '', condition: 'down', threshold: 0, count: 3, samples: 3, popup: true, sound: false, soundName: 'alarm', email: false, emailTo: '', webhook: false, hookUrl: '', repeatMin: 0, onClear: true, customerId: S.view.name === 'customer' ? S.view.id : '' };
  const name = h('input', { type: 'text', value: r.name, placeholder: T('Rule name') });
  const cust = select([['', T('All customers')], ...S.customers.map(c => [c.id, c.name])], r.customerId || '');
  const cond = select([['down', T('Down – lost packets')], ['latency', T('High latency')], ['loss', T('Packet loss in percent')], ['offline', 'Agent offline'],
    ['cpu', T('Agent CPU high')], ['mem', T('Agent memory high')], ['disk', T('Agent disk full')], ['rssi', T('Agent Wi-Fi signal weak')], ['cert', T('HTTPS certificate expiring')], ['path', T('Path (traceroute) changes')],
    ['snmp', T('SNMP device not responding')], ['ifdown', T('Watched port down')], ['ifutil', T('Port traffic high')], ['iferrors', T('Port errors')], ['speed', T('Speed test slow')],
    ['sticky', T('Sticky Wi-Fi client')], ['roamrate', T('Too many roams')], ['vendor', T('UniFi/Meraki/Aruba device offline')]], r.condition);
  const vendorWhich = select([[0, T('All devices')], [1, T('Only access points')], [2, T('All except access points')]], r.condition === 'vendor' ? r.threshold : 0);
  const thr = num(r.threshold, -120, 100000, .5), cnt = num(r.count, 1, 3600), smp = num(r.samples, 1, 3600);
  const condRow = h('div', { class: 'field full' });
  const syncCond = () => {
    const c = cond.value;
    if (c === 'down') condRow.replaceChildren(h('span', null, T('Condition')), h('div', { class: 'row' }, cnt, h('span', null, T('lost of the last')), smp, h('span', null, T('packets'))));
    if (c === 'latency') condRow.replaceChildren(h('span', null, T('Condition')), h('div', { class: 'row' }, cnt, h('span', null, T('of the last')), smp, h('span', null, T('over')), thr, h('span', null, 'ms')));
    if (c === 'loss') condRow.replaceChildren(h('span', null, T('Condition')), h('div', { class: 'row' }, h('span', null, T('Loss over')), thr, h('span', null, T('% of the last')), smp, h('span', null, T('packets'))));
    if (c === 'cpu' || c === 'mem') condRow.replaceChildren(h('span', null, T('Condition')), h('div', { class: 'row' }, h('span', null, c === 'cpu' ? T('CPU over') : T('Memory over')), thr, h('span', null, T('% for')), smp, h('span', null, T('minutes in a row'))),
      h('small', null, T('Applies to all agents of the customer') + (cust.value ? '' : T(' – and the PingVakt server itself')) + '.'));
    if (c === 'disk') condRow.replaceChildren(h('span', null, T('Condition')), h('div', { class: 'row' }, h('span', null, T('Any disk more than')), thr, h('span', null, '% full')),
      h('small', null, T('Applies to all agents of the customer') + (cust.value ? '' : T(' – and the PingVakt server itself')) + '.'));
    if (c === 'rssi') condRow.replaceChildren(h('span', null, T('Condition')), h('div', { class: 'row' }, h('span', null, T('Signal below')), thr, h('span', null, T('dBm for')), smp, h('span', null, T('minutes in a row'))),
      h('small', null, T('Applies to all agents and desktop apps of the customer connected via Wi-Fi. −67 dBm is enough for calls and video; below −75 dBm problems are common.')));
    if (c === 'path') condRow.replaceChildren(h('small', null, T('Triggers when traceroute shows a new path twice in a row. Turn on "Path monitoring" on the targets to watch.')));
    if (c === 'cert') condRow.replaceChildren(h('span', null, T('Condition')), h('div', { class: 'row' }, h('span', null, T('Certificate expires within')), thr, h('span', null, T('days'))),
      h('small', null, T('Applies to all HTTPS targets of the customer.')));
    if (c === 'snmp') condRow.replaceChildren(h('span', null, T('Condition')), h('div', { class: 'row' }, h('span', null, T('No response')), smp, h('span', null, T('polls in a row'))),
      h('small', null, T('Applies to all SNMP devices of the customer.')));
    if (c === 'ifdown') condRow.replaceChildren(h('small', null, T('Triggers when a port that is ticked under "Watch" in the SNMP device view goes down while it is enabled.')));
    if (c === 'ifutil') condRow.replaceChildren(h('span', null, T('Condition')), h('div', { class: 'row' }, h('span', null, T('Traffic over')), thr, h('span', null, T('% of the port speed for')), smp, h('span', null, T('minutes'))),
      h('small', null, T('Applies to watched ports, or all ports of the device when none is watched.')));
    if (c === 'iferrors') condRow.replaceChildren(h('span', null, T('Condition')), h('div', { class: 'row' }, h('span', null, T('More than')), thr, h('span', null, T('errors per minute on a port'))),
      h('small', null, T('Applies to all ports of the customer\'s SNMP devices.')));
    if (c === 'speed') condRow.replaceChildren(h('span', null, T('Condition')), h('div', { class: 'row' }, h('span', null, T('Download below')), thr, h('span', null, T('Mbit/s in')), smp, h('span', null, T('tests in a row'))),
      h('small', null, T('Turn on automatic speed tests on the agent (CPU, memory and disk view).')));
    if (c === 'vendor') condRow.replaceChildren(h('span', null, T('Condition')), h('div', { class: 'row' }, vendorWhich, h('span', null, T('offline for')), smp, h('span', null, T('polls in a row'))),
      h('small', null, T('Applies to the integrations of the customer (customer menu → Add integration).')));
    if (c === 'sticky') condRow.replaceChildren(h('span', null, T('Condition')), h('div', { class: 'row' }, h('span', null, T('Signal below')), thr, h('span', null, T('dBm while another AP with the same SSID is')), cnt, h('span', null, T('dB stronger for')), smp, h('span', null, T('minutes'))),
      h('small', null, T('Needs automatic Wi-Fi scanning on the agent (CPU, memory and disk view → Wi-Fi networks nearby).')));
    if (c === 'roamrate') condRow.replaceChildren(h('span', null, T('Condition')), h('div', { class: 'row' }, h('span', null, T('More than')), thr, h('span', null, T('roams in')), smp, h('span', null, T('minutes'))),
      h('small', null, T('Applies to all agents and desktop apps of the customer connected via Wi-Fi. Many roams often mean overlapping cells with similar signal or a client that roams too eagerly.')));
    if (c === 'offline') condRow.replaceChildren(h('small', null, T('Triggers when an agent has not reported for {0} seconds. Applies to all agents of the customer.', Math.max(30, S.reportSec * 3))));
  };
  cond.onchange = () => {
    const c = cond.value;
    if (c === 'latency') thr.value = +thr.value > 0 ? thr.value : 150;
    if (c === 'loss') thr.value = +thr.value > 0 && +thr.value <= 100 ? thr.value : 5;
    if (c === 'cpu' || c === 'mem' || c === 'disk') { thr.value = +thr.value >= 50 && +thr.value <= 100 ? thr.value : 90; smp.value = c === 'disk' ? 1 : 5; }
    if (c === 'down') { cnt.value = 3; smp.value = 3; }
    if (c === 'rssi') { thr.value = +thr.value < 0 ? thr.value : -75; smp.value = 5; }
    if (c === 'cert') { thr.value = 14; smp.value = 1; cnt.value = 1; }
    if (c === 'snmp') smp.value = 3;
    if (c === 'sticky') { thr.value = +thr.value < 0 ? thr.value : -70; cnt.value = 10; smp.value = 10; }
    if (c === 'roamrate') { thr.value = 10; smp.value = 60; }
    if (c === 'vendor') smp.value = 2;
    if (c === 'ifutil') { thr.value = +thr.value > 0 && +thr.value <= 100 ? thr.value : 90; smp.value = 10; }
    if (c === 'iferrors') thr.value = +thr.value > 0 ? thr.value : 10;
    if (c === 'speed') { thr.value = +thr.value > 0 ? thr.value : 50; smp.value = 2; }
    syncCond();
  };
  cust.addEventListener('change', syncCond);
  syncCond();
  const emailTo = h('input', { type: 'text', value: r.emailTo, placeholder: T('one@example.com, two@example.com') });
  const hook = h('input', { type: 'text', value: r.hookUrl, placeholder: T('https://… (Teams, Slack, your own service)') });
  const sound = select([['beep', T('Beep')], ['alarm', T('Alerts')], ['chime', T('Chime')]], r.soundName || 'alarm');
  const repeat = num(r.repeatMin, 0, 1440);
  const smsTo = h('input', { type: 'text', value: r.smsTo || '', placeholder: T('070-123 45 67, +46701234567') });
  const teamsUrl = h('input', { type: 'text', value: r.teamsUrl || '', placeholder: T('Teams workflow URL (Post to a channel when a webhook request is received)') });
  const onCall = select([['', T('– none –')], ...(S.oncalls || []).map(o => [o.id, o.name])], r.onCall || '');
  const flags = { popup: r.popup, sound: r.sound, email: r.email, webhook: r.webhook, onClear: r.onClear, push: !!r.push, sms: !!r.sms, teams: !!r.teams };
  const box = (label, key) => check(label, flags[key], e => { flags[key] = e.target.checked; });
  const collect = () => Object.assign(r, { name: name.value.trim(), customerId: cust.value, condition: cond.value, threshold: cond.value === 'vendor' ? +vendorWhich.value : +thr.value, count: +cnt.value, samples: +smp.value,
    emailTo: emailTo.value, hookUrl: hook.value.trim(), soundName: sound.value, repeatMin: +repeat.value,
    smsTo: smsTo.value.trim(), teamsUrl: teamsUrl.value.trim(), onCall: onCall.value }, flags);
  const testRes = h('span');
  const err = h('span', { class: 'form-error' });
  const close = modal({ title: rule ? T('Edit alert rule') : T('New alert rule'), wide: true,
    body: h('div', { class: 'form' }, field(T('Name'), name), field(T('Applies to'), cust), field(T('Type'), cond, null, true), condRow,
      h('div', { class: 'section-title' }, T('Actions')),
      h('div', { class: 'field full' }, h('div', { class: 'checks', style: 'flex-direction:column' },
        h('div', { class: 'row' }, box(T('Send email to'), 'email'), emailTo),
        h('div', { class: 'row' }, box(T('Push to phones'), 'push'), h('small', { class: 'muted' }, T('ntfy/Pushover from Settings, and everyone who turned on notifications under their account'))),
        h('div', { class: 'row' }, box('SMS', 'sms'), smsTo),
        h('div', { class: 'row' }, box('Teams', 'teams'), teamsUrl),
        h('div', { class: 'row' }, box('Webhook', 'webhook'), hook),
        h('div', { class: 'row' }, h('span', null, T('Notify on call')), onCall),
        box(T('Popup in the browser for anyone with PingVakt open'), 'popup'),
        h('div', { class: 'row' }, box(T('Sound in the browser'), 'sound'), sound, h('button', { class: 'btn sm', onclick: () => playSound(sound.value) }, '▶')))),
      h('div', { class: 'section-title' }, T('When')),
      h('div', { class: 'field full' }, h('div', { class: 'checks', style: 'flex-direction:column' },
        h('div', { class: 'row' }, h('span', null, T('Repeat every')), repeat, h('span', null, T('minutes while the alert is active (0 = no)'))),
        box(T('Also notify when the alert clears'), 'onClear'))),
      rule && ['down', 'latency', 'loss'].includes(cond.value) ? h('div', { class: 'field full' }, h('div', { class: 'row' },
        h('span', { class: 'muted', style: 'flex:1' }, T('Attach the rule to targets:')),
        h('button', { class: 'btn sm', onclick: async () => assign(true) }, T('Attach to all targets')),
        h('button', { class: 'btn sm', onclick: async () => assign(false) }, T('Detach from all')))) : null),
    foot: [h('div', { class: 'left' }, err, testRes,
      h('button', { class: 'btn', onclick: async () => { err.textContent = ''; testRes.textContent = ''; try { await POST('/api/rules/test', collect()); testRes.className = 'form-ok'; testRes.textContent = T('Test sent.'); } catch (e) { err.textContent = e.message; } } }, T('Test')),
      rule ? h('button', { class: 'btn danger', onclick: async () => { if (!await confirmDialog(T('Delete rule'), T('Delete "{0}"?', rule.name), T('Delete'))) return; await DEL('/api/rules/' + rule.id); close(); await afterChange(); } }, T('Delete')) : null),
      h('button', { class: 'btn', onclick: () => close() }, T('Cancel')),
      h('button', { class: 'btn primary', onclick: async () => { err.textContent = ''; try { await POST('/api/rules', collect()); close(); await afterChange(); } catch (e) { err.textContent = e.message; } } }, T('Save'))] });
  async function assign(on) {
    collect(); await POST('/api/rules', r);
    const ids = S.targets.filter(t => !r.customerId || customerOfTarget(t) === r.customerId).map(t => t.id);
    const res = await POST(`/api/rules/${r.id}/assign`, { targetIds: ids, on });
    testRes.className = 'form-ok'; testRes.textContent = T('{0} targets updated.', res.changed); await loadState();
  }
}

function userDialog(u) {
  const email = h('input', { type: 'email', value: u ? u.email : '' }), name = h('input', { type: 'text', value: u ? u.name : '' });
  const role = select([['admin', T('Administrator (everything)')], ['viewer', T('Customer (read-only)')]], u ? u.role : 'viewer');
  const cust = select(S.customers.map(c => [c.id, c.name]), u ? u.customerId : (S.customers[0] || {}).id);
  const pw = h('input', { type: 'password', autocomplete: 'new-password', placeholder: u ? T('Leave empty to keep') : T('At least 8 characters') });
  const phone = h('input', { type: 'tel', value: u ? u.phone || '' : '', placeholder: '070-123 45 67' });
  const cf = field(T('Customer'), cust); const sync = () => { cf.style.display = role.value === 'viewer' ? '' : 'none'; }; role.onchange = sync; sync();
  const fields = [field(T('Email'), email), field(T('Name'), name), field(T('Role'), role), cf, field(T('Password'), pw), field(T('Mobile phone (SMS, on call)'), phone)];
  if (u && u.totp) fields.push(h('div', { class: 'field full' }, h('button', { class: 'btn', onclick: async e => {
    if (!await confirmDialog(T('Reset two-factor authentication'), T('{0} can then sign in with just the password and needs to enable two-factor authentication again.', u.email), T('Reset'))) return;
    await POST(`/api/users/${u.id}/totp/reset`); e.target.closest('.backdrop')._close(); renderPage();
  } }, T('Reset two-factor authentication'))));
  if (u && u.id !== S.me.id) fields.push(h('div', { class: 'field full' }, h('button', { class: 'btn danger', onclick: async e => {
    if (!await confirmDialog(T('Delete user'), T('Delete {0}?', u.email), T('Delete'))) return; await DEL('/api/users/' + u.id); e.target.closest('.backdrop')._close(); renderPage();
  } }, T('Delete the user'))));
  formDialog({ title: u ? T('Edit user') : T('New user'), fields, onSave: async () => {
    const body = { email: email.value, name: name.value, role: role.value, customerId: role.value === 'viewer' ? cust.value : '', password: pw.value, phone: phone.value };
    u ? await PUT('/api/users/' + u.id, body) : await POST('/api/users', body);
    renderPage();
  } });
}

/* ---------------- egna aviseringar och webbpush ---------------- */
function urlB64ToUint8(s) { const p = '='.repeat((4 - s.length % 4) % 4); const b = atob((s + p).replace(/-/g, '+').replace(/_/g, '/')); return Uint8Array.from(b, c => c.charCodeAt(0)); }
async function pushSubscription() {
  if (!('serviceWorker' in navigator) || !('PushManager' in window)) return null;
  const reg = await navigator.serviceWorker.getRegistration('/');
  return reg ? reg.pushManager.getSubscription() : null;
}
async function enablePush(vapid) {
  if (!('serviceWorker' in navigator) || !('PushManager' in window)) throw new Error(T('This browser does not support push notifications. On iPhone: share → Add to Home Screen, then open PingVakt from the home screen.'));
  const perm = await Notification.requestPermission();
  if (perm !== 'granted') throw new Error(T('Notifications were not allowed in the browser.'));
  const reg = await navigator.serviceWorker.register('/sw.js', { scope: '/' });
  await navigator.serviceWorker.ready;
  let sub = await reg.pushManager.getSubscription();
  if (!sub) sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlB64ToUint8(vapid) });
  await POST('/api/push/subscribe', sub.toJSON());
}
async function notifyDialog() {
  const n = await GET('/api/me/notify');
  const phone = h('input', { type: 'tel', value: n.phone, placeholder: '070-123 45 67' });
  const po = h('input', { type: 'text', value: n.pushover, placeholder: T('Your Pushover user key') });
  const nt = h('input', { type: 'text', value: n.ntfy, placeholder: T('e.g. pingvakt-jonas-x7k2 (hard to guess)') });
  const devState = h('span', { class: 'muted' });
  const devBtn = h('button', { class: 'btn' });
  const res = h('span');
  const refreshDev = async () => {
    const sub = await pushSubscription().catch(() => null);
    devState.textContent = sub ? T('On for this device') : T('Off for this device');
    devBtn.textContent = sub ? T('Turn off on this device') : T('Turn on on this device');
    devBtn.onclick = async () => {
      res.className = ''; res.textContent = '';
      try {
        if (sub) { await POST('/api/push/unsubscribe', { endpoint: sub.endpoint }); await sub.unsubscribe(); }
        else await enablePush(n.vapidPublic);
      } catch (e) { res.className = 'form-error'; res.textContent = e.message; }
      refreshDev();
    };
  };
  refreshDev();
  formDialog({ title: T('Notifications'), saveLabel: T('Save'), fields: [
    h('div', { class: 'field full' }, h('span', null, T('Push to this phone or computer')), h('div', { class: 'row' }, devBtn, devState),
      h('small', null, T('Works in Chrome, Edge, Firefox and Safari. On iPhone, first add PingVakt to the home screen (share → Add to Home Screen) and turn it on from there.'))),
    field(T('Mobile phone (SMS, on call)'), phone, n.hasSMS ? null : T('SMS is not configured on the server yet.')),
    n.hasPushover ? field('Pushover', po) : null,
    field(T('ntfy topic'), nt, T('Subscribe to the same topic in the ntfy app ({0}).', n.ntfyUrl || 'https://ntfy.sh')),
    h('div', { class: 'field full' }, h('div', { class: 'row' }, h('button', { class: 'btn', onclick: async () => {
      res.className = ''; res.textContent = '';
      try { await PUT('/api/me/notify', { phone: phone.value, pushover: po.value, ntfy: nt.value }); const r = await POST('/api/me/notify/test'); res.className = 'form-ok'; res.textContent = T('Test sent to {0} channels.', r.sent); }
      catch (e) { res.className = 'form-error'; res.textContent = e.message; }
    } }, T('Save and send a test')), res)),
    h('small', { class: 'field full muted' }, T('You get push for alert rules with "Push to phones" for the customers you can see, and SMS/push when you are on call.'))],
    onSave: async () => { await PUT('/api/me/notify', { phone: phone.value, pushover: po.value, ntfy: nt.value }); } });
}

function passwordDialog() {
  const cur = h('input', { type: 'password', autocomplete: 'current-password' }), nw = h('input', { type: 'password', autocomplete: 'new-password' });
  formDialog({ title: T('Change password'), fields: [field(T('Current password'), cur, null, true), field(T('New password'), nw, T('At least 8 characters'), true)],
    onSave: async () => { await POST('/api/me/password', { current: cur.value, new: nw.value }); toast({ kind: 'info', title: T('The password has been changed') }); } });
}

/* ---------------- meny, toast, ljud ---------------- */
let openMenuEl = null;
function closeMenu() { if (openMenuEl) { openMenuEl.remove(); openMenuEl = null; } }
function showMenu(anchor, items) {
  closeMenu();
  const m = h('div', { class: 'menu' }, ...items.filter(Boolean).map(it => it === '-' ? h('hr') : h('button', { class: it.danger ? 'danger' : '', onclick: () => { closeMenu(); it.action(); } }, it.label)));
  document.body.append(m);
  const r = anchor.getBoundingClientRect(), mw = m.offsetWidth, mh = m.offsetHeight;
  m.style.left = Math.max(8, Math.min(r.right - mw, window.innerWidth - mw - 8)) + 'px';
  m.style.top = (r.bottom + mh + 8 > window.innerHeight ? r.top - mh - 4 : r.bottom + 4) + 'px';
  openMenuEl = m;
}
document.addEventListener('mousedown', e => { if (openMenuEl && !openMenuEl.contains(e.target)) closeMenu(); });
document.addEventListener('keydown', e => {
  if (e.key !== 'Escape') return;
  if (openMenuEl) return closeMenu();
  const m = document.querySelector('#modalRoot .backdrop:last-child'); if (m) return m._close();
  if (S.selected) closeDrawer();
});

function toast({ kind = 'info', title, text, time, sticky, target, ackKey }) {
  const box = $('#toasts');
  const close = h('button', { class: 'icon-btn', title: T('Close'), style: 'grid-row:1;grid-column:3' }, '✕');
  const el = h('div', { class: 'toast k-' + kind, style: 'grid-template-columns:1fr auto auto' }, h('b', null, title), h('span', { class: 't-time' }, clock(time || Date.now())), close,
    text ? h('div', { class: 't-text' }, text) : null);
  close.onclick = e => { e.stopPropagation(); el.remove(); };
  if (ackKey) el.append(h('div', { style: 'grid-column:1/-1;margin-top:6px' }, h('button', { class: 'btn sm', onclick: async e => { e.stopPropagation(); await ackAlert(ackKey); el.remove(); } }, T('Acknowledge'))));
  if (target) { el.style.cursor = 'pointer'; el.onclick = () => { openDrawer(target); el.remove(); }; }
  box.prepend(el);
  while (box.children.length > 6) box.lastChild.remove();
  if (!sticky) setTimeout(() => el.remove(), 8000);
}

let audioCtx;
function playSound(name) {
  try {
    audioCtx = audioCtx || new (window.AudioContext || window.webkitAudioContext)();
    const ctx = audioCtx, t0 = ctx.currentTime;
    const tone = (f, s, len, type = 'sine', vol = .18) => {
      const o = ctx.createOscillator(), g = ctx.createGain(); o.type = type; o.frequency.value = f;
      g.gain.setValueAtTime(0, t0 + s); g.gain.linearRampToValueAtTime(vol, t0 + s + .01); g.gain.exponentialRampToValueAtTime(.0001, t0 + s + len);
      o.connect(g).connect(ctx.destination); o.start(t0 + s); o.stop(t0 + s + len + .05);
    };
    if (name === 'alarm') { for (let i = 0; i < 3; i++) { tone(880, i * .36, .16, 'square', .09); tone(660, i * .36 + .18, .16, 'square', .09); } }
    else if (name === 'chime') { tone(659, 0, .5); tone(784, .12, .5); tone(1047, .24, .7); }
    else tone(1000, 0, .18);
  } catch (e) { /* inget ljud */ }
}
