'use strict';
/* PingVakt – gränssnittet i Android-appen. Visas i en WebView och pratar med agenten på 127.0.0.1. */

function T(s, ...a) {
  const d = window.PV_DICT || {};
  let r = d[s];
  if (r == null) r = String(s).replace(/\|[a-z]{1,12}$/, '');
  return a.length ? r.replace(/\{(\d+)\}/g, (m, i) => a[i] == null ? '' : a[i]) : r;
}
function TE(msg) {
  msg = String(msg == null ? '' : msg);
  const d = window.PV_DICT || {};
  if (d[msg] != null) return d[msg];
  let best = '';
  for (const k in d) if (k.length > best.length && /[ :(]$/.test(k) && msg.startsWith(k)) best = k;
  return best ? d[best] + msg.slice(best.length) : msg;
}
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'class') el.className = v;
    else if (k === 'style') el.style.cssText = v;
    else if (k in el && k !== 'list') el[k] = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const k of kids.flat()) if (k != null && k !== false) el.append(k.nodeType ? k : document.createTextNode(k));
  return el;
}
const $ = s => document.querySelector(s);
const PV = window.PV || null; // brygga till Android-appen (saknas i en vanlig webbläsare)

async function api(method, path, body) {
  const r = await fetch(path, { method, headers: body ? { 'Content-Type': 'application/json' } : {}, body: body ? JSON.stringify(body) : undefined, credentials: 'same-origin' });
  if (r.status === 204) return null;
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(TE(j.error || r.statusText));
  return j;
}

const S = { tab: 'status', st: null, rssi: [], roams: [], lastBssid: '', rec: [], recording: false, loc: '', sv: {}, lastScanT: 0 };

function toast(text, ms) {
  const el = $('#toast');
  el.textContent = text; el.classList.remove('hidden');
  clearTimeout(toast.t); toast.t = setTimeout(() => el.classList.add('hidden'), ms || 3500);
}
const fmtMs = v => v == null || v < 0 || isNaN(v) ? '–' : v < 10 ? v.toFixed(1) : String(Math.round(v));
const clock = ms => new Date(ms).toLocaleTimeString('sv-SE');
const bandOf = f => f >= 5925 ? '6 GHz' : f >= 4900 ? '5 GHz' : f ? '2.4 GHz' : '';
const rssiLv = v => v >= -67 ? 'ok' : v >= -75 ? 'high' : 'bad';
const STATUS = () => ({ up: T('Up'), warn: T('Warning'), down: T('Down'), paused: T('Paused'), pending: T('Pending') });

async function poll() {
  try {
    const st = await api('GET', '/api/status');
    S.st = st;
    const w = st.wifi;
    if (w) {
      S.rssi.push({ t: Date.now(), v: w.rssi });
      if (S.rssi.length > 360) S.rssi.shift();
      if (S.lastBssid && w.bssid && w.bssid !== S.lastBssid) S.roams.unshift({ t: Date.now(), from: S.lastBssid, to: w.bssid, rssi: w.rssi });
      if (w.bssid) S.lastBssid = w.bssid;
    }
    // ny avsökning medan inspelning pågår → spara med platsen
    if (S.recording && st.scan && st.scan.t !== S.lastScanT) S.rec.push({ scan: st.scan, loc: S.loc });
    if (st.scan) S.lastScanT = st.scan.t;
    renderTop(); render();
  } catch (e) { $('#conn').replaceChildren(h('span', { class: 'dot down' }), T('The agent is not running')); }
}

function renderTop() {
  const st = S.st;
  const conn = $('#conn');
  if (!st.enrolled) conn.replaceChildren(h('span', { class: 'dot' }), T('Not connected'));
  else if (st.link && st.link.connected) conn.replaceChildren(h('span', { class: 'dot up' }), st.link.agentName || T('Connected'));
  else conn.replaceChildren(h('span', { class: 'dot down' }), T('No contact with the server'));
  const tabs = st.enrolled ? [['status', '◉', T('Status')], ['wifi', '📶', 'Wi-Fi'], ['survey', '◫', 'Survey']] : [['setup', '⚙', T('Connect')], ['wifi', '📶', 'Wi-Fi'], ['survey', '◫', 'Survey']];
  if (!tabs.some(t => t[0] === S.tab)) S.tab = tabs[0][0];
  const bar = $('#tabbar');
  const sig = tabs.map(t => t[0]).join() + S.tab;
  if (bar.dataset.sig !== sig) {
    bar.dataset.sig = sig;
    bar.replaceChildren(...tabs.map(([id, ic, label]) => h('button', { class: S.tab === id ? 'on' : '', onclick: () => { S.tab = id; renderTop(); render(true); } }, h('span', { class: 'ic' }, ic), label)));
  }
}

