/* PingVakt-server – webbgränssnitt */
'use strict';

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
    const err = new Error((data && data.error) || `HTTP ${res.status}`);
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
const STATUS_TEXT = { up: 'Uppe', warn: 'Varning', down: 'Nere', paused: 'Pausad', pending: 'Väntar', offline: 'Agent offline' };
const RANGES = [[900, '15 min'], [3600, '1 h'], [21600, '6 h'], [86400, '24 h'], [604800, '7 d'], [2592000, '30 d']];
const INTERVALS = [500, 1000, 2000, 5000, 10000, 30000, 60000];
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
    serverUrl: st.serverUrl, version: st.version,
  });
}

/* ---------------- start ---------------- */
window.addEventListener('DOMContentLoaded', start);
async function start() {
  try { S.me = await GET('/api/me'); }
  catch (e) { if (e.status === 401) return e.data && e.data.setup ? showSetup() : showLogin(); throw e; }
  await boot();
}

async function boot() {
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
  const email = h('input', { type: 'email', placeholder: 'E-post', autocomplete: 'username' });
  const pw = h('input', { type: 'password', placeholder: 'Lösenord', autocomplete: 'current-password' });
  const err = h('div', { class: 'form-error' });
  const form = h('form', { onsubmit: async e => {
    e.preventDefault(); err.textContent = '';
    try { S.me = await POST('/api/login', { email: email.value, password: pw.value }); location.hash = ''; await boot(); }
    catch (x) { err.textContent = x.message; }
  } }, email, pw, err, h('button', { class: 'btn primary', type: 'submit' }, 'Logga in'));
  authShell('Logga in', 'Logga in för att se övervakningen', form);
  setTimeout(() => email.focus(), 30);
}
function showSetup() {
  const name = h('input', { type: 'text', placeholder: 'Ditt namn' });
  const email = h('input', { type: 'email', placeholder: 'E-post', autocomplete: 'username' });
  const pw = h('input', { type: 'password', placeholder: 'Lösenord (minst 8 tecken)', autocomplete: 'new-password' });
  const err = h('div', { class: 'form-error' });
  const form = h('form', { onsubmit: async e => {
    e.preventDefault(); err.textContent = '';
    try { S.me = await POST('/api/setup', { name: name.value, email: email.value, password: pw.value }); await boot(); }
    catch (x) { err.textContent = x.message; }
  } }, name, email, pw, err, h('button', { class: 'btn primary', type: 'submit' }, 'Skapa administratör'));
  authShell('Kom igång', 'Skapa det första administratörskontot', form);
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
        h('div', { class: 'who' }, h('b', null, S.me.name || S.me.email), h('small', null, isAdmin() ? 'Administratör' : 'Kund (läsbehörighet)')),
        h('button', { class: 'icon-btn', title: 'Konto', onclick: e => showMenu(e.currentTarget, [
          { label: 'Byt lösenord…', action: passwordDialog },
          { label: 'Logga ut', action: async () => { await POST('/api/logout'); location.reload(); } },
        ]) }, '⋯'))),
    h('div', { class: 'mainwrap' },
      h('div', { class: 'page', id: 'page' }),
      h('section', { id: 'drawer', class: 'drawer hidden' })));
  renderNav();
}

function renderNav() {
  const route = location.hash || '#/';
  const link = (href, label, extra) => h('a', { href, class: route === href || (href !== '#/' && route.startsWith(href)) ? 'on' : '' }, label, extra || null);
  const items = [link('#/', 'Översikt')];
  items.push(h('div', { class: 'navhead' }, 'Kunder'));
  for (const c of S.customers) {
    const cnt = counts(S.targets.filter(t => customerOfTarget(t) === c.id));
    const st = cnt.down ? 'down' : cnt.offline ? 'offline' : cnt.warn ? 'warn' : 'up';
    const a = link('#/c/' + c.id, h('span', { class: 'dot st-' + st }), h('span', { class: 'cnt' }, String(cnt.total)));
    a.classList.add('custlink');
    a.insertBefore(document.createTextNode(c.name), a.lastChild);
    items.push(a);
  }
  if (isAdmin() && !S.customers.length) items.push(h('a', { href: '#/', class: 'custlink', onclick: e => { e.preventDefault(); customerDialog(); } }, '+ Ny kund'));
  items.push(h('div', { class: 'navhead' }, 'Hantera'));
  items.push(link('#/agents', 'Agenter'));
  items.push(link('#/alerts', 'Larm'));
  if (isAdmin()) { items.push(link('#/users', 'Användare')); items.push(link('#/settings', 'Inställningar')); }
  $('#nav').replaceChildren(...items);
}

const plural = (n, one, many) => `${n} ${n === 1 ? one : many}`;

function counts(targets) {
  const c = { total: targets.length, up: 0, warn: 0, down: 0, offline: 0, paused: 0, pending: 0 };
  for (const t of targets) { const s = (S.stats[t.id] || {}).status || 'pending'; if (s in c) c[s]++; }
  return c;
}

/* ---------------- routing ---------------- */
function route() {
  closeDrawer();
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
  return renderOverview(page);
}

function connBadge() {
  return h('span', { class: 'conn', id: 'connBadge' }, h('span', { class: 'dot ' + (S.connected ? 'st-up' : 'st-offline') }), S.connected ? 'Live' : 'Ansluter…');
}

/* ---------------- översikt ---------------- */
function renderOverview(page) {
  const all = counts(S.targets);
  const head = h('div', { class: 'page-head' }, h('h1', null, 'Översikt'), connBadge(),
    h('div', { class: 'tools' }, isAdmin() ? h('button', { class: 'btn primary', onclick: () => customerDialog() }, '+ Ny kund') : null));
  const chip = (k, label) => h('span', { class: 'chip st-' + k + (all[k] ? '' : ' zero'), title: label }, h('span', { class: 'dot' }), `${all[k]} ${label}`);
  const summary = h('div', { class: 'summary', style: 'margin-bottom:16px;flex-wrap:wrap' },
    chip('up', 'uppe'), chip('warn', 'varning'), chip('down', 'nere'), chip('offline', 'offline'), chip('paused', 'pausade'));
  const cards = h('div', { class: 'cards' }, ...S.customers.map(c => {
    const ts = S.targets.filter(t => customerOfTarget(t) === c.id);
    const cnt = counts(ts);
    const ags = S.agents.filter(a => customerOfAgent(a) === c.id);
    const on = ags.filter(a => (S.live[a.id] || {}).online).length;
    const st = cnt.down ? 'down' : cnt.offline ? 'offline' : cnt.warn ? 'warn' : 'up';
    const n = (k, cls) => h('span', { class: cnt[k] ? '' : 'zero' }, h('span', { class: 'dot st-' + cls }), String(cnt[k]));
    return h('div', { class: 'card st-' + st, onclick: () => { location.hash = '#/c/' + c.id; } },
      h('h3', null, c.name),
      h('div', { class: 'meta' }, `${plural(S.sites.filter(s => s.customerId === c.id).length, 'site', 'siter')} · ${on}/${ags.length} agenter online · ${ts.length} mål`),
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
      h('span', { class: 'pill' }, STATUS_TEXT[st.status]), h('span', { class: 'muted', style: 'min-width:90px;text-align:right' }, st.err || dur(Date.now() - st.since)));
  }) : [h('div', { class: 'empty-ok' }, S.targets.length ? 'Inga problem just nu.' : 'Inga mål ännu.')]));
  const content = [head, summary];
  if (!S.customers.length) {
    content.push(h('div', { class: 'empty', style: 'display:block' }, h('h2', null, 'Välkommen till PingVakt-servern'),
      h('p', null, isAdmin() ? 'Börja med att skapa en kund. Därefter lägger du till en agent på kundens nätverk och väljer vad den ska övervaka.' : 'Inga kunder är kopplade till ditt konto.'),
      isAdmin() ? h('div', { class: 'quick' }, h('button', { class: 'btn primary', onclick: () => customerDialog() }, '+ Skapa första kunden')) : null));
  } else {
    content.push(cards, h('div', { class: 'section' }, h('div', { class: 'section-head' }, h('h2', null, 'Just nu')), plist));
  }
  page.replaceChildren(...content);
}

