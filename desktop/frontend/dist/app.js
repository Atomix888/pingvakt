/* PingVakt – gränssnitt */
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

let api;
const S = {
  targets: [], stats: {}, alerts: [], settings: {}, defaults: {}, smtp: {}, hasPassword: false,
  alertLog: '', ui: { theme: 'auto', columns: 0, density: 'normal' }, active: {},
  spark: {}, filter: '', sort: 'manual', selected: null, range: 300, chartData: null, version: '', platform: '',
};
const tiles = new Map();
const SPARK_MAX = 600;

const STATUS_TEXT = { up: 'Uppe', warn: 'Varning', down: 'Nere', paused: 'Pausad', pending: 'Väntar' };
const STATUS_ORDER = { down: 0, warn: 1, pending: 2, up: 3, paused: 4 };
const RANGES = [[60, '1 min'], [300, '5 min'], [900, '15 min'], [3600, '1 h'], [21600, '6 h']];
const INTERVALS = [250, 500, 1000, 2000, 5000, 10000, 30000, 60000];

/* ---------------- start ---------------- */
window.addEventListener('DOMContentLoaded', async () => {
  api = await window.connectAPI();
  if (api.demo) $('#demoBadge').classList.remove('hidden');
  await reload();
  // fyll minigraferna med historik
  const since = Date.now() - SPARK_MAX * 1000;
  await Promise.all(S.targets.map(async t => {
    const hst = await api.call('GetHistory', t.id, since);
    S.spark[t.id] = zip(hst).slice(-SPARK_MAX);
  }));
  renderGrid();
  api.on('tick', onTick);
  api.on('alert', onAlert);
  api.on('error', msg => toast({ kind: 'error', title: 'Fel', text: msg }));
  bindUI();
  setInterval(() => { for (const t of S.targets) updateTileText(t); }, 15000);
});

async function reload() {
  const st = await api.call('GetState');
  Object.assign(S, {
    targets: st.targets || [], stats: st.stats || {}, alerts: st.alerts || [], settings: st.settings || {},
    defaults: st.defaults || {}, smtp: st.smtp || {}, hasPassword: st.hasPassword, alertLog: st.alertLog,
    ui: st.ui || S.ui, active: st.active || {}, version: st.version, platform: st.platform, configPath: st.configPath,
  });
  applyUI();
  $('#footInfo').textContent = S.version === 'demo' ? '' : 'v' + S.version;
}

function zip(hst) {
  const out = [];
  const t = hst.t || [], r = hst.r || [];
  for (let i = 0; i < t.length; i++) out.push({ t: t[i], r: r[i] });
  return out;
}

function applyUI() {
  document.documentElement.dataset.theme = S.ui.theme || 'auto';
  document.body.classList.toggle('compact', S.ui.density === 'compact');
  $('#columns').value = String(S.ui.columns || 0);
  const cols = +S.ui.columns || 0;
  const grid = $('#grid');
  grid.style.gridTemplateColumns = cols ? `repeat(${cols}, minmax(0, 1fr))` : '';
}

/* ---------------- grid ---------------- */
function visibleTargets() {
  const f = S.filter.trim().toLowerCase();
  let list = S.targets.filter(t => !f || (t.name + ' ' + t.host + ' ' + (t.group || '') + ' ' + ((S.stats[t.id] || {}).addr || '')).toLowerCase().includes(f));
  const st = id => S.stats[id] || {};
  switch (S.sort) {
    case 'name': list.sort((a, b) => (a.name || a.host).localeCompare(b.name || b.host, 'sv')); break;
    case 'status': list.sort((a, b) => (STATUS_ORDER[st(a.id).status] ?? 9) - (STATUS_ORDER[st(b.id).status] ?? 9)); break;
    case 'latency': list.sort((a, b) => (st(b.id).avg || 0) - (st(a.id).avg || 0)); break;
    case 'loss': list.sort((a, b) => (st(b.id).loss || 0) - (st(a.id).loss || 0)); break;
  }
  return list;
}

function renderGrid() {
  const grid = $('#grid');
  const list = visibleTargets();
  const ids = new Set(S.targets.map(t => t.id));
  for (const [id, el] of tiles) if (!ids.has(id)) { el.remove(); tiles.delete(id); }
  for (const t of list) {
    let el = tiles.get(t.id);
    if (!el) { el = buildTile(t); tiles.set(t.id, el); }
    grid.append(el); // flyttar till rätt ordning
    fillTileStatic(el, t);
    updateTile(t);
  }
  for (const [id, el] of tiles) if (!list.find(t => t.id === id)) el.remove();
  $('#empty').classList.toggle('hidden', S.targets.length > 0);
  grid.classList.toggle('hidden', S.targets.length === 0);
  renderQuick();
  updateSummary();
}

function buildTile(t) {
  const el = h('div', { class: 'tile st-pending', 'data-id': t.id, draggable: 'true' },
    h('div', { class: 'tile-head' },
      h('span', { class: 'dot' }),
      h('div', { class: 'names' }, h('div', { class: 'name' }), h('div', { class: 'host' })),
      h('button', { class: 'icon-btn menu-btn', title: 'Meny' }, '⋯')),
    h('div', { class: 'tile-main' }, h('span', { class: 'lat' }, '–'), h('span', { class: 'unit' }, 'ms'), h('span', { class: 'state-text' })),
    h('canvas', { class: 'spark' }),
    h('div', { class: 'tile-stats' }));
  el.addEventListener('click', e => {
    if (e.target.closest('.menu-btn')) { e.stopPropagation(); tileMenu(t.id, e.target.closest('.menu-btn')); return; }
    openDrawer(t.id);
  });
  el.addEventListener('dblclick', e => { if (!e.target.closest('.menu-btn')) editTarget(t.id); });
  el.addEventListener('contextmenu', e => { e.preventDefault(); tileMenu(t.id, { getBoundingClientRect: () => ({ left: e.clientX, right: e.clientX, bottom: e.clientY, top: e.clientY }) }); });
  // dra och släpp för egen ordning
  el.addEventListener('dragstart', e => {
    if (S.sort !== 'manual' || S.filter) { e.preventDefault(); return; }
    e.dataTransfer.setData('text/plain', t.id); e.dataTransfer.effectAllowed = 'move'; el.classList.add('dragging');
  });
  el.addEventListener('dragend', () => { el.classList.remove('dragging'); clearDrop(); });
  el.addEventListener('dragover', e => {
    if (S.sort !== 'manual' || S.filter) return;
    e.preventDefault(); clearDrop();
    const r = el.getBoundingClientRect();
    el.classList.add(e.clientX < r.left + r.width / 2 ? 'drop-before' : 'drop-after');
  });
  el.addEventListener('drop', async e => {
    e.preventDefault();
    const from = e.dataTransfer.getData('text/plain');
    const before = el.classList.contains('drop-before');
    clearDrop();
    if (!from || from === t.id) return;
    const order = S.targets.map(x => x.id).filter(x => x !== from);
    let idx = order.indexOf(t.id); if (!before) idx++;
    order.splice(idx, 0, from);
    S.targets.sort((a, b) => order.indexOf(a.id) - order.indexOf(b.id));
    renderGrid();
    await api.call('Reorder', order);
  });
  return el;
}
function clearDrop() { document.querySelectorAll('.drop-before,.drop-after').forEach(x => x.classList.remove('drop-before', 'drop-after')); }