let lastRender = '';
function render(force) {
  const main = $('#main');
  // skriv inte över formulär eller filter medan användaren skriver
  if (!force && document.activeElement && main.contains(document.activeElement) && /INPUT|SELECT/.test(document.activeElement.tagName)) return;
  const fn = { setup: renderSetup, status: renderStatus, wifi: renderWiFi, survey: renderSurvey }[S.tab];
  if (S.tab === 'setup' && !force && lastRender === 'setup') return;
  lastRender = S.tab;
  fn(main);
}

function renderSetup(main) {
  const server = h('input', { type: 'url', placeholder: 'https://pingvakt.example.com', autocapitalize: 'off', autocorrect: 'off', value: localStorage.getItem('pv.server') || '' });
  const token = h('input', { type: 'text', placeholder: 'pva_…', autocapitalize: 'off', autocorrect: 'off', spellcheck: false });
  const msg = h('div');
  const btn = h('button', { class: 'btn primary wide', onclick: async () => {
    msg.className = ''; msg.textContent = ''; btn.disabled = true;
    try {
      localStorage.setItem('pv.server', server.value.trim());
      const r = await api('POST', '/api/enroll', { server: server.value, token: token.value });
      toast(T('Connected as "{0}" at {1} / {2}', r.agent, r.customer, r.site), 5000);
      S.tab = 'status'; await poll();
    } catch (e) { msg.className = 'error'; msg.textContent = e.message; } finally { btn.disabled = false; }
  } }, T('Connect'));
  main.replaceChildren(h('h1', null, T('Connect to your PingVakt server')),
    h('div', { class: 'card' },
      h('p', { class: 'muted', style: 'margin-top:0' }, T('In PingVakt, open the customer, click "New agent" and copy the token. The phone then measures like any other agent and reports Wi-Fi, roaming and scans.')),
      h('label', { class: 'field' }, T('Server address'), server), h('label', { class: 'field' }, 'Token', token), btn, msg),
    permCard(),
    h('p', { class: 'muted' }, T('You can use Wi-Fi and Survey without a server.')));
}

function permCard() {
  if (!PV) return '';
  let p = {};
  try { p = JSON.parse(PV.permissions()); } catch (e) { return ''; }
  const items = [];
  if (!p.location) items.push(T('Location access is needed to see Wi-Fi networks, BSSID and channels (Android requires it).'));
  else if (!p.background) items.push(T('Allow location "all the time" so Wi-Fi is measured when the app is in the background.'));
  if (!p.notifications) items.push(T('Allow notifications so the agent can keep running in the background.'));
  if (!p.battery) items.push(T('Turn off battery optimization for PingVakt, otherwise Android may stop the measurements.'));
  if (!items.length) return '';
  return h('div', { class: 'notice' }, h('ul', { style: 'margin:0 0 10px;padding-left:18px' }, ...items.map(t => h('li', null, t))),
    h('div', { class: 'row' }, h('button', { class: 'btn sm', onclick: () => PV.requestPermissions() }, T('Allow access')),
      !p.battery ? h('button', { class: 'btn sm', onclick: () => PV.openBatterySettings() }, T('Battery settings')) : ''));
}

