/* PingVakt – Wi-Fi-survey (samma fil i server/web och desktop/frontend/dist).
 * Använder h och T från värdappen. */
'use strict';

const SV_BANDS = {
  '2.4': { lo: 2401, hi: 2495, ticks: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13], labels: null, f: c => c === 14 ? 2484 : 2407 + c * 5 },
  '5': { lo: 5150, hi: 5895, ticks: [36, 40, 44, 48, 52, 56, 60, 64, 100, 104, 108, 112, 116, 120, 124, 128, 132, 136, 140, 144, 149, 153, 157, 161, 165, 169, 173, 177],
    labels: [36, 44, 52, 60, 100, 108, 116, 124, 132, 140, 149, 157, 165, 173], f: c => 5000 + c * 5 },
  '6': { lo: 5925, hi: 7125, ticks: Array.from({ length: 59 }, (_, i) => 1 + i * 4), labels: [1, 33, 65, 97, 129, 161, 193, 225], f: c => 5950 + c * 5 },
};
const svBandOf = f => f >= 5925 ? '6' : f >= 4900 ? '5' : '2.4';
const SV_PAL = ['#5b9cf5', '#8b7cf6', '#f2b33d', '#e46aa8', '#3ccf7a', '#ef8a50', '#4fc1c9', '#c1d045', '#b07ae0', '#f07a7a'];
function svColor(ssid) { let x = 0; for (const ch of ssid || '?') x = (x * 31 + ch.charCodeAt(0)) >>> 0; return SV_PAL[x % SV_PAL.length]; }
function svAlpha(hex, a) { const n = parseInt(hex.replace('#', ''), 16); return `rgba(${n >> 16 & 255},${n >> 8 & 255},${n & 255},${a})`; }
function svVar(name, def) { const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim(); return v || def; }
const svLevel = v => v >= -67 ? 'ok' : v >= -75 ? 'high' : 'bad';
const svTime = ms => new Date(ms).toLocaleString('sv-SE');

// svSpan ger frekvensområdet ett nät upptar, med kanalsammanslagning (40/80/160 MHz) i rätt block.
function svSpan(b) {
  const w = b.width || 20, f = b.freq;
  if (w <= 20) return [f - 10, f + 10];
  if (f < 4900) return b.ch <= 7 ? [f - 10, f + 30] : [f - 30, f + 10];
  const base = f >= 5925 ? 5945 : f < 5490 ? 5170 : f < 5735 ? 5490 : 5735;
  const start = base + Math.floor((f - base) / w) * w;
  return [start, start + w];
}

function svInsights(list, cur) {
  const out = [];
  if (cur && cur.ch) {
    const mine = list.filter(b => b.ch === cur.ch && svBandOf(b.freq) === svBandOf(cur.freq || 0) && !(b.bssid && cur.bssid && b.bssid === cur.bssid.toLowerCase()));
    const strong = mine.filter(b => b.rssi >= -82);
    out.push([strong.length > 3 ? 'bad' : strong.length > 1 ? 'high' : 'ok', T('{0} other networks on channel {1} above −82 dBm (co-channel interference)', strong.length, cur.ch)]);
  }
  const odd = list.filter(b => svBandOf(b.freq) === '2.4' && ![1, 6, 11].includes(b.ch) && b.rssi >= -85);
  if (odd.length) out.push(['high', T('{0} networks in 2.4 GHz use channels other than 1, 6 and 11 and overlap their neighbours', odd.length)]);
  const wide = list.filter(b => svBandOf(b.freq) === '2.4' && b.width >= 40);
  if (wide.length) out.push(['high', T('{0} networks use 40 MHz in 2.4 GHz', wide.length)]);
  const open = list.filter(b => b.sec === 'Open' || b.sec === 'WEP');
  if (open.length) out.push(['high', T('{0} open or WEP networks', open.length)]);
  return out;
}