/* ---------------- kund ---------------- */
const tiles = new Map();
function renderCustomer(page, id) {
  const c = byId(S.customers, id);
  if (!c) { page.replaceChildren(h('p', { class: 'muted' }, 'Kunden finns inte.')); return; }
  const sites = S.sites.filter(s => s.customerId === id);
  const head = h('div', { class: 'page-head' },
    h('div', null, h('h1', null, c.name), c.notes ? h('div', { class: 'sub' }, c.notes) : null), connBadge(),
    h('div', { class: 'tools' }, isAdmin() ? [
      h('button', { class: 'btn primary', onclick: () => addTargetsDialog(id) }, '+ Lägg till mål'),
      h('button', { class: 'btn', onclick: () => agentDialog(id) }, '+ Ny agent'),
      h('button', { class: 'btn', onclick: () => siteDialog(id) }, '+ Ny site'),
      h('button', { class: 'btn ghost', onclick: e => showMenu(e.currentTarget, [
        { label: 'Redigera kund…', action: () => customerDialog(c) },
        { label: 'Larm för kunden…', action: () => { location.hash = '#/alerts'; } },
        '-',
        { label: 'Ta bort kund…', danger: true, action: () => deleteCustomer(c) },
      ]) }, '⋯')] : null));
  const sections = sites.map(site => {
    const ags = S.agents.filter(a => a.siteId === site.id);
    const ts = S.targets.filter(t => ags.some(a => a.id === t.agentId));
    const grid = h('div', { class: 'grid inline' });
    for (const t of ts) { const el = buildTile(t); tiles.set(t.id, el); grid.append(el); updateTile(t); }
    const agentChips = h('div', { class: 'agents-row' }, ...ags.map(a => agentChip(a)),
      !ags.length ? h('span', { class: 'muted' }, isAdmin() ? 'Ingen agent på den här siten ännu. ' : 'Ingen agent.') : null,
      !ags.length && isAdmin() ? h('button', { class: 'btn sm', onclick: () => agentDialog(id, site.id) }, '+ Lägg till agent') : null);
    return h('div', { class: 'section' },
      h('div', { class: 'section-head' }, h('h2', null, site.name), h('span', { class: 'muted' }, `${ts.length} mål`),
        isAdmin() ? h('div', { class: 'tools' }, h('button', { class: 'btn ghost sm', onclick: e => showMenu(e.currentTarget, [
          { label: 'Byt namn…', action: () => siteDialog(id, site) },
          { label: 'Ta bort site…', danger: true, action: () => deleteSite(site) },
        ]) }, '⋯')) : null),
      agentChips,
      ts.length ? grid : (ags.length ? h('p', { class: 'muted' }, isAdmin() ? 'Inga mål ännu – klicka på "Lägg till mål".' : 'Inga mål.') : null));
  });
  page.replaceChildren(head, ...sections);
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
  const kind = a.kind === 'desktop' ? 'skrivbordsapp' : 'agent';
  const el = h('span', { class: 'agent-chip st-' + st, 'data-agent': a.id, title: `${a.hostname || ''} ${a.os || ''} ${a.version ? 'v' + a.version : ''} ${a.ip || ''}`.trim() },
    h('span', { class: 'dot' }), h('b', null, a.name),
    h('small', null, st === 'pending' ? 'väntar på första kontakt' : st === 'up' ? `${kind} · online` : `offline ${dur(Date.now() - seen)}`),
    isAdmin() ? h('button', { class: 'icon-btn', title: 'Agent', onclick: e => agentMenu(a, e.currentTarget) }, '⋯') : null);
  return el;
}

function agentMenu(a, anchor) {
  showMenu(anchor, [
    { label: 'Installera / visa kommandon…', action: () => installDialog(a, null) },
    { label: 'Ny token…', action: async () => {
      if (!await confirmDialog('Ny token', `Den gamla token för "${a.name}" slutar fungera direkt. Agenten måste registreras om med den nya.`, 'Skapa ny token')) return;
      const r = await POST(`/api/agents/${a.id}/token`); await afterChange(); installDialog(a, r.token);
    } },
    { label: 'Byt namn / flytta…', action: () => editAgentDialog(a) },
    '-',
    { label: 'Ta bort agent…', danger: true, action: async () => {
      if (!await confirmDialog('Ta bort agent', `Ta bort "${a.name}" och alla dess mål och mätdata?`, 'Ta bort')) return;
      await DEL('/api/agents/' + a.id); await afterChange();
    } },
  ]);
}

/* ---------------- rutor ---------------- */
function displayName(t) { return t.name || t.host; }
function kindText(t) { return t.kind === 'tcp' ? `TCP ${t.port}` : 'ICMP'; }
function warnFor(t) { return t.warnMs > 0 ? t.warnMs : (S.monitor.warnMs || 0); }