function renderStatus(main) {
  const st = S.st, l = st.link || {};
  const fact = (k, v) => h('div', null, h('small', null, k), h('b', null, v || '–'));
  const ts = st.targets || [];
  main.replaceChildren(
    permCard(),
    h('div', { class: 'card' }, h('div', { class: 'facts' },
      fact(T('Agent'), l.agentName), fact(T('Customer'), l.customer), fact(T('Site'), l.site), fact('Server', (st.server || '').replace(/^https?:\/\//, '')),
      fact(T('Last contact'), l.lastOk ? clock(l.lastOk) : ''), fact(T('Queued'), String(l.buffered || 0))),
      l.lastError ? h('div', { class: 'error' }, TE(l.lastError)) : ''),
    h('h3', null, T('Targets ({0})', ts.length)),
    ts.length ? h('div', { class: 'targets' }, ...ts.map(t => {
      const s = t.stats || {};
      const status = s.status || 'pending';
      return h('div', { class: 'target st-' + status }, h('span', { class: 'dot' }), h('div', { class: 'name' }, t.name || t.host),
        h('div', { class: 'val' }, s.last >= 0 ? fmtMs(s.last) + ' ms' : status === 'down' ? T('lost') : '–', h('small', null, `${T('avg')} ${fmtMs(s.avg)} · ${T('loss')} ${s.loss != null ? (+s.loss).toFixed(s.loss < 10 ? 1 : 0) : '–'} %`)),
        h('div', { class: 'host' }, `${t.host}${t.kind && t.kind !== 'icmp' ? ' · ' + t.kind.toUpperCase() : ''}`));
    })) : h('p', { class: 'muted' }, T('No targets yet – add targets for this agent in PingVakt.')),
    h('div', { style: 'margin-top:22px' }, h('button', { class: 'btn ghost danger', onclick: async () => {
      if (!confirm(T('Disconnect the phone from the server?'))) return;
      await api('POST', '/api/unenroll'); S.tab = 'setup'; await poll();
    } }, T('Disconnect from the server'))),
    h('p', { class: 'muted', style: 'font-size:12px' }, `PingVakt ${st.version}${st.device ? ' · ' + st.device : ''}`));
}

function renderWiFi(main) {
  const w = S.st.wifi;
  if (!w) { main.replaceChildren(permCard(), h('div', { class: 'card' }, h('h2', null, T('Not connected to Wi-Fi')), h('p', { class: 'muted' }, T('Connect the phone to a Wi-Fi network, or allow location access so Android shows the network details.')))); return; }
  const fact = (k, v, cls) => h('div', null, h('small', null, k), h('b', { class: cls || '' }, v || '–'));
  const cv = h('canvas', { class: 'spark' });
  main.replaceChildren(permCard(),
    h('div', { class: 'card' }, h('div', { class: 'row', style: 'justify-content:space-between' },
      h('div', null, h('div', { class: 'muted' }, w.ssid || T('hidden')), h('div', { class: 'big' }, h('b', { class: 'lvl-' + rssiLv(w.rssi) }, String(w.rssi)), h('span', null, 'dBm'))),
      h('div', { style: 'text-align:right' }, h('div', { class: 'muted' }, T('Channel')), h('b', { class: 'mono', style: 'font-size:22px' }, String(w.ch || '–')), h('div', { class: 'muted' }, bandOf(w.freq) + (w.width ? ' · ' + w.width + ' MHz' : '')))),
      cv),
    h('div', { class: 'card' }, h('div', { class: 'facts' }, fact(T('Access point (BSSID)'), w.bssid), fact(T('Speed'), w.tx ? `${Math.round(w.tx)}${w.rx ? ' / ' + Math.round(w.rx) : ''} Mbit/s` : ''),
      fact('Standard', w.std), fact(T('Frequency'), w.freq ? w.freq + ' MHz' : ''))),
    h('h3', null, T('Roams')),
    S.roams.length ? h('ul', { class: 'roams' }, ...S.roams.slice(0, 20).map(r => h('li', null, h('span', { class: 'mono' }, clock(r.t)), ` ${r.from.slice(-8)} → ${r.to.slice(-8)} (${r.rssi} dBm)`)))
      : h('p', { class: 'muted' }, T('No roams since the app was opened.')));
  requestAnimationFrame(() => drawSpark(cv));
}

function drawSpark(cv) {
  const pts = S.rssi.slice(-180);
  const W = cv.clientWidth, H = cv.clientHeight, dpr = window.devicePixelRatio || 1;
  cv.width = W * dpr; cv.height = H * dpr;
  const g = cv.getContext('2d'); g.scale(dpr, dpr);
  const css = getComputedStyle(document.documentElement);
  const y = v => (Math.min(-30, Math.max(-95, v)) + 30) / -65 * (H - 4) + 2;
  g.strokeStyle = css.getPropertyValue('--border'); g.setLineDash([4, 3]); g.beginPath(); g.moveTo(0, y(-67)); g.lineTo(W, y(-67)); g.stroke(); g.setLineDash([]);
  if (pts.length < 2) return;
  g.strokeStyle = css.getPropertyValue('--accent'); g.lineWidth = 2; g.beginPath();
  pts.forEach((p, i) => { const x = i / (pts.length - 1) * W; i ? g.lineTo(x, y(p.v)) : g.moveTo(x, y(p.v)); });
  g.stroke();
}

function renderSurvey(main) {
  const sc = S.st.scan;
  const loc = h('input', { type: 'text', placeholder: T('Location, e.g. Room 204'), value: S.loc, oninput: e => { S.loc = e.target.value; } });
  const rec = h('input', { type: 'checkbox', checked: S.recording, onchange: e => { S.recording = e.target.checked; if (PV) PV.setFastScan(S.recording); if (S.recording) scanNow(); render(true); } });
  const head = h('div', { class: 'card' },
    h('div', { class: 'row' }, h('button', { class: 'btn primary', onclick: scanNow }, T('Scan now')),
      h('label', { class: 'row', style: 'gap:6px' }, rec, T('Repeat (every 30 s)'))),
    h('label', { class: 'field', style: 'margin:12px 0 0' }, T('Location'), loc),
    h('div', { class: 'row', style: 'margin-top:10px' },
      h('span', { class: 'muted' }, T('{0} scans recorded', S.rec.length)),
      S.rec.length ? h('button', { class: 'btn sm', onclick: exportCSV }, T('Export CSV')) : '',
      S.rec.length ? h('button', { class: 'btn sm ghost', onclick: () => { if (confirm(T('Clear the recorded scans?'))) { S.rec = []; render(true); } } }, T('Clear')) : '',
      S.st.enrolled && sc ? h('button', { class: 'btn sm', onclick: async () => { try { await api('POST', '/api/survey/send'); toast(T('The scan was sent to the server.')); } catch (e) { toast(e.message); } } }, T('Send to server')) : ''),
    h('small', { class: 'muted', style: 'display:block;margin-top:8px' }, T('Android allows about 4 scans per 2 minutes. Walk between locations and change the location name; every scan is saved with it.')));
  if (!sc) { main.replaceChildren(permCard(), head, h('p', { class: 'muted' }, T('No scan yet.'))); return; }
  const cur = S.st.wifi;
  main.replaceChildren(permCard(), head, sc.err && !sc.bss.length ? h('div', { class: 'notice' }, TE(sc.err)) : '', surveyPanel({ scan: sc, cur: cur ? { ssid: cur.ssid, bssid: cur.bssid, ch: cur.ch, freq: cur.freq, rssi: cur.rssi } : null, state: S.sv }));
}

function scanNow() {
  if (PV) PV.scanNow(); else api('POST', '/api/survey/request').catch(() => {});
  toast(T('Scanning…'), 2000);
}

function exportCSV() {
  const csv = svCSV(S.rec, (window.PV_LANG || 'en') === 'sv');
  const name = `wifi-survey-${new Date().toISOString().slice(0, 16).replace(/[:T]/g, '-')}.csv`;
  if (PV) PV.share(name, csv);
  else { const a = h('a', { href: URL.createObjectURL(new Blob(['﻿' + csv], { type: 'text/csv' })), download: name }); document.body.append(a); a.click(); a.remove(); }
}

window.addEventListener('DOMContentLoaded', () => {
  $('#menuBtn').addEventListener('click', () => {
    if (!S.st) return;
    const items = [`PingVakt ${S.st.version}`];
    if (PV) items.push(T('Battery settings'));
    if (PV && confirm(items.join('\n') + '\n\n' + T('Open battery settings?'))) PV.openBatterySettings();
  });
  poll();
  setInterval(poll, 2000);
});