function kindText(t) { return t.kind === 'tcp' ? `TCP ${t.port}` : 'ICMP'; }
function displayName(t) { return t.name || t.host; }

function fillTileStatic(el, t) {
  el.querySelector('.name').textContent = displayName(t);
  el.querySelector('.name').title = displayName(t);
  el.classList.toggle('selected', S.selected === t.id);
}

function updateTile(t) {
  const el = tiles.get(t.id);
  if (!el) return;
  const st = S.stats[t.id] || { status: 'pending', last: -1 };
  const status = st.status || 'pending';
  el.className = el.className.replace(/\bst-\w+/g, '').trim() + ' st-' + status;
  el.classList.toggle('alerting', isAlerting(t.id));
  const addr = st.addr && st.addr !== t.host ? ` → ${st.addr}` : '';
  el.querySelector('.host').textContent = `${t.host}${addr} · ${kindText(t)}`;
  const lat = el.querySelector('.lat');
  const lost = st.window > 0 && st.last < 0;
  lat.textContent = status === 'paused' ? '–' : st.window ? (lost ? 'förlorad' : fmtMs(st.last)) : '…';
  lat.classList.toggle('lost', lost);
  el.querySelector('.unit').style.visibility = (!st.window || lost || status === 'paused') ? 'hidden' : '';
  updateTileText(t);
  const s = el.querySelector('.tile-stats');
  s.replaceChildren(
    h('span', null, h('i', null, 'snitt'), fmtMs(st.avg)),
    h('span', null, h('i', null, 'max'), fmtMs(st.max)),
    h('span', null, h('i', null, 'jitter'), fmtMs(st.jitter)),
    h('span', { class: st.loss > 0 ? 'bad' : '' }, h('i', null, 'förl'), (st.loss || 0).toFixed(st.loss && st.loss < 10 ? 1 : 0) + '%'));
  drawSpark(el.querySelector('canvas'), S.spark[t.id] || [], warnFor(t));
}

function updateTileText(t) {
  const el = tiles.get(t.id);
  if (!el) return;
  const st = S.stats[t.id] || {};
  let txt = '';
  switch (st.status) {
    case 'up': txt = 'Uppe ' + dur(Date.now() - st.since); break;
    case 'down': txt = (st.err || 'Nere') + ' · ' + dur(Date.now() - st.since); break;
    case 'warn': txt = st.consecLost ? (st.err || 'Paket förlorat') : st.loss > (S.settings.warnLossPct || 0) ? `Förlust ${st.loss}%` : 'Hög latens'; break;
    case 'paused': txt = 'Pausad'; break;
    default: txt = 'Väntar på svar…';
  }
  const e = el.querySelector('.state-text');
  e.textContent = txt; e.title = st.err || txt;
}

function isAlerting(id) { return Object.keys(S.active || {}).some(k => k.endsWith('|' + id)); }
function warnFor(t) { return t.warnMs > 0 ? t.warnMs : S.settings.warnMs || 0; }

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
function clock(ms) { return new Date(ms).toLocaleTimeString('sv-SE'); }

let cssCache = null;
function css() {
  if (!cssCache) {
    const cs = getComputedStyle(document.documentElement);
    const v = n => cs.getPropertyValue(n).trim();
    cssCache = { bar: v('--bar'), warn: v('--bar-warn'), lost: v('--bar-lost'), faint: v('--faint'), muted: v('--muted'), border: v('--border'), accent: v('--accent'), text: v('--text') };
  }
  return cssCache;
}
matchMedia('(prefers-color-scheme: light)').addEventListener('change', () => { cssCache = null; renderGrid(); rebuildChart(); });

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
    ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(w, y); ctx.stroke();
    ctx.setLineDash([]); ctx.globalAlpha = 1;
  }
}

function updateSummary() {
  const c = { up: 0, warn: 0, down: 0, paused: 0 };
  for (const t of S.targets) { const s = (S.stats[t.id] || {}).status; if (s in c) c[s]++; }
  const chip = (k, label) => h('span', { class: 'chip st-' + k + (c[k] ? '' : ' zero'), title: label }, h('span', { class: 'dot' }), String(c[k]));
  $('#summary').replaceChildren(chip('up', 'Uppe'), chip('warn', 'Varning'), chip('down', 'Nere'), chip('paused', 'Pausade'));
  const allPaused = S.targets.length && S.targets.every(t => t.paused);
  $('#btnPauseAll').textContent = allPaused ? '▶ Starta alla' : '❚❚ Pausa alla';
  const title = c.down ? `PingVakt – ${c.down} nere` : c.warn ? `PingVakt – ${c.warn} varning` : 'PingVakt';
  if (title !== document.title) { document.title = title; api.setTitle(title); }
}

function renderQuick() {
  const q = $('#quickAdd');
  if (S.targets.length) { q.replaceChildren(); return; }
  q.replaceChildren(...[['1.1.1.1', 'Cloudflare DNS'], ['8.8.8.8', 'Google DNS'], ['9.9.9.9', 'Quad9 DNS']].map(([host, name]) =>
    h('button', { class: 'btn', onclick: async () => { const [t] = await api.call('AddHosts', host, { kind: 'icmp', port: 0 }); t.name = name; await api.call('SaveTarget', t); await afterChange(); } }, `${name} (${host})`)));
}

/* ---------------- händelser från motorn ---------------- */
function onTick(tk) {
  S.stats = tk.stats || S.stats;
  S.active = tk.active || {};
  for (const [id, arr] of Object.entries(tk.samples || {})) {
    const sp = S.spark[id] || (S.spark[id] = []);
    sp.push(...arr);
    if (sp.length > SPARK_MAX) sp.splice(0, sp.length - SPARK_MAX);
    if (id === S.selected && S.chartData) appendChart(arr);
  }
  for (const t of S.targets) updateTile(t);
  if (S.sort !== 'manual') renderGrid(); else updateSummary();
  if (S.selected) updateDrawerHead();
}

function onAlert(ev) {
  if (ev.popup || ev.kind === 'test') {
    const title = { fire: 'Larm', repeat: 'Larm pågår', clear: 'Larm upphört', test: 'Testlarm' }[ev.kind] || 'Larm';
    toast({ kind: ev.kind, title: `${title}: ${ev.targetName}`, text: `${ev.alertName} – ${ev.detail}`, time: ev.time, sticky: ev.kind === 'fire' || ev.kind === 'repeat', target: ev.targetId });
  }
  if (ev.sound) playSound(ev.soundName);
}