function buildTile(t) {
  const el = h('div', { class: 'tile st-pending', 'data-id': t.id },
    h('div', { class: 'tile-head' }, h('span', { class: 'dot' }),
      h('div', { class: 'names' }, h('div', { class: 'name' }), h('div', { class: 'host' })),
      isAdmin() ? h('button', { class: 'icon-btn menu-btn', title: 'Meny' }, '⋯') : null),
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
  const items = [{ label: 'Visa graf', action: () => openDrawer(id) }, { label: 'Redigera…', action: () => editTargetDialog(id) }];
  if (t.source !== 'agent') {
    items.push({ label: t.paused ? 'Starta' : 'Pausa', action: () => togglePause(t) });
    items.push('-', { label: 'Ta bort', danger: true, action: async () => {
      if (!await confirmDialog('Ta bort mål', `Ta bort ${displayName(t)} och dess mätdata?`, 'Ta bort')) return;
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
  el.classList.toggle('alerting', Object.keys(S.active).some(k => k.endsWith('|' + t.id)));
  el.classList.toggle('selected', S.selected === t.id);
  const nm = el.querySelector('.name');
  nm.textContent = displayName(t); nm.title = displayName(t);
  const a = agentOf(t.agentId);
  const addr = st.addr && st.addr !== t.host ? ` → ${st.addr}` : '';
  el.querySelector('.host').textContent = `${t.host}${addr} · ${kindText(t)}${a ? ' · ' + a.name : ''}`;
  const lat = el.querySelector('.lat');
  const lost = st.window > 0 && st.last < 0;
  lat.textContent = status === 'paused' || status === 'offline' ? '–' : st.window ? (lost ? 'förlorad' : fmtMs(st.last)) : '…';
  lat.classList.toggle('lost', lost && status !== 'offline');
  el.querySelector('.unit').style.visibility = (!st.window || lost || status === 'paused' || status === 'offline') ? 'hidden' : '';
  tileText(el, st);
  el.querySelector('.tile-stats').replaceChildren(
    h('span', null, h('i', null, 'snitt'), fmtMs(st.avg)),
    h('span', null, h('i', null, 'max'), fmtMs(st.max)),
    h('span', null, h('i', null, 'jitter'), fmtMs(st.jitter)),
    h('span', { class: st.loss > 0 ? 'bad' : '' }, h('i', null, 'förl'), (st.loss || 0).toFixed(st.loss && st.loss < 10 ? 1 : 0) + '%'));
  drawSpark(el.querySelector('canvas'), S.spark[t.id] || [], warnFor(t));
}

function tileText(el, st) {
  let txt;
  switch (st.status) {
    case 'up': txt = 'Uppe ' + dur(Date.now() - st.since); break;
    case 'down': txt = (st.err || 'Nere') + ' · ' + dur(Date.now() - st.since); break;
    case 'warn': txt = st.consecLost ? (st.err || 'Paket förlorat') : st.loss > (S.monitor.warnLossPct || 0) ? `Förlust ${st.loss}%` : 'Hög latens'; break;
    case 'paused': txt = 'Pausad'; break;
    case 'offline': txt = 'Agenten är offline'; break;
    default: txt = 'Väntar på data…';
  }
  const e = el.querySelector('.state-text');
  e.textContent = txt; e.title = st.err || txt;
}

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
    else if (m.type === 'alert') onAlert(m.event);
  };
}
function updateConn() { const b = $('#connBadge'); if (b) b.replaceWith(connBadge()); }

let structTimer = null;
function onTick(m) {
  Object.assign(S.stats, m.stats || {});
  Object.assign(S.live, m.agents || {});
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
  else for (const t of S.targets) if (tiles.has(t.id)) updateTile(t);
  document.querySelectorAll('.agent-chip').forEach(ch => {
    const a = agentOf(ch.dataset.agent); const was = ch.className;
    if (a) { const n = agentChip(a); if (n.className !== was) ch.replaceWith(n); }
  });
  if (S.selected) updateDrawerHead();
  renderNavThrottled();
}
let navT = 0;
function renderNavThrottled() { const n = Date.now(); if (n - navT > 5000) { navT = n; renderNav(); } }

function onAlert(ev) {
  const title = { fire: 'Larm', repeat: 'Larm pågår', clear: 'Larm upphört', test: 'Testlarm' }[ev.kind] || 'Larm';
  if (ev.popup || ev.kind === 'test') toast({ kind: ev.kind, title: `${title}: ${ev.targetName}`, text: `${ev.alertName} – ${ev.detail}`, time: ev.time, sticky: ev.kind === 'fire', target: byId(S.targets, ev.targetId) ? ev.targetId : null });
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
        isAdmin() ? h('button', { class: 'btn ghost sm', onclick: () => editTargetDialog(S.selected) }, 'Redigera') : null,
        h('button', { class: 'btn ghost sm', onclick: () => { const to = Date.now(); location.href = `/api/targets/${S.selected}/export.csv?from=${to - S.range * 1000}&to=${to}`; } }, 'Exportera CSV'),
        h('button', { class: 'icon-btn', title: 'Stäng (Esc)', onclick: closeDrawer }, '✕'))),
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
  $('#dHost').textContent = `${t.host}${st.addr && st.addr !== t.host ? ' → ' + st.addr : ''} · ${kindText(t)} · ${a ? a.name : ''} · var ${t.intervalMs >= 1000 ? t.intervalMs / 1000 + ' s' : t.intervalMs + ' ms'}`;
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
    rows = [row('Paket', String(n)), row('Förlorade', `${lost} (${n ? +(lost * 100 / n).toFixed(2) : 0}%)`), row('Snitt', fmtMs(avg) + ' ms'),
      row('Min', fmtMs(sorted[0]) + ' ms'), row('Max', fmtMs(sorted[sorted.length - 1]) + ' ms'), row('Median', fmtMs(pct(.5)) + ' ms'),
      row('95:e pct', fmtMs(pct(.95)) + ' ms'), row('Jitter', fmtMs(jit) + ' ms')];
  } else {
    const n = S.chartN || [];
    let tot = 0, lostTot = 0, sum = 0, okN = 0, mn = Infinity, mx = -Infinity;
    for (let i = 0; i < d[0].length; i++) {
      const cnt = n[i] || 0, lossP = d[4][i] || 0; tot += cnt; const l = Math.round(cnt * lossP / 100); lostTot += l;
      if (d[1][i] != null) { sum += d[1][i] * (cnt - l); okN += cnt - l; }
      if (d[2][i] != null) mn = Math.min(mn, d[2][i]);
      if (d[3][i] != null) mx = Math.max(mx, d[3][i]);
    }
    rows = [row('Paket', String(tot)), row('Förlorade', `${lostTot} (${tot ? +(lostTot * 100 / tot).toFixed(2) : 0}%)`),
      row('Snitt', fmtMs(okN ? sum / okN : NaN) + ' ms'), row('Min', fmtMs(isFinite(mn) ? mn : NaN) + ' ms'), row('Max', fmtMs(isFinite(mx) ? mx : NaN) + ' ms'),
      row('Upplösning', S.chartBucket >= 3600000 ? S.chartBucket / 3600000 + ' h' : S.chartBucket / 60000 + ' min')];
  }
  $('#dStats').replaceChildren(h('dl', null, row('Status', STATUS_TEXT[st.status] || '–'), row('Sedan', st.since ? dur(Date.now() - st.since) : '–'),
    row('Senaste', st.last >= 0 ? fmtMs(st.last) + ' ms' : 'förlorad'), ...rows), ...(st.err ? [h('div', { class: 'err' }, st.err)] : []));
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
    { label: 'Tid', value: (u, v) => v == null ? '–' : new Date(v * 1000).toLocaleString('sv-SE') },
    { label: 'Latens', stroke: c.accent, width: 1.5, fill: hexA(c.accent, .12), points: { show: false }, spanGaps: false, value: (u, v, si, idx) => idx == null ? '–' : v == null ? 'förlorad' : fmtMs(v) + ' ms' },
    { label: 'Förlorad', scale: 'loss', stroke: c.lost, width: 0, points: { show: false }, paths: () => null, value: (u, v, si, idx) => idx == null ? '–' : v ? 'ja' : 'nej' },
  ] : [
    { label: 'Tid', value: (u, v) => v == null ? '–' : new Date(v * 1000).toLocaleString('sv-SE') },
    { label: 'Snitt', stroke: c.accent, width: 1.6, points: { show: false }, value: val },
    { label: 'Min', stroke: hexA(c.accent, .35), width: 1, points: { show: false }, value: val },
    { label: 'Max', stroke: hexA(c.accent, .35), width: 1, points: { show: false }, value: val },
    { label: 'Förlust', scale: 'loss', stroke: c.lost, width: 0, points: { show: false }, paths: () => null, value: (u, v, si, idx) => idx == null || v == null ? '–' : (+v).toFixed(2) + ' %' },
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

/* ---------------- agenter ---------------- */
function renderAgents(page) {
  const head = h('div', { class: 'page-head' }, h('h1', null, 'Agenter'), h('span', { class: 'sub' }, `${S.agents.length} st`), connBadge());
  if (!S.agents.length) { page.replaceChildren(head, h('p', { class: 'muted' }, 'Inga agenter ännu. Öppna en kund och klicka på "Ny agent".')); return; }
  const rows = S.agents.slice().sort((a, b) => (customerName(a) + a.name).localeCompare(customerName(b) + b.name, 'sv')).map(a => {
    const lv = S.live[a.id] || {}, seen = lv.lastSeen || a.lastSeen;
    const st = !seen ? 'pending' : lv.online ? 'up' : 'offline';
    const site = siteOf(a.siteId);
    return h('tr', null,
      h('td', null, h('span', { class: 'pill st-' + st }, h('span', { class: 'dot' }), st === 'up' ? 'Online' : st === 'offline' ? 'Offline' : 'Ej ansluten')),
      h('td', null, h('b', null, a.name), h('div', { class: 'muted' }, a.kind === 'desktop' ? 'Skrivbordsapp' : a.kind === 'agent' ? 'Agent' : '–')),
      h('td', null, h('a', { href: '#/c/' + (site ? site.customerId : '') }, customerName(a)), h('div', { class: 'muted' }, site ? site.name : '')),
      h('td', { class: 'mono' }, a.hostname || '–', h('div', { class: 'muted' }, a.os || '')),
      h('td', { class: 'mono' }, a.ip || '–'), h('td', { class: 'mono' }, a.version || '–'),
      h('td', null, seen ? dateTime(seen) : 'aldrig'),
      h('td', null, String(S.targets.filter(t => t.agentId === a.id).length)),
      h('td', { class: 'actions' }, isAdmin() ? h('button', { class: 'icon-btn', onclick: e => agentMenu(a, e.currentTarget) }, '⋯') : null));
  });
  page.replaceChildren(head, h('table', { class: 'tbl' },
    h('thead', null, h('tr', null, ...['Status', 'Namn', 'Kund / site', 'Värd', 'IP', 'Version', 'Senast sedd', 'Mål', ''].map(x => h('th', null, x)))),
    h('tbody', null, ...rows)));
}
function customerName(a) { const s = siteOf(a.siteId); const c = s && byId(S.customers, s.customerId); return c ? c.name : ''; }

/* ---------------- larm ---------------- */
async function renderAlerts(page) {
  const head = h('div', { class: 'page-head' }, h('h1', null, 'Larm'),
    h('div', { class: 'tools' }, isAdmin() ? h('button', { class: 'btn primary', onclick: () => ruleDialog(null) }, '+ Ny larmregel') : null));
  const groups = [['', 'Gäller alla kunder'], ...S.customers.map(c => [c.id, c.name])];
  const ruleTables = groups.map(([cid, label]) => {
    const rs = S.rules.filter(r => (r.customerId || '') === cid);
    if (!rs.length && (cid !== '' || !isAdmin())) return null;
    return h('div', { class: 'section' }, h('div', { class: 'section-head' }, h('h2', null, label)),
      rs.length ? h('table', { class: 'tbl' }, h('thead', null, h('tr', null, ...['Namn', 'Villkor', 'Åtgärder', 'Mål', ''].map(x => h('th', null, x)))),
        h('tbody', null, ...rs.map(r => h('tr', null, h('td', null, h('b', null, r.name)), h('td', null, describe(r)),
          h('td', null, actionsText(r)), h('td', null, r.condition === 'offline' ? 'alla agenter' : String(S.targets.filter(t => (t.alertIds || []).includes(r.id)).length)),
          h('td', { class: 'actions' }, isAdmin() ? h('button', { class: 'btn sm', onclick: () => ruleDialog(r) }, 'Redigera') : null)))))
        : h('p', { class: 'muted' }, 'Inga regler.'));
  }).filter(Boolean);
  const evBox = h('div', null, h('p', { class: 'muted' }, 'Hämtar…'));
  page.replaceChildren(head, ...ruleTables, h('div', { class: 'section' }, h('div', { class: 'section-head' }, h('h2', null, 'Senaste händelser')), evBox));
  try {
    const evs = await GET('/api/events?limit=300');
    const kt = { fire: 'Utlöst', repeat: 'Pågår', clear: 'Upphört', test: 'Test' };
    evBox.replaceChildren(evs.length ? h('table', { class: 'tbl' },
      h('thead', null, h('tr', null, ...['Tid', 'Händelse', 'Mål', 'Larm', 'Detalj'].map(x => h('th', null, x)))),
      h('tbody', null, ...evs.map(e => h('tr', null, h('td', { class: 'mono' }, dateTime(e.time)), h('td', null, h('span', { class: 'tag ' + e.kind }, kt[e.kind] || e.kind)),
        h('td', null, e.targetName), h('td', null, e.alertName), h('td', null, e.detail)))))
      : h('p', { class: 'muted' }, 'Inga larmhändelser ännu.'));
  } catch (e) { evBox.replaceChildren(h('p', { class: 'form-error' }, e.message)); }
}
function describe(a) {
  if (a.condition === 'latency') return `Latens > ${a.threshold} ms, ${a.count} av ${a.samples}`;
  if (a.condition === 'loss') return `Förlust > ${a.threshold} % (senaste ${a.samples})`;
  if (a.condition === 'offline') return 'Agent offline';
  return `Nere: ${a.count} av ${a.samples} förlorade`;
}
function actionsText(r) {
  const a = [];
  if (r.email) a.push('e-post'); if (r.webhook) a.push('webhook'); if (r.popup) a.push('popup'); if (r.sound) a.push('ljud');
  return a.join(', ') || '–';
}

/* ---------------- användare ---------------- */
async function renderUsers(page) {
  const head = h('div', { class: 'page-head' }, h('h1', null, 'Användare'), h('div', { class: 'tools' }, h('button', { class: 'btn primary', onclick: () => userDialog(null) }, '+ Ny användare')));
  const box = h('div');
  page.replaceChildren(head, box, h('p', { class: 'ro-note', style: 'margin-top:12px' }, 'Kundanvändare ser bara sin egen kund och kan inte ändra något.'));
  const us = await GET('/api/users');
  box.replaceChildren(h('table', { class: 'tbl' }, h('thead', null, h('tr', null, ...['Namn', 'E-post', 'Roll', 'Kund', ''].map(x => h('th', null, x)))),
    h('tbody', null, ...us.map(u => h('tr', null, h('td', null, u.name || '–'), h('td', null, u.email),
      h('td', null, u.role === 'admin' ? 'Administratör' : 'Kund (läs)'), h('td', null, u.customerId ? ((byId(S.customers, u.customerId) || {}).name || '–') : '–'),
      h('td', { class: 'actions' }, h('button', { class: 'btn sm', onclick: () => userDialog(u) }, 'Redigera')))))));
}

/* ---------------- inställningar ---------------- */
async function renderSettings(page) {
  const st = await GET('/api/settings');
  const pub = h('input', { type: 'text', value: st.publicUrl, placeholder: S.serverUrl });
  const rep = num(st.reportSec, 2, 300), raw = num(st.rawDays, 1, 365), agg = num(st.aggDays, 7, 3650);
  const m = st.monitor;
  const win = num(m.statsWindow, 5, 3600), warnMs = num(m.warnMs, 0, 10000), warnLoss = num(m.warnLossPct, 0, 100, .5), downAfter = num(m.downAfter, 1, 100);
  const sm = st.smtp;
  const sHost = h('input', { type: 'text', value: sm.host, placeholder: 'smtp.exempel.se' }), sPort = num(sm.port || 587, 1, 65535);
  const sSec = select([['starttls', 'STARTTLS (587)'], ['tls', 'TLS/SSL (465)'], ['none', 'Ingen kryptering (25)']], sm.security || 'starttls');
  const sUser = h('input', { type: 'text', value: sm.username }), sPass = h('input', { type: 'password', placeholder: st.hasPassword ? '•••••••• (sparat – lämna tomt för att behålla)' : '', autocomplete: 'new-password' });
  const sFrom = h('input', { type: 'email', value: sm.from, placeholder: 'pingvakt@exempel.se' }), sMin = num(sm.minMinutes ?? 5, 0, 1440);
  sSec.onchange = () => { sPort.value = { starttls: 587, tls: 465, none: 25 }[sSec.value]; };
  const smtpVal = () => ({ host: sHost.value.trim(), port: +sPort.value, security: sSec.value, username: sUser.value.trim(), password: sPass.value, from: sFrom.value.trim(), minMinutes: +sMin.value });
  const testTo = h('input', { type: 'email', placeholder: 'mottagare@exempel.se' }), testRes = h('span');
  const msg = h('span');
  page.replaceChildren(h('div', { class: 'page-head' }, h('h1', null, 'Inställningar')),
    h('div', { class: 'modal', style: 'width:auto;max-width:860px;max-height:none' }, h('div', { class: 'modal-body', style: 'padding-top:16px' }, h('div', { class: 'form' },
      h('div', { class: 'section-title' }, 'Server'),
      field('Publik adress', pub, 'Används i installationskommandon och mejl. Tomt = adressen du surfar på', true),
      field('Agenter rapporterar var (sekunder)', rep), h('span'),
      field('Spara rådata (dagar)', raw, 'Varje enskild ping'), field('Spara minutvärden (dagar)', agg, 'Snitt/min/max/förlust per minut'),
      h('div', { class: 'section-title' }, 'Mätning (skickas till agenterna)'),
      field('Statistikfönster (mätpunkter)', win), field('Nere efter (förlorade i rad)', downAfter),
      field('Varningsgräns latens (ms)', warnMs), field('Varningsgräns förlust (%)', warnLoss),
      h('div', { class: 'section-title' }, 'E-post för larm'),
      field('SMTP-server', sHost), field('Säkerhet', sSec), field('Port', sPort), field('Avsändare', sFrom),
      field('Användarnamn', sUser), field('Lösenord', sPass), field('Max ett mejl per larm var (minuter)', sMin),
      h('div', { class: 'field full' }, h('span', null, 'Testa e-post'), h('div', { class: 'row' }, testTo, h('button', { class: 'btn', onclick: async () => {
        testRes.className = ''; testRes.replaceChildren(h('span', { class: 'spin' }), ' Skickar…');
        try { await POST('/api/settings/test-email', { to: testTo.value, smtp: smtpVal(), newPassword: sPass.value !== '' }); testRes.className = 'form-ok'; testRes.textContent = 'Testmejlet skickades.'; }
        catch (e) { testRes.className = 'form-error'; testRes.textContent = e.message; }
      } }, 'Skicka testmejl')), testRes))),
    h('div', { class: 'modal-foot' }, h('div', { class: 'left' }, msg, h('span', { class: 'muted' }, `PingVakt-server ${S.version}`)),
      h('button', { class: 'btn primary', onclick: async () => {
        msg.className = ''; msg.textContent = '';
        try {
          await PUT('/api/settings', { publicUrl: pub.value.trim(), reportSec: +rep.value, rawDays: +raw.value, aggDays: +agg.value,
            monitor: { statsWindow: +win.value, warnMs: +warnMs.value, warnLossPct: +warnLoss.value, downAfter: +downAfter.value, historySize: m.historySize },
            smtp: smtpVal(), newPassword: sPass.value !== '' });
          msg.className = 'form-ok'; msg.textContent = 'Sparat.'; await loadState();
        } catch (e) { msg.className = 'form-error'; msg.textContent = e.message; }
      } }, 'Spara'))));
}

/* ---------------- dialoger ---------------- */
function modal({ title, body, foot, wide, onClose }) {
  const close = () => { bd.remove(); onClose && onClose(); };
  const bd = h('div', { class: 'backdrop' }, h('div', { class: 'modal' + (wide ? ' wide' : ''), role: 'dialog', 'aria-label': title },
    h('div', { class: 'modal-head' }, h('h3', null, title), h('button', { class: 'icon-btn', title: 'Stäng', onclick: close }, '✕')),
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
      foot: [h('button', { class: 'btn', onclick: () => fin(false) }, 'Avbryt'), h('button', { class: 'btn primary', onclick: () => fin(true) }, okLabel || 'OK')],
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
    foot: [h('div', { class: 'left' }, err), h('button', { class: 'btn', onclick: () => close() }, 'Avbryt'),
      h('button', { class: 'btn primary', onclick: async () => { err.textContent = ''; try { await onSave(); close(); } catch (e) { err.textContent = e.message; } } }, saveLabel || 'Spara')] });
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

function customerDialog(c) {
  const name = h('input', { type: 'text', value: c ? c.name : '', placeholder: 'Kundens namn' });
  const notes = h('input', { type: 'text', value: c ? c.notes : '', placeholder: 'Valfri anteckning, t.ex. kontaktperson' });
  formDialog({ title: c ? 'Redigera kund' : 'Ny kund', fields: [field('Namn', name, null, true), field('Anteckning', notes, null, true),
    c ? null : h('small', { class: 'field full' }, 'En första site ("Huvudkontor") skapas automatiskt.')],
    onSave: async () => {
      const r = c ? await PUT('/api/customers/' + c.id, { name: name.value, notes: notes.value }) : await POST('/api/customers', { name: name.value, notes: notes.value });
      await afterChange(); if (!c) location.hash = '#/c/' + r.id;
    } });
}
async function deleteCustomer(c) {
  if (!await confirmDialog('Ta bort kund', `Ta bort "${c.name}" med alla siter, agenter, mål, mätdata, larmregler och kundanvändare? Det går inte att ångra.`, 'Ta bort')) return;
  await DEL('/api/customers/' + c.id); location.hash = '#/'; await afterChange();
}
function siteDialog(customerId, site) {
  const name = h('input', { type: 'text', value: site ? site.name : '', placeholder: 't.ex. Lager Borås' });
  formDialog({ title: site ? 'Byt namn på site' : 'Ny site', fields: [field('Namn', name, null, true)],
    onSave: async () => { site ? await PUT('/api/sites/' + site.id, { name: name.value }) : await POST('/api/sites', { customerId, name: name.value }); await afterChange(); } });
}
async function deleteSite(site) {
  if (!await confirmDialog('Ta bort site', `Ta bort "${site.name}" med dess agenter, mål och mätdata?`, 'Ta bort')) return;
  await DEL('/api/sites/' + site.id); await afterChange();
}
function agentDialog(customerId, siteId) {
  const sites = S.sites.filter(s => s.customerId === customerId);
  const site = select(sites.map(s => [s.id, s.name]), siteId || (sites[0] || {}).id);
  const name = h('input', { type: 'text', placeholder: 't.ex. Raspberry Pi i serverrummet' });
  formDialog({ title: 'Ny agent', saveLabel: 'Skapa agent', fields: [field('Site', site, null, true), field('Namn', name, null, true),
    h('small', { class: 'field full' }, 'Efter att agenten skapats visas en token och färdiga installationskommandon. Samma token kan användas i skrivbordsappen (Inställningar → Server).')],
    onSave: async () => { const r = await POST('/api/agents', { siteId: site.value, name: name.value }); await afterChange(); installDialog(r.agent, r.token); } });
}
function editAgentDialog(a) {
  const cust = customerOfAgent(a);
  const name = h('input', { type: 'text', value: a.name });
  const site = select(S.sites.filter(s => s.customerId === cust).map(s => [s.id, s.name]), a.siteId);
  formDialog({ title: 'Redigera agent', fields: [field('Namn', name, null, true), field('Site', site, null, true)],
    onSave: async () => { await PUT('/api/agents/' + a.id, { name: name.value, siteId: site.value }); await afterChange(); } });
}
function copyBox(text) {
  const b = h('button', { class: 'btn sm', onclick: async () => { try { await navigator.clipboard.writeText(text); b.textContent = 'Kopierat'; setTimeout(() => { b.textContent = 'Kopiera'; }, 1500); } catch (e) { /* ignorera */ } } }, 'Kopiera');
  return h('div', { class: 'codebox' }, text, b);
}
function installDialog(a, token) {
  const url = S.serverUrl;
  const tok = token || '<TOKEN>';
  const body = h('div', { class: 'form' },
    token ? h('div', { class: 'notice field full' }, 'Token visas bara nu. Kopiera den eller kör kommandot direkt. Den kan bytas ut senare via agentens meny ("Ny token").')
      : h('div', { class: 'notice field full' }, 'Token visas bara när den skapas. Skapa en ny token via agentens meny om du behöver installera om.'),
    token ? field('Token', copyBox(token), null, true) : null,
    field('Linux (även Raspberry Pi) och macOS – i terminalen', copyBox(`curl -fsSL ${url}/install.sh | sudo sh -s -- --token ${tok}`), null, true),
    field('Windows – i PowerShell som administratör', copyBox(`& ([scriptblock]::Create((irm ${url}/install.ps1))) -Token ${tok}`), null, true),
    field('Skrivbordsappen PingVakt', h('div', null, 'Inställningar → Server: ange adressen ', h('code', null, url), ' och token.'), null, true),
    h('small', { class: 'field full' }, 'Agenten ansluter utåt till servern via HTTPS – inga portar behöver öppnas hos kunden. Agentfilerna hämtas från den här servern.'));
  modal({ title: `Installera agenten "${a.name}"`, wide: true, body, foot: [h('button', { class: 'btn primary', onclick: e => e.target.closest('.backdrop')._close() }, 'Klar')] });
}

function addTargetsDialog(customerId) {
  const ags = S.agents.filter(a => customerOfAgent(a) === customerId && a.kind !== 'desktop');
  if (!ags.length) { toast({ kind: 'info', title: 'Ingen agent', text: 'Skapa en agent först – det är agenten som mäter mot målen.' }); return agentDialog(customerId); }
  const agent = select(ags.map(a => [a.id, `${a.name} (${(siteOf(a.siteId) || {}).name || ''})`]), ags[0].id);
  const hosts = h('textarea', { rows: 4, placeholder: '192.168.1.1, 10.0.0.1\nskrivare.lokal\nwebb.kund.se:443', spellcheck: 'false' });
  const kind = select([['icmp', 'ICMP (ping)'], ['tcp', 'TCP (port)']], 'icmp');
  const port = num(443, 1, 65535);
  const group = h('input', { type: 'text', placeholder: 'Valfritt, t.ex. Accesspunkter' });
  const pf = field('Port', port); pf.style.display = 'none';
  kind.onchange = () => { pf.style.display = kind.value === 'tcp' ? '' : 'none'; };
  formDialog({ title: 'Lägg till mål', saveLabel: 'Lägg till', fields: [field('Agent som mäter', agent, null, true),
    field('Värdar', hosts, 'En eller flera, separerade med komma eller radbrytning. Skriv värd:port för TCP.', true),
    field('Mättyp', kind), pf, field('Grupp', group, null, true)],
    onSave: async () => { await POST('/api/targets', { agentId: agent.value, hosts: hosts.value, kind: kind.value, port: +port.value, group: group.value }); await afterChange(); } });
}

function editTargetDialog(id) {
  const t = JSON.parse(JSON.stringify(byId(S.targets, id) || {}));
  if (!t.id) return;
  const cust = customerOfTarget(t);
  const fromApp = t.source === 'agent';
  const host = h('input', { type: 'text', value: t.host, disabled: fromApp });
  const name = h('input', { type: 'text', value: t.name, disabled: fromApp });
  const kind = select([['icmp', 'ICMP (ping)'], ['tcp', 'TCP (port)']], t.kind); kind.disabled = fromApp;
  const port = num(t.port || 443, 1, 65535); port.disabled = fromApp;
  const interval = select(INTERVALS.map(v => [v, v >= 1000 ? `${v / 1000} s` : `${v} ms`]), t.intervalMs); interval.disabled = fromApp;
  const timeout = num(t.timeoutMs, 100, 60000, 100); timeout.disabled = fromApp;
  const size = num(t.size, 1, 65000); size.disabled = fromApp;
  const ipMode = select([['auto', 'Automatiskt'], ['4', 'IPv4'], ['6', 'IPv6']], t.ipMode); ipMode.disabled = fromApp;
  const group = h('input', { type: 'text', value: t.group || '', disabled: fromApp });
  const warn = h('input', { type: 'number', value: t.warnMs || '', min: 0, placeholder: `Globalt (${S.monitor.warnMs} ms)` });
  const ags = S.agents.filter(a => customerOfAgent(a) === cust && a.kind !== 'desktop');
  const agent = select(ags.map(a => [a.id, a.name]), t.agentId); agent.disabled = fromApp;
  const sel = new Set(t.alertIds || []);
  const rules = S.rules.filter(r => r.condition !== 'offline' && (!r.customerId || r.customerId === cust));
  const pf = field('Port', port); const syncKind = () => { pf.style.display = kind.value === 'tcp' ? '' : 'none'; }; kind.onchange = syncKind; syncKind();
  formDialog({ title: 'Redigera mål', fields: [
    fromApp ? h('div', { class: 'notice field full' }, 'Målet kommer från en skrivbordsapp. Värd och mätinställningar ändras i appen – här väljer du larm och varningsgräns.') : null,
    field('Värd', host), field('Namn', name), field('Mättyp', kind), pf, fromApp ? null : field('Agent', agent),
    field('Intervall', interval), field('Tidsgräns (ms)', timeout), field('Paketstorlek (byte)', size), field('IP-version', ipMode),
    field('Grupp', group), field('Varningsgräns latens (ms)', warn, 'Tomt = globalt värde'),
    h('div', { class: 'section-title' }, 'Larm'),
    h('div', { class: 'field full' }, rules.length ? h('div', { class: 'checks' }, ...rules.map(r => check(r.name, sel.has(r.id), e => e.target.checked ? sel.add(r.id) : sel.delete(r.id))))
      : h('small', null, 'Inga larmregler finns för kunden.'))],
    onSave: async () => {
      Object.assign(t, { host: host.value.trim(), name: name.value.trim(), kind: kind.value, port: +port.value || 0, intervalMs: +interval.value, timeoutMs: +timeout.value,
        size: +size.value, ipMode: ipMode.value, group: group.value.trim(), warnMs: +warn.value || 0, alertIds: [...sel], agentId: agent.value || t.agentId });
      await PUT('/api/targets/' + id, t); S.spark[id] = S.spark[id] || []; await afterChange(); if (S.selected === id) loadChart();
    } });
}

function ruleDialog(rule) {
  const r = rule ? JSON.parse(JSON.stringify(rule)) : { id: '', name: '', condition: 'down', threshold: 0, count: 3, samples: 3, popup: true, sound: false, soundName: 'alarm', email: false, emailTo: '', webhook: false, hookUrl: '', repeatMin: 0, onClear: true, customerId: S.view.name === 'customer' ? S.view.id : '' };
  const name = h('input', { type: 'text', value: r.name, placeholder: 'Namn på regeln' });
  const cust = select([['', 'Alla kunder'], ...S.customers.map(c => [c.id, c.name])], r.customerId || '');
  const cond = select([['down', 'Nere – förlorade paket'], ['latency', 'Hög latens'], ['loss', 'Paketförlust i procent'], ['offline', 'Agent offline']], r.condition);
  const thr = num(r.threshold, 0, 100000, .5), cnt = num(r.count, 1, 3600), smp = num(r.samples, 1, 3600);
  const condRow = h('div', { class: 'field full' });
  const syncCond = () => {
    const c = cond.value;
    if (c === 'down') condRow.replaceChildren(h('span', null, 'Villkor'), h('div', { class: 'row' }, cnt, h('span', null, 'förlorade av de senaste'), smp, h('span', null, 'paketen')));
    if (c === 'latency') condRow.replaceChildren(h('span', null, 'Villkor'), h('div', { class: 'row' }, cnt, h('span', null, 'av de senaste'), smp, h('span', null, 'över'), thr, h('span', null, 'ms')));
    if (c === 'loss') condRow.replaceChildren(h('span', null, 'Villkor'), h('div', { class: 'row' }, h('span', null, 'Förlust över'), thr, h('span', null, '% av de senaste'), smp, h('span', null, 'paketen')));
    if (c === 'offline') condRow.replaceChildren(h('small', null, `Utlöses när en agent inte har rapporterat på ${Math.max(30, S.reportSec * 3)} sekunder. Gäller alla agenter hos kunden.`));
  };
  cond.onchange = () => { if (cond.value === 'latency' && !(+thr.value > 0)) thr.value = 150; if (cond.value === 'loss' && !(+thr.value > 0)) thr.value = 5; syncCond(); };
  syncCond();
  const emailTo = h('input', { type: 'text', value: r.emailTo, placeholder: 'en@exempel.se, två@exempel.se' });
  const hook = h('input', { type: 'text', value: r.hookUrl, placeholder: 'https://… (Teams, Slack, egen tjänst)' });
  const sound = select([['beep', 'Pip'], ['alarm', 'Larm'], ['chime', 'Klocka']], r.soundName || 'alarm');
  const repeat = num(r.repeatMin, 0, 1440);
  const flags = { popup: r.popup, sound: r.sound, email: r.email, webhook: r.webhook, onClear: r.onClear };
  const box = (label, key) => check(label, flags[key], e => { flags[key] = e.target.checked; });
  const collect = () => Object.assign(r, { name: name.value.trim(), customerId: cust.value, condition: cond.value, threshold: +thr.value, count: +cnt.value, samples: +smp.value,
    emailTo: emailTo.value, hookUrl: hook.value.trim(), soundName: sound.value, repeatMin: +repeat.value }, flags);
  const testRes = h('span');
  const err = h('span', { class: 'form-error' });
  const close = modal({ title: rule ? 'Redigera larmregel' : 'Ny larmregel', wide: true,
    body: h('div', { class: 'form' }, field('Namn', name), field('Gäller', cust), field('Typ', cond, null, true), condRow,
      h('div', { class: 'section-title' }, 'Åtgärder'),
      h('div', { class: 'field full' }, h('div', { class: 'checks', style: 'flex-direction:column' },
        h('div', { class: 'row' }, box('Skicka e-post till', 'email'), emailTo),
        h('div', { class: 'row' }, box('Webhook', 'webhook'), hook),
        box('Popup i webbläsaren för den som har PingVakt öppet', 'popup'),
        h('div', { class: 'row' }, box('Ljud i webbläsaren', 'sound'), sound, h('button', { class: 'btn sm', onclick: () => playSound(sound.value) }, '▶')))),
      h('div', { class: 'section-title' }, 'När'),
      h('div', { class: 'field full' }, h('div', { class: 'checks', style: 'flex-direction:column' },
        h('div', { class: 'row' }, h('span', null, 'Upprepa var'), repeat, h('span', null, 'minut medan larmet pågår (0 = nej)')),
        box('Meddela även när larmet upphör', 'onClear'))),
      rule && cond.value !== 'offline' ? h('div', { class: 'field full' }, h('div', { class: 'row' },
        h('span', { class: 'muted', style: 'flex:1' }, 'Koppla regeln till målen:'),
        h('button', { class: 'btn sm', onclick: async () => assign(true) }, 'Koppla till alla mål'),
        h('button', { class: 'btn sm', onclick: async () => assign(false) }, 'Koppla bort från alla'))) : null),
    foot: [h('div', { class: 'left' }, err, testRes,
      h('button', { class: 'btn', onclick: async () => { err.textContent = ''; testRes.textContent = ''; try { await POST('/api/rules/test', collect()); testRes.className = 'form-ok'; testRes.textContent = 'Test skickat.'; } catch (e) { err.textContent = e.message; } } }, 'Testa'),
      rule ? h('button', { class: 'btn danger', onclick: async () => { if (!await confirmDialog('Ta bort regel', `Ta bort "${rule.name}"?`, 'Ta bort')) return; await DEL('/api/rules/' + rule.id); close(); await afterChange(); } }, 'Ta bort') : null),
      h('button', { class: 'btn', onclick: () => close() }, 'Avbryt'),
      h('button', { class: 'btn primary', onclick: async () => { err.textContent = ''; try { await POST('/api/rules', collect()); close(); await afterChange(); } catch (e) { err.textContent = e.message; } } }, 'Spara')] });
  async function assign(on) {
    collect(); await POST('/api/rules', r);
    const ids = S.targets.filter(t => !r.customerId || customerOfTarget(t) === r.customerId).map(t => t.id);
    const res = await POST(`/api/rules/${r.id}/assign`, { targetIds: ids, on });
    testRes.className = 'form-ok'; testRes.textContent = `${res.changed} mål uppdaterade.`; await loadState();
  }
}

function userDialog(u) {
  const email = h('input', { type: 'email', value: u ? u.email : '' }), name = h('input', { type: 'text', value: u ? u.name : '' });
  const role = select([['admin', 'Administratör (allt)'], ['viewer', 'Kund (läsbehörighet)']], u ? u.role : 'viewer');
  const cust = select(S.customers.map(c => [c.id, c.name]), u ? u.customerId : (S.customers[0] || {}).id);
  const pw = h('input', { type: 'password', autocomplete: 'new-password', placeholder: u ? 'Lämna tomt för att behålla' : 'Minst 8 tecken' });
  const cf = field('Kund', cust); const sync = () => { cf.style.display = role.value === 'viewer' ? '' : 'none'; }; role.onchange = sync; sync();
  const fields = [field('E-post', email), field('Namn', name), field('Roll', role), cf, field('Lösenord', pw, null, true)];
  if (u && u.id !== S.me.id) fields.push(h('div', { class: 'field full' }, h('button', { class: 'btn danger', onclick: async e => {
    if (!await confirmDialog('Ta bort användare', `Ta bort ${u.email}?`, 'Ta bort')) return; await DEL('/api/users/' + u.id); e.target.closest('.backdrop')._close(); renderPage();
  } }, 'Ta bort användaren')));
  formDialog({ title: u ? 'Redigera användare' : 'Ny användare', fields, onSave: async () => {
    const body = { email: email.value, name: name.value, role: role.value, customerId: role.value === 'viewer' ? cust.value : '', password: pw.value };
    u ? await PUT('/api/users/' + u.id, body) : await POST('/api/users', body);
    renderPage();
  } });
}

function passwordDialog() {
  const cur = h('input', { type: 'password', autocomplete: 'current-password' }), nw = h('input', { type: 'password', autocomplete: 'new-password' });
  formDialog({ title: 'Byt lösenord', fields: [field('Nuvarande lösenord', cur, null, true), field('Nytt lösenord', nw, 'Minst 8 tecken', true)],
    onSave: async () => { await POST('/api/me/password', { current: cur.value, new: nw.value }); toast({ kind: 'info', title: 'Lösenordet är bytt' }); } });
}

/* ---------------- meny, toast, ljud ---------------- */
let openMenuEl = null;
function closeMenu() { if (openMenuEl) { openMenuEl.remove(); openMenuEl = null; } }
function showMenu(anchor, items) {
  closeMenu();
  const m = h('div', { class: 'menu' }, ...items.map(it => it === '-' ? h('hr') : h('button', { class: it.danger ? 'danger' : '', onclick: () => { closeMenu(); it.action(); } }, it.label)));
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

function toast({ kind = 'info', title, text, time, sticky, target }) {
  const box = $('#toasts');
  const close = h('button', { class: 'icon-btn', title: 'Stäng', style: 'grid-row:1;grid-column:3' }, '✕');
  const el = h('div', { class: 'toast k-' + kind, style: 'grid-template-columns:1fr auto auto' }, h('b', null, title), h('span', { class: 't-time' }, clock(time || Date.now())), close,
    text ? h('div', { class: 't-text' }, text) : null);
  close.onclick = e => { e.stopPropagation(); el.remove(); };
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