// surveyPanel bygger vyn för en avsökning. state behåller valt band och filter mellan uppdateringar.
// opt: { scan: {t, bss[], tool}, cur: {ssid,bssid,ch,freq}, state: {} }
function surveyPanel(opt) {
  const body = h('div', { class: 'survey-panel' });
  const st = opt.state || {};
  const sc = opt.scan, cur = opt.cur;
  if (!st.band) st.band = cur && cur.freq ? svBandOf(cur.freq) : (sc.bss.length ? svBandOf(sc.bss[0].freq) : '2.4');
  if (st.filter == null) st.filter = '';
  const draw = () => {
    const list = sc.bss;
    const counts = { '2.4': 0, '5': 0, '6': 0 };
    for (const b of list) counts[svBandOf(b.freq)]++;
    const tabs = h('div', { class: 'ranges' }, ...['2.4', '5', '6'].map(bd => h('button', { class: st.band === bd ? 'on' : '', onclick: () => { st.band = bd; draw(); } }, `${bd} GHz (${counts[bd]})`)));
    const cv = h('canvas', { class: 'chan-canvas' });
    const filter = h('input', { type: 'search', placeholder: T('Filter on SSID, BSSID or manufacturer'), value: st.filter });
    const tbl = h('div', { class: 'tbl-wrap' });
    const fact = (k, v) => h('div', null, h('small', null, k), h('b', null, v || '–'));
    const ssids = new Set(list.map(b => b.ssid || '(hidden)'));
    const ins = svInsights(list, cur);
    body.replaceChildren(
      h('div', { class: 'facts' }, fact(T('Scanned'), svTime(sc.t)), fact(T('Access points (BSSID)'), String(list.length)), fact(T('Networks (SSID)'), String(ssids.size)),
        cur && cur.ssid ? fact(T('Connected to'), `${cur.ssid} · ${T('channel')} ${cur.ch}${cur.rssi ? ' · ' + cur.rssi + ' dBm' : ''}`) : '', sc.tool ? fact(T('Source'), sc.tool) : ''),
      ins.length ? h('ul', { class: 'insights' }, ...ins.map(([lv, t]) => h('li', { class: 'lvl-' + lv }, t))) : '',
      h('div', { class: 'section-head', style: 'margin-top:10px' }, h('h2', null, T('Channels')), h('div', { class: 'tools' }, tabs)), cv,
      h('div', { class: 'row', style: 'margin:10px 0 6px' }, filter), tbl);
    const drawTable = () => {
      const q = filter.value.trim().toLowerCase(); st.filter = filter.value;
      const rows = list.filter(b => !q || (b.ssid || '').toLowerCase().includes(q) || (b.bssid || '').includes(q) || (b.vendor || '').toLowerCase().includes(q));
      const shown = st.showAll ? rows : rows.slice(0, 60);
      const curB = cur && cur.bssid ? cur.bssid.toLowerCase() : '';
      tbl.replaceChildren(h('table', { class: 'tbl compact survey-tbl' },
        h('thead', null, h('tr', null, ...['SSID', 'BSSID', T('Manufacturer'), T('Channel'), T('Width'), 'Signal', T('Security'), 'Standard'].map(x => h('th', null, x)))),
        h('tbody', null, ...shown.map(b => h('tr', { class: b.conn || (curB && b.bssid === curB) ? 'sel' : '' },
          h('td', null, h('span', { class: 'sw', style: `background:${svColor(b.ssid)}` }), b.ssid ? h('b', null, b.ssid) : h('i', { class: 'muted' }, T('hidden'))),
          h('td', { class: 'mono' }, b.bssid || '–'), h('td', { class: 'muted' }, b.vendor || ''),
          h('td', { class: 'mono' }, `${b.ch} · ${svBandOf(b.freq)} GHz`), h('td', { class: 'mono' }, b.width + ' MHz'),
          h('td', { class: 'mono lvl-' + svLevel(b.rssi) }, b.rssi + ' dBm'),
          h('td', { class: b.sec === 'Open' || b.sec === 'WEP' ? 'lvl-high' : '' }, b.sec || '–'), h('td', { class: 'muted' }, b.std || ''))))),
        rows.length > shown.length ? h('button', { class: 'btn ghost sm', onclick: () => { st.showAll = true; drawTable(); } }, T('Show all {0}', rows.length)) : '');
    };
    filter.oninput = drawTable;
    drawTable();
    requestAnimationFrame(() => svDrawChannels(cv, list.filter(b => svBandOf(b.freq) === st.band), st.band, cur));
  };
  draw();
  return body;
}