/* ---------------- toast ---------------- */
function toast({ kind = 'info', title, text, time, sticky, target }) {
  const box = $('#toasts');
  const el = h('div', { class: 'toast k-' + kind },
    h('b', null, title), h('span', { class: 't-time' }, clock(time || Date.now())),
    text ? h('div', { class: 't-text' }, text) : null);
  const close = h('button', { class: 'icon-btn', title: 'Stäng', style: 'grid-row:1;grid-column:3' }, '✕');
  el.style.gridTemplateColumns = '1fr auto auto';
  el.append(close);
  close.onclick = e => { e.stopPropagation(); el.remove(); };
  if (target) { el.style.cursor = 'pointer'; el.onclick = () => { openDrawer(target); el.remove(); }; }
  box.prepend(el);
  while (box.children.length > 6) box.lastChild.remove();
  if (!sticky) setTimeout(() => el.remove(), 8000);
}

/* ---------------- ljud ---------------- */
let audioCtx;
function playSound(name) {
  try {
    audioCtx = audioCtx || new (window.AudioContext || window.webkitAudioContext)();
    const ctx = audioCtx, t0 = ctx.currentTime;
    const tone = (f, start, len, type = 'sine', vol = .18) => {
      const o = ctx.createOscillator(), g = ctx.createGain();
      o.type = type; o.frequency.value = f;
      g.gain.setValueAtTime(0, t0 + start);
      g.gain.linearRampToValueAtTime(vol, t0 + start + .01);
      g.gain.exponentialRampToValueAtTime(.0001, t0 + start + len);
      o.connect(g).connect(ctx.destination); o.start(t0 + start); o.stop(t0 + start + len + .05);
    };
    if (name === 'alarm') { for (let i = 0; i < 3; i++) { tone(880, i * .36, .16, 'square', .09); tone(660, i * .36 + .18, .16, 'square', .09); } }
    else if (name === 'chime') { tone(659, 0, .5); tone(784, .12, .5); tone(1047, .24, .7); }
    else { tone(1000, 0, .18); }
  } catch (e) { /* ljud inte tillgängligt */ }
}

/* ---------------- meny ---------------- */
let openMenuEl = null;
function closeMenu() { if (openMenuEl) { openMenuEl.remove(); openMenuEl = null; } }
function showMenu(anchor, items) {
  closeMenu();
  const m = h('div', { class: 'menu' }, ...items.map(it => it === '-' ? h('hr') :
    h('button', { class: it.danger ? 'danger' : '', onclick: () => { closeMenu(); it.action(); } }, it.label)));
  document.body.append(m);
  const r = anchor.getBoundingClientRect();
  const mw = m.offsetWidth, mh = m.offsetHeight;
  m.style.left = Math.min(r.right - mw, window.innerWidth - mw - 8) + 'px';
  m.style.left = Math.max(8, parseFloat(m.style.left)) + 'px';
  m.style.top = (r.bottom + mh + 8 > window.innerHeight ? r.top - mh - 4 : r.bottom + 4) + 'px';
  openMenuEl = m;
}
document.addEventListener('mousedown', e => { if (openMenuEl && !openMenuEl.contains(e.target)) closeMenu(); });

function tileMenu(id, anchor) {
  const t = S.targets.find(x => x.id === id);
  if (!t) return;
  showMenu(anchor, [
    { label: 'Visa graf', action: () => openDrawer(id) },
    { label: 'Redigera…', action: () => editTarget(id) },
    { label: t.paused ? 'Starta' : 'Pausa', action: () => togglePause(id) },
    { label: 'Traceroute…', action: () => traceDialog(t.host) },
    { label: 'Exportera CSV…', action: () => exportCSV([id]) },
    { label: 'Duplicera', action: async () => { const c = Object.assign({}, t, { id: '', name: (t.name || t.host) + ' (kopia)' }); await api.call('SaveTarget', c); await afterChange(); } },
    '-',
    { label: 'Ta bort', danger: true, action: () => removeTargets([id]) },
  ]);
}

async function togglePause(id) {
  const t = S.targets.find(x => x.id === id);
  await api.call('SetPaused', [id], !t.paused);
  await afterChange();
}

async function removeTargets(ids) {
  const names = ids.map(id => displayName(S.targets.find(t => t.id === id) || { host: id }));
  if (!await confirmDialog('Ta bort mål', `Ta bort ${names.join(', ')}? Historiken för målet försvinner.`, 'Ta bort')) return;
  await api.call('RemoveTargets', ids);
  if (ids.includes(S.selected)) closeDrawer();
  await afterChange();
}

async function exportCSV(ids) {
  try {
    const p = await api.call('ExportCSV', ids, 0);
    if (p) toast({ kind: 'info', title: 'Exporterat', text: p });
  } catch (e) { toast({ kind: 'error', title: 'Export misslyckades', text: errText(e) }); }
}

async function afterChange() {
  await reload();
  for (const t of S.targets) if (!S.spark[t.id]) S.spark[t.id] = [];
  renderGrid();
  if (S.selected && !S.targets.find(t => t.id === S.selected)) closeDrawer();
  else if (S.selected) updateDrawerHead();
}

function errText(e) { return (e && (e.message || e.toString())) || String(e); }

/* ---------------- UI-bindningar ---------------- */
function bindUI() {
  const kind = $('#addKind'), port = $('#addPort');
  kind.onchange = () => port.classList.toggle('hidden', kind.value !== 'tcp');
  $('#addForm').addEventListener('submit', async e => {
    e.preventDefault();
    const v = $('#addHost').value.trim();
    if (!v) { $('#addHost').focus(); return; }
    try {
      await api.call('AddHosts', v, { kind: kind.value, port: +port.value || 0 });
      $('#addHost').value = '';
      await afterChange();
    } catch (err) { toast({ kind: 'error', title: 'Kunde inte lägga till', text: errText(err) }); }
  });
  $('#filter').addEventListener('input', e => { S.filter = e.target.value; renderGrid(); });
  $('#sortBy').addEventListener('change', e => { S.sort = e.target.value; renderGrid(); });
  $('#columns').addEventListener('change', async e => {
    S.ui.columns = +e.target.value; applyUI(); renderGrid();
    await saveSettings({});
  });
  $('#btnPauseAll').onclick = async () => {
    const allPaused = S.targets.every(t => t.paused);
    await api.call('PauseAll', !allPaused); await afterChange();
  };
  $('#btnTrace').onclick = () => traceDialog('');
  $('#btnAlerts').onclick = () => alertsDialog();
  $('#btnSettings').onclick = () => settingsDialog();
  $('#dClose').onclick = closeDrawer;
  $('#dEdit').onclick = () => editTarget(S.selected);
  $('#dPause').onclick = () => togglePause(S.selected);
  $('#dTrace').onclick = () => { const t = S.targets.find(x => x.id === S.selected); if (t) traceDialog(t.host); };
  $('#dExport').onclick = () => exportCSV([S.selected]);
  $('#dRanges').replaceChildren(...RANGES.map(([sec, label]) => h('button', { 'data-r': sec, onclick: () => setRange(sec) }, label)));
  document.addEventListener('keydown', e => {
    if (e.key === 'Escape') {
      if (openMenuEl) return closeMenu();
      const m = document.querySelector('#modalRoot .backdrop:last-child');
      if (m) return m._close && m._close();
      if (S.selected) closeDrawer();
    }
    if (e.key === '/' && !/INPUT|TEXTAREA|SELECT/.test(document.activeElement.tagName)) { e.preventDefault(); $('#filter').focus(); }
  });
  // drawer-storlek
  const grip = $('#drawerGrip'), drawer = $('#drawer');
  grip.addEventListener('mousedown', e => {
    e.preventDefault();
    const startY = e.clientY, startH = drawer.offsetHeight;
    const move = ev => { drawer.style.height = Math.max(240, Math.min(window.innerHeight * .8, startH + startY - ev.clientY)) + 'px'; resizeChart(); };
    const up = () => { window.removeEventListener('mousemove', move); window.removeEventListener('mouseup', up); };
    window.addEventListener('mousemove', move); window.addEventListener('mouseup', up);
  });
  window.addEventListener('resize', () => { for (const t of S.targets) updateTile(t); resizeChart(); });
}

/* ---------------- detaljvy med graf ---------------- */
let chart = null;

async function openDrawer(id) {
  S.selected = id;
  tiles.forEach((el, tid) => el.classList.toggle('selected', tid === id));
  $('#drawer').classList.remove('hidden');
  updateDrawerHead();
  await loadChart();
  const el = tiles.get(id);
  if (el) el.scrollIntoView({ block: 'nearest' });
}

function closeDrawer() {
  S.selected = null; S.chartData = null;
  tiles.forEach(el => el.classList.remove('selected'));
  $('#drawer').classList.add('hidden');
  if (chart) { chart.destroy(); chart = null; }
}

function setRange(sec) { S.range = sec; loadChart(); }

function updateDrawerHead() {
  const t = S.targets.find(x => x.id === S.selected);
  if (!t) return;
  const st = S.stats[t.id] || {};
  $('#dDot').parentElement.className = 'drawer-head st-' + (st.status || 'pending');
  $('#dName').textContent = displayName(t);
  $('#dHost').textContent = `${t.host}${st.addr && st.addr !== t.host ? ' → ' + st.addr : ''} · ${kindText(t)} · var ${t.intervalMs >= 1000 ? t.intervalMs / 1000 + ' s' : t.intervalMs + ' ms'}`;
  $('#dPause').textContent = t.paused ? '▶ Starta' : '❚❚ Pausa';
  document.querySelectorAll('#dRanges button').forEach(b => b.classList.toggle('on', +b.dataset.r === S.range));
  renderDrawerStats();
}

function renderDrawerStats() {
  const d = S.chartData;
  const t = S.targets.find(x => x.id === S.selected);
  if (!d || !t) return;
  const vals = [], n = d[0].length;
  let lost = 0;
  for (let i = 0; i < n; i++) { const v = d[1][i]; if (v == null) lost++; else vals.push(v); }
  const sorted = vals.slice().sort((a, b) => a - b);
  const avg = vals.length ? vals.reduce((a, b) => a + b, 0) / vals.length : NaN;
  let jit = 0; for (let i = 1; i < vals.length; i++) jit += Math.abs(vals[i] - vals[i - 1]);
  jit = vals.length > 1 ? jit / (vals.length - 1) : NaN;
  const pct = p => sorted.length ? sorted[Math.min(sorted.length - 1, Math.floor(p * sorted.length))] : NaN;
  const st = S.stats[t.id] || {};
  const row = (k, v) => [h('dt', null, k), h('dd', null, v)];
  $('#dStats').replaceChildren(
    h('dl', null,
      row('Status', STATUS_TEXT[st.status] || '–'),
      row('Sedan', st.since ? dur(Date.now() - st.since) : '–'),
      row('Senaste', st.last >= 0 ? fmtMs(st.last) + ' ms' : 'förlorad'),
      row('Paket', String(n)),
      row('Förlorade', `${lost} (${n ? +(lost * 100 / n).toFixed(1) : 0}%)`),
      row('Snitt', fmtMs(avg) + ' ms'),
      row('Min', fmtMs(sorted[0]) + ' ms'),
      row('Max', fmtMs(sorted[sorted.length - 1]) + ' ms'),
      row('Median', fmtMs(pct(.5)) + ' ms'),
      row('95:e pct', fmtMs(pct(.95)) + ' ms'),
      row('Jitter', fmtMs(jit) + ' ms'),
      row('Totalt', `${st.sent || 0} skickade`),
    ),
    ...(st.err ? [h('div', { class: 'err' }, st.err)] : []));
}