function svDrawChannels(cv, list, band, cur) {
  const B = SV_BANDS[band];
  const accent = svVar('--accent', '#35c2b0'), muted = svVar('--muted', '#8b95a3'), border = svVar('--border', '#262c35');
  const W = cv.parentElement ? cv.parentElement.clientWidth : 600, H = 230, dpr = window.devicePixelRatio || 1;
  cv.width = W * dpr; cv.height = H * dpr; cv.style.width = W + 'px'; cv.style.height = H + 'px';
  const g = cv.getContext('2d'); g.scale(dpr, dpr);
  const L = 44, R = 8, T0 = 10, Bm = 26, pw = W - L - R, ph = H - T0 - Bm;
  const x = f => L + (f - B.lo) / (B.hi - B.lo) * pw;
  const y = r => T0 + (Math.min(-20, Math.max(-100, r)) + 20) / -80 * ph;
  g.font = '11px ' + getComputedStyle(document.body).fontFamily;
  g.strokeStyle = border; g.fillStyle = muted; g.lineWidth = 1;
  for (const r of [-30, -50, -67, -80, -90]) { g.beginPath(); g.moveTo(L, y(r)); g.lineTo(W - R, y(r)); g.setLineDash(r === -67 ? [4, 3] : []); g.stroke(); g.fillText(r, 8, y(r) + 4); }
  g.setLineDash([]);
  let lastX = -1e9;
  for (const ch of B.ticks) {
    const px = x(B.f(ch));
    g.beginPath(); g.moveTo(px, T0 + ph); g.lineTo(px, T0 + ph + 4); g.stroke();
    if (!B.labels || B.labels.includes(ch)) {
      const t = String(ch), tw = g.measureText(t).width;
      if (px - tw / 2 > lastX + 4) { g.fillText(t, px - tw / 2, H - 8); lastX = px + tw / 2; } // hoppa över etiketter som krockar på smala skärmar
    }
  }
  const curB = cur && cur.bssid ? cur.bssid.toLowerCase() : '';
  const labels = [];
  for (const b of list.slice().sort((a, c) => a.rssi - c.rssi)) {
    const [f0, f1] = svSpan(b), x0 = x(f0), x1 = x(f1), top = y(b.rssi), base = T0 + ph, slope = Math.min(10, (x1 - x0) / 5);
    const mine = b.conn || (curB && b.bssid === curB);
    const col = svColor(b.ssid);
    g.beginPath(); g.moveTo(x0, base); g.lineTo(x0 + slope, top); g.lineTo(x1 - slope, top); g.lineTo(x1, base);
    g.fillStyle = svAlpha(col, mine ? .35 : .12); g.fill();
    g.strokeStyle = mine ? accent : col; g.lineWidth = mine ? 2.5 : 1.2; g.stroke();
    labels.push({ x: (x0 + x1) / 2, y: top, t: b.ssid || T('hidden'), col: mine ? accent : col, r: b.rssi });
  }
  g.lineWidth = 1;
  const placed = [];
  for (const lb of labels.sort((a, c) => c.r - a.r).slice(0, 18)) {
    const w = g.measureText(lb.t).width; let ly = lb.y - 4;
    while (placed.some(p => Math.abs(p.x - lb.x) < (p.w + w) / 2 + 4 && Math.abs(p.y - ly) < 12)) ly -= 12;
    if (ly < T0 + 8) continue;
    placed.push({ x: lb.x, y: ly, w });
    g.fillStyle = lb.col; g.fillText(lb.t, Math.max(L, Math.min(W - R - w, lb.x - w / 2)), ly);
  }
  if (!list.length) { g.fillStyle = muted; const t = T('No networks in this band'); g.fillText(t, L + pw / 2 - g.measureText(t).width / 2, T0 + ph / 2); }
}

// svCSV gör en CSV av en eller flera avsökningar (semikolon för svensk Excel). list: [{scan, loc}]
function svCSV(list, sv) {
  const sep = sv ? ';' : ',';
  const q = v => { const s = String(v == null ? '' : v); return /[";,\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s; };
  const withLoc = list.some(x => x.loc);
  const head = [...(withLoc ? ['Location'] : []), 'Time', 'SSID', 'BSSID', 'Manufacturer', 'Band', 'Channel', 'Width (MHz)', 'Signal (dBm)', 'Security', 'Standard'].map(x => T(x));
  const rows = [];
  for (const { scan, loc } of list) for (const b of scan.bss) rows.push([...(withLoc ? [loc || ''] : []), svTime(scan.t), b.ssid, b.bssid, b.vendor, svBandOf(b.freq) + ' GHz', b.ch, b.width, b.rssi, b.sec, b.std]);
  return [head, ...rows].map(r => r.map(q).join(sep)).join('\r\n');
}