async function loadChart() {
  const id = S.selected;
  if (!id) return;
  const since = Date.now() - S.range * 1000;
  const hst = await api.call('GetHistory', id, since);
  if (id !== S.selected) return;
  const t = hst.t || [], r = hst.r || [];
  S.chartData = [t.map(x => x / 1000), r.map(v => v < 0 ? null : v), r.map(v => v < 0 ? 1 : 0)];
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

function rebuildChart() {
  const box = $('#dChart');
  if (chart) { chart.destroy(); chart = null; }
  if (!S.selected || !S.chartData) return;
  const c = css();
  const t = S.targets.find(x => x.id === S.selected);
  const warn = t ? warnFor(t) : 0;
  const lossPlugin = {
    hooks: {
      drawClear: u => {
        const ctx = u.ctx, d = u.data, top = u.bbox.top, hgt = u.bbox.height;
        ctx.save();
        ctx.fillStyle = c.lost; ctx.globalAlpha = .28;
        const ivl = t ? t.intervalMs / 1000 : 1;
        const w = Math.max(1.5 * devicePixelRatio, ivl * u.bbox.width / S.range * .9);
        for (let i = 0; i < d[0].length; i++) if (d[2][i]) {
          const x = u.valToPos(d[0][i], 'x', true);
          ctx.fillRect(x - w / 2, top, Math.max(w, 2), hgt);
        }
        ctx.restore();
      },
      draw: u => {
        if (!warn) return;
        const y = u.valToPos(warn, 'y', true);
        if (y < u.bbox.top || y > u.bbox.top + u.bbox.height) return;
        const ctx = u.ctx; ctx.save();
        ctx.strokeStyle = c.warn; ctx.setLineDash([5, 4]); ctx.lineWidth = 1 * devicePixelRatio; ctx.globalAlpha = .8;
        ctx.beginPath(); ctx.moveTo(u.bbox.left, y); ctx.lineTo(u.bbox.left + u.bbox.width, y); ctx.stroke();
        ctx.restore();
      },
    },
  };
  const axis = { stroke: c.muted, grid: { stroke: c.border, width: 1 }, ticks: { stroke: c.border, width: 1 }, font: '11px ' + getComputedStyle(document.body).fontFamily };
  const opts = {
    width: box.clientWidth - 14, height: Math.max(120, box.clientHeight - 40),
    cursor: { drag: { x: true, y: false } },
    scales: { x: { time: true, range: () => { const n = Date.now() / 1000; return [n - S.range, n]; } }, y: { range: (u, min, max) => [0, Math.max((max || 0) * 1.15, 0.1)] }, loss: { range: [0, 1] } },
    axes: [Object.assign({}, axis, { values: (u, splits) => splits.map(v => timeLabel(v, S.range)) }), Object.assign({}, axis, { label: 'ms', labelSize: 18, size: 50 })],
    series: [
      { label: 'Tid', value: (u, v) => v == null ? '–' : new Date(v * 1000).toLocaleTimeString('sv-SE') },
      { label: 'Latens', stroke: c.accent, width: 1.5, fill: hexA(c.accent, .12), points: { show: false }, spanGaps: false, value: (u, v, si, idx) => idx == null ? '–' : v == null ? 'förlorad' : fmtMs(v) + ' ms' },
      { label: 'Förlorad', scale: 'loss', stroke: c.lost, width: 0, points: { show: false }, paths: () => null, value: (u, v, si, idx) => idx == null ? '–' : v ? 'ja' : 'nej' },
    ],
    legend: { live: true },
    plugins: [lossPlugin],
  };
  S.zoomed = false;
  opts.hooks = { setSelect: [u => { if (u.select.width > 4) S.zoomed = true; }] };
  chart = new uPlot(opts, S.chartData, box);
  chart.over.addEventListener('dblclick', () => { S.zoomed = false; chart.setData(S.chartData, true); });
  chart.over.title = 'Dra för att zooma, dubbelklicka för att återställa';
}

function timeLabel(sec, range) {
  const d = new Date(sec * 1000);
  const p = n => String(n).padStart(2, '0');
  return range <= 900 ? `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}` : `${p(d.getHours())}:${p(d.getMinutes())}`;
}

function resizeChart() {
  if (!chart) return;
  const box = $('#dChart');
  chart.setSize({ width: box.clientWidth - 14, height: Math.max(120, box.clientHeight - 40) });
}

function hexA(color, a) {
  const m = /^#?([\da-f]{2})([\da-f]{2})([\da-f]{2})$/i.exec(color.trim());
  if (!m) return color;
  return `rgba(${parseInt(m[1], 16)},${parseInt(m[2], 16)},${parseInt(m[3], 16)},${a})`;
}

/* ---------------- dialoger ---------------- */
function modal({ title, body, foot, wide, onClose }) {
  const root = $('#modalRoot');
  const close = () => { bd.remove(); onClose && onClose(); };
  const bd = h('div', { class: 'backdrop' },
    h('div', { class: 'modal' + (wide ? ' wide' : ''), role: 'dialog', 'aria-label': title },
      h('div', { class: 'modal-head' }, h('h3', null, title), h('button', { class: 'icon-btn', title: 'Stäng', onclick: close }, '✕')),
      h('div', { class: 'modal-body' }, body),
      foot ? h('div', { class: 'modal-foot' }, foot) : null));
  bd._close = close;
  bd.addEventListener('mousedown', e => { if (e.target === bd) close(); });
  root.append(bd);
  const f = bd.querySelector('input:not([type=checkbox]),select,textarea');
  if (f) setTimeout(() => f.focus(), 30);
  return close;
}

function confirmDialog(title, text, okLabel) {
  return new Promise(resolve => {
    let done = false;
    const finish = v => { if (!done) { done = true; resolve(v); close(); } };
    const close = modal({
      title, body: h('p', null, text),
      foot: [h('button', { class: 'btn', onclick: () => finish(false) }, 'Avbryt'), h('button', { class: 'btn primary', onclick: () => finish(true) }, okLabel || 'OK')],
      onClose: () => { if (!done) { done = true; resolve(false); } },
    });
  });
}

const field = (label, input, hint, full) => h('label', { class: 'field' + (full ? ' full' : '') }, h('span', null, label), input, hint ? h('small', null, hint) : null);
const check = (label, checked, onchange) => h('label', { class: 'check' }, h('input', { type: 'checkbox', checked, onchange }), label);
const select = (opts, value) => h('select', null, ...opts.map(([v, l]) => h('option', { value: v, selected: String(v) === String(value) }, l)));
const num = (value, min, max, step) => h('input', { type: 'number', value, min, max, step: step || 1 });

/* --- redigera mål --- */
function editTarget(id) {
  const orig = S.targets.find(x => x.id === id);
  if (!orig) return;
  const t = JSON.parse(JSON.stringify(orig));
  const host = h('input', { type: 'text', value: t.host, spellcheck: 'false' });
  const name = h('input', { type: 'text', value: t.name, placeholder: 'Valfritt visningsnamn' });
  const kind = select([['icmp', 'ICMP (ping)'], ['tcp', 'TCP (port)']], t.kind);
  const port = num(t.port || 443, 1, 65535);
  const interval = select(INTERVALS.map(v => [v, v >= 1000 ? `${v / 1000} s` : `${v} ms`]), t.intervalMs);
  const timeout = num(t.timeoutMs, 100, 60000, 100);
  const size = num(t.size, 1, 65000);
  const ipMode = select([['auto', 'Automatiskt'], ['4', 'IPv4'], ['6', 'IPv6']], t.ipMode);
  const group = h('input', { type: 'text', value: t.group || '', placeholder: 't.ex. Kund A / Lager' });
  const warn = h('input', { type: 'number', value: t.warnMs || '', min: 0, placeholder: `Globalt (${S.settings.warnMs} ms)` });
  const sel = new Set(t.alertIds || []);
  const alertChecks = h('div', { class: 'checks' }, ...S.alerts.map(a => check(a.name, sel.has(a.id), e => e.target.checked ? sel.add(a.id) : sel.delete(a.id))));
  const portField = field('Port', port);
  const syncKind = () => { portField.style.display = kind.value === 'tcp' ? '' : 'none'; size.disabled = kind.value === 'tcp'; };
  kind.onchange = syncKind; syncKind();
  const err = h('span', { class: 'form-error' });
  const close = modal({
    title: 'Redigera mål',
    body: h('div', { class: 'form' },
      field('Värd (namn eller IP)', host), field('Namn', name),
      field('Mättyp', kind), portField,
      field('Intervall', interval), field('Tidsgräns (ms)', timeout),
      field('Paketstorlek (byte)', size), field('IP-version', ipMode),
      field('Grupp', group, 'Används för filtrering'), field('Varningsgräns latens (ms)', warn, 'Tomt = globalt värde'),
      h('div', { class: 'section-title' }, 'Larm för det här målet'),
      h('div', { class: 'field full' }, S.alerts.length ? alertChecks : h('small', null, 'Inga larm skapade ännu.'))),
    foot: [h('div', { class: 'left' }, err), h('button', { class: 'btn', onclick: () => close() }, 'Avbryt'),
      h('button', { class: 'btn primary', onclick: async () => {
        Object.assign(t, { host: host.value.trim(), name: name.value.trim(), kind: kind.value, port: +port.value || 0, intervalMs: +interval.value,
          timeoutMs: +timeout.value, size: +size.value, ipMode: ipMode.value, group: group.value.trim(), warnMs: +warn.value || 0, alertIds: [...sel] });
        try {
          await api.call('SaveTarget', t);
          if (orig.host !== t.host || orig.kind !== t.kind || orig.port !== t.port) S.spark[t.id] = [];
          close(); await afterChange();
          if (S.selected === t.id) loadChart();
        } catch (e) { err.textContent = errText(e); }
      } }, 'Spara')],
  });
}

/* --- inställningar --- */
async function saveSettings(patch) {
  const inp = Object.assign({ settings: S.settings, defaults: S.defaults, smtp: S.smtp, newPassword: false, alertLog: S.alertLog, ui: S.ui }, patch);
  await api.call('SaveSettings', inp);
}

function settingsDialog() {
  const st = Object.assign({}, S.settings), df = JSON.parse(JSON.stringify(S.defaults)), sm = Object.assign({}, S.smtp), ui = Object.assign({}, S.ui);
  let tab = 'general';
  const tabs = h('div', { class: 'tabs' });
  const body = h('div');
  const err = h('span', { class: 'form-error' });
  const pane = {};

  // allmänt
  const statsWindow = num(st.statsWindow, 5, 3600), warnMs = num(st.warnMs, 0, 10000), warnLoss = num(st.warnLossPct, 0, 100, .5);
  const downAfter = num(st.downAfter, 1, 100), histSize = num(st.historySize, 300, 604800, 300);
  const histHint = h('small');
  const syncHist = () => { histHint.textContent = `≈ ${dur(+histSize.value * (df.intervalMs || 1000))} per mål vid ${(df.intervalMs || 1000) / 1000} s intervall`; };
  histSize.oninput = syncHist; syncHist();
  pane.general = h('div', { class: 'form' },
    field('Statistikfönster (antal mätpunkter)', statsWindow, 'Snitt, min, max, jitter och förlust i rutorna räknas på detta'),
    field('Nere efter (förlorade i rad)', downAfter),
    field('Varningsgräns latens (ms)', warnMs, '0 = av. Kan ändras per mål'),
    field('Varningsgräns förlust (%)', warnLoss),
    h('label', { class: 'field full' }, h('span', null, 'Historik i minnet (mätpunkter per mål)'), histSize, histHint));

  // förval
  const dInterval = select(INTERVALS.map(v => [v, v >= 1000 ? `${v / 1000} s` : `${v} ms`]), df.intervalMs);
  const dTimeout = num(df.timeoutMs, 100, 60000, 100), dSize = num(df.size, 1, 65000);
  const dSel = new Set(df.alertIds || []);
  pane.defaults = h('div', { class: 'form' },
    field('Intervall', dInterval), field('Tidsgräns (ms)', dTimeout), field('Paketstorlek (byte)', dSize), h('span'),
    h('div', { class: 'section-title' }, 'Larm som kopplas till nya mål'),
    h('div', { class: 'field full' }, h('div', { class: 'checks' }, ...S.alerts.map(a => check(a.name, dSel.has(a.id), e => e.target.checked ? dSel.add(a.id) : dSel.delete(a.id))))));

  // e-post
  const sHost = h('input', { type: 'text', value: sm.host, placeholder: 'smtp.exempel.se' });
  const sPort = num(sm.port || 587, 1, 65535);
  const sSec = select([['starttls', 'STARTTLS (587)'], ['tls', 'TLS/SSL (465)'], ['none', 'Ingen kryptering (25)']], sm.security || 'starttls');
  const sUser = h('input', { type: 'text', value: sm.username, autocomplete: 'off' });
  const sPass = h('input', { type: 'password', value: '', placeholder: S.hasPassword ? '•••••••• (sparat – lämna tomt för att behålla)' : '', autocomplete: 'new-password' });
  const sFrom = h('input', { type: 'email', value: sm.from, placeholder: 'pingvakt@exempel.se' });
  const sMin = num(sm.minMinutes ?? 5, 0, 1440);
  const testTo = h('input', { type: 'email', placeholder: 'mottagare@exempel.se' });
  const testRes = h('span');
  sSec.onchange = () => { sPort.value = { starttls: 587, tls: 465, none: 25 }[sSec.value]; };
  const smtpVal = () => ({ host: sHost.value.trim(), port: +sPort.value, security: sSec.value, username: sUser.value.trim(), password: sPass.value, from: sFrom.value.trim(), minMinutes: +sMin.value });
  pane.email = h('div', { class: 'form' },
    field('SMTP-server', sHost), field('Säkerhet', sSec), field('Port', sPort), field('Avsändare', sFrom),
    field('Användarnamn', sUser), field('Lösenord', sPass),
    field('Max ett mejl per larm var (minuter)', sMin, 'Händelser däremellan samlas i nästa mejl', true),
    h('div', { class: 'section-title' }, 'Testa'),
    h('div', { class: 'field full' }, h('div', { class: 'row' }, testTo, h('button', { class: 'btn', onclick: async () => {
      testRes.className = ''; testRes.replaceChildren(h('span', { class: 'spin' }), ' Skickar…');
      try { await api.call('TestEmail', testTo.value, smtpVal(), sPass.value !== ''); testRes.className = 'form-ok'; testRes.textContent = 'Testmejlet skickades.'; }
      catch (e) { testRes.className = 'form-error'; testRes.textContent = errText(e); }
    } }, 'Skicka testmejl')), testRes),
    h('small', { class: 'field full' }, 'Lösenordet sparas i konfigurationsfilen på den här datorn.'));

  // utseende och filer
  const theme = select([['auto', 'Följ systemet'], ['dark', 'Mörkt'], ['light', 'Ljust']], ui.theme);
  const density = select([['normal', 'Normal'], ['compact', 'Kompakt']], ui.density);
  const logPath = h('input', { type: 'text', value: S.alertLog, spellcheck: 'false' });
  pane.ui = h('div', { class: 'form' },
    field('Tema', theme), field('Rutor', density),
    field('Larmlogg (fil)', h('div', { class: 'row' }, logPath, h('button', { class: 'btn', onclick: () => api.call('OpenPath', logPath.value) }, 'Öppna')), null, true),
    field('Konfigurationsfil', h('div', { class: 'row' }, h('code', { style: 'flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap' }, S.configPath || '–')), null, true),
    h('small', { class: 'field full' }, `PingVakt ${S.version} · ${S.platform}`));

  const TABS = [['general', 'Mätning'], ['defaults', 'Förval för nya mål'], ['email', 'E-post'], ['ui', 'Utseende och filer']];
  const show = k => { tab = k; tabs.querySelectorAll('button').forEach(b => b.classList.toggle('on', b.dataset.k === k)); body.replaceChildren(pane[k]); };
  tabs.append(...TABS.map(([k, l]) => h('button', { 'data-k': k, onclick: () => show(k) }, l)));
  show(tab);

  const close = modal({
    title: 'Inställningar', wide: false, body: h('div', null, tabs, body),
    foot: [h('div', { class: 'left' }, err), h('button', { class: 'btn', onclick: () => close() }, 'Avbryt'),
      h('button', { class: 'btn primary', onclick: async () => {
        const inp = {
          settings: { statsWindow: +statsWindow.value, warnMs: +warnMs.value, warnLossPct: +warnLoss.value, downAfter: +downAfter.value, historySize: +histSize.value },
          defaults: { intervalMs: +dInterval.value, timeoutMs: +dTimeout.value, size: +dSize.value, alertIds: [...dSel] },
          smtp: smtpVal(), newPassword: sPass.value !== '', alertLog: logPath.value.trim(), ui: { theme: theme.value, density: density.value, columns: S.ui.columns },
        };
        try { await api.call('SaveSettings', inp); close(); cssCache = null; await afterChange(); rebuildChart(); }
        catch (e) { err.textContent = errText(e); }
      } }, 'Spara')],
  });
}

/* --- larm --- */
function alertsDialog() {
  let list = JSON.parse(JSON.stringify(S.alerts));
  let cur = list[0] ? list[0].id : null;
  let tab = 'rules';
  const tabs = h('div', { class: 'tabs' });
  const body = h('div');
  const err = h('span', { class: 'form-error' });
  const newAlert = () => ({ id: 'n' + Date.now().toString(36), name: 'Nytt larm', condition: 'down', threshold: 0, count: 3, samples: 3, popup: true, sound: true, soundName: 'beep', notify: false, email: false, emailTo: '', log: true, repeatMin: 0, onClear: true });

  const renderRules = () => {
    const a = list.find(x => x.id === cur);
    const lst = h('div', { class: 'list' }, ...list.map(x => h('button', { class: x.id === cur ? 'on' : '', onclick: () => { cur = x.id; renderRules(); } },
      x.name || '(namnlöst)', h('small', null, describe(x) + ' · ' + usage(x.id)))));
    const left = h('div', { style: 'display:flex;flex-direction:column;gap:8px' }, lst,
      h('button', { class: 'btn', onclick: () => { const n = newAlert(); list.push(n); cur = n.id; renderRules(); } }, '+ Nytt larm'));
    body.replaceChildren(h('div', { class: 'split' }, left, a ? alertForm(a) : h('div', { class: 'muted', style: 'padding:20px' }, 'Skapa ett larm till vänster.')));
  };
  const usage = id => { const n = S.targets.filter(t => (t.alertIds || []).includes(id)).length; return n === 1 ? '1 mål' : n + ' mål'; };

  const alertForm = a => {
    const name = h('input', { type: 'text', value: a.name, oninput: e => { a.name = e.target.value; } });
    const cond = select([['down', 'Nere – förlorade paket'], ['latency', 'Hög latens'], ['loss', 'Paketförlust i procent']], a.condition);
    const thr = num(a.threshold, 0, 100000, .5);
    const cnt = num(a.count, 1, 3600), smp = num(a.samples, 1, 3600);
    const condRow = h('div', { class: 'field full' });
    const syncCond = () => {
      a.condition = cond.value;
      const c = a.condition;
      if (c === 'down') condRow.replaceChildren(h('span', null, 'Villkor'), h('div', { class: 'row' }, cnt, h('span', null, 'förlorade av de senaste'), smp, h('span', null, 'paketen')));
      if (c === 'latency') condRow.replaceChildren(h('span', null, 'Villkor'), h('div', { class: 'row' }, cnt, h('span', null, 'av de senaste'), smp, h('span', null, 'över'), thr, h('span', null, 'ms')), h('small', null, 'Förlorade paket räknas också som för höga.'));
      if (c === 'loss') condRow.replaceChildren(h('span', null, 'Villkor'), h('div', { class: 'row' }, h('span', null, 'Förlust över'), thr, h('span', null, '% av de senaste'), smp, h('span', null, 'paketen')));
    };
    cond.onchange = () => {
      if (cond.value === 'latency' && !(a.threshold > 0)) thr.value = 150;
      if (cond.value === 'loss' && !(a.threshold > 0)) thr.value = 5;
      syncCond(); upd();
    };
    const upd = () => { a.threshold = +thr.value; a.count = +cnt.value; a.samples = +smp.value; };
    [thr, cnt, smp].forEach(x => x.oninput = upd);
    syncCond();
    const sound = select([['beep', 'Pip'], ['alarm', 'Larm'], ['chime', 'Klocka']], a.soundName);
    sound.onchange = () => { a.soundName = sound.value; playSound(sound.value); };
    const emailTo = h('input', { type: 'text', value: a.emailTo, placeholder: 'en@exempel.se, två@exempel.se', oninput: e => { a.emailTo = e.target.value; } });
    const repeat = num(a.repeatMin, 0, 1440); repeat.oninput = () => { a.repeatMin = +repeat.value; };
    const box = (label, key) => check(label, a[key], e => { a[key] = e.target.checked; });
    const assigned = S.targets.filter(t => (t.alertIds || []).includes(a.id)).length;
    return h('div', { class: 'form', style: 'align-content:start' },
      field('Namn', name, null, true),
      field('Typ', cond, null, true), condRow,
      h('div', { class: 'section-title' }, 'Åtgärder'),
      h('div', { class: 'field full' }, h('div', { class: 'checks', style: 'flex-direction:column' },
        box('Visa popup i programmet', 'popup'),
        h('div', { class: 'row' }, box('Spela ljud', 'sound'), sound, h('button', { class: 'btn sm', onclick: () => playSound(sound.value) }, '▶')),
        box('Systemnotis', 'notify'),
        h('div', { class: 'row' }, box('Skicka e-post till', 'email'), emailTo),
        box('Skriv till larmloggen', 'log'))),
      h('div', { class: 'section-title' }, 'När'),
      h('div', { class: 'field full' }, h('div', { class: 'checks', style: 'flex-direction:column' },
        h('div', { class: 'row' }, h('span', null, 'Upprepa var'), repeat, h('span', null, 'minut medan larmet pågår (0 = nej)')),
        box('Meddela även när larmet upphör', 'onClear'))),
      h('div', { class: 'section-title' }, 'Mål'),
      h('div', { class: 'field full' }, h('div', { class: 'row' },
        h('span', { class: 'muted', style: 'flex:1' }, `Kopplat till ${assigned} av ${S.targets.length} mål.`),
        h('button', { class: 'btn sm', onclick: async () => { await persist(); await api.call('AssignAlert', a.id, S.targets.map(t => t.id), true); await afterChange(); renderRules(); } }, 'Koppla till alla'),
        h('button', { class: 'btn sm', onclick: async () => { await persist(); await api.call('AssignAlert', a.id, S.targets.map(t => t.id), false); await afterChange(); renderRules(); } }, 'Koppla bort från alla'))),
      h('div', { class: 'field full' }, h('div', { class: 'row', style: 'justify-content:space-between' },
        h('button', { class: 'btn', onclick: async () => {
          err.textContent = '';
          try { await api.call('TestAlert', a); } catch (e) { err.textContent = errText(e); }
        } }, 'Testa larmet'),
        h('button', { class: 'btn danger', onclick: () => { list = list.filter(x => x.id !== a.id); cur = list[0] ? list[0].id : null; renderRules(); } }, 'Ta bort larmet'))));
  };

  const renderEvents = async () => {
    const evs = await api.call('AlertHistory');
    if (!evs || !evs.length) { body.replaceChildren(h('p', { class: 'muted' }, 'Inga larmhändelser sedan programmet startade. Äldre händelser finns i larmloggen.')); return; }
    const kt = { fire: 'Utlöst', repeat: 'Pågår', clear: 'Upphört', test: 'Test' };
    body.replaceChildren(h('div', { style: 'max-height:440px;overflow:auto' }, h('table', { class: 'events' },
      h('thead', null, h('tr', null, h('th', null, 'Tid'), h('th', null, 'Händelse'), h('th', null, 'Mål'), h('th', null, 'Larm'), h('th', null, 'Detalj'))),
      h('tbody', null, ...evs.map(e => h('tr', null,
        h('td', { class: 'mono' }, new Date(e.time).toLocaleString('sv-SE')), h('td', null, h('span', { class: 'tag ' + e.kind }, kt[e.kind] || e.kind)),
        h('td', null, e.targetName), h('td', null, e.alertName), h('td', null, e.detail)))))));
  };

  const persist = async () => {
    list = await api.call('SaveAlerts', list);
    S.alerts = list;
  };
  const show = k => { tab = k; tabs.querySelectorAll('button').forEach(b => b.classList.toggle('on', b.dataset.k === k)); k === 'rules' ? renderRules() : renderEvents(); };
  tabs.append(h('button', { 'data-k': 'rules', onclick: () => show('rules') }, 'Larmregler'), h('button', { 'data-k': 'events', onclick: () => show('events') }, 'Händelser'));
  show(tab);

  const close = modal({
    title: 'Larm', wide: true, body: h('div', null, tabs, body),
    foot: [h('div', { class: 'left' }, err), h('button', { class: 'btn', onclick: () => close() }, 'Stäng utan att spara'),
      h('button', { class: 'btn primary', onclick: async () => {
        try { await persist(); close(); await afterChange(); } catch (e) { err.textContent = errText(e); }
      } }, 'Spara')],
  });
}

function describe(a) {
  if (a.condition === 'latency') return `> ${a.threshold} ms, ${a.count} av ${a.samples}`;
  if (a.condition === 'loss') return `förlust > ${a.threshold} %`;
  return `${a.count} av ${a.samples} förlorade`;
}

/* --- traceroute --- */
let traceHandlersBound = false;
let traceState = null;
function traceDialog(host) {
  const input = h('input', { type: 'text', value: host || '', placeholder: 'Värdnamn eller IP', spellcheck: 'false' });
  const btn = h('button', { class: 'btn primary' }, 'Starta');
  const status = h('span', { class: 'muted' });
  const tbody = h('tbody');
  const addAll = h('button', { class: 'btn', disabled: true }, 'Lägg till alla hopp som mål');
  const hops = [];
  const st = traceState = { running: false };

  const addHop = hop => {
    hops.push(hop);
    const tr = h('tr', { class: hop.addr ? '' : 'timeout' },
      h('td', null, String(hop.n)), h('td', null, hop.addr || '*'), h('td', { class: 'name', title: hop.name || '' }, hop.name || ''),
      h('td', null, hop.rtt >= 0 ? fmtMs(hop.rtt) + ' ms' : '–'),
      h('td', null, hop.addr ? h('button', { class: 'btn sm', title: 'Lägg till som mål', onclick: async ev => {
        ev.target.disabled = true; const [t] = await api.call('AddHosts', hop.addr, { kind: 'icmp', port: 0 });
        t.name = `Hopp ${hop.n}${hop.name ? ' ' + hop.name : ''}`; await api.call('SaveTarget', t); await afterChange();
      } }, '+') : null));
    tbody.append(tr);
    addAll.disabled = !hops.some(x => x.addr);
  };
  if (!traceHandlersBound) {
    traceHandlersBound = true;
    api.on('trace:hop', hop => traceState && traceState.onHop && traceState.onHop(hop));
    api.on('trace:done', msg => traceState && traceState.onDone && traceState.onDone(msg));
  }
  st.onHop = addHop;
  st.onDone = msg => {
    st.running = false; btn.textContent = 'Starta';
    status.className = msg ? 'form-error' : 'muted';
    status.textContent = msg || `Klar – ${hops.length} hopp`;
  };
  const start = async () => {
    if (st.running) { await api.call('StopTrace'); st.onDone('Avbruten'); return; }
    const v = input.value.trim();
    if (!v) return input.focus();
    hops.length = 0; tbody.replaceChildren(); addAll.disabled = true;
    st.running = true; btn.textContent = 'Stoppa';
    status.className = 'muted'; status.replaceChildren(h('span', { class: 'spin' }), ' Kör traceroute…');
    try { await api.call('StartTrace', v); } catch (e) { st.onDone(errText(e)); }
  };
  btn.onclick = start;
  input.addEventListener('keydown', e => { if (e.key === 'Enter') start(); });
  addAll.onclick = async () => {
    const dest = input.value.trim();
    for (const hop of hops.filter(x => x.addr)) {
      const [t] = await api.call('AddHosts', hop.addr, { kind: 'icmp', port: 0 });
      t.name = `${dest} hopp ${hop.n}${hop.name ? ' (' + hop.name + ')' : ''}`;
      await api.call('SaveTarget', t);
    }
    addAll.disabled = true;
    await afterChange();
    toast({ kind: 'info', title: 'Hopp tillagda', text: `${hops.filter(x => x.addr).length} mål lades till` });
  };
  modal({
    title: 'Traceroute', wide: true,
    body: h('div', null, h('div', { class: 'row' }, input, btn), h('div', { style: 'margin-top:8px' }, status),
      h('div', { style: 'max-height:420px;overflow:auto' }, h('table', { class: 'trace-table' },
        h('thead', null, h('tr', null, h('th', null, '#'), h('th', null, 'Adress'), h('th', null, 'Namn'), h('th', null, 'Latens'), h('th', null, ''))), tbody))),
    foot: [addAll],
    onClose: () => { if (st.running) api.call('StopTrace'); if (traceState === st) traceState = null; },
  });
  if (host) start();
}
