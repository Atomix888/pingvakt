'use strict';
/* PingVakt – SNMP-enheter, hastighetstester och mallar för mål. */

/* ---------------- gemensamt ---------------- */
function fmtBps(v) {
  if (v == null || isNaN(v)) return '–';
  if (v >= 1e9) return (v / 1e9).toFixed(v >= 1e10 ? 0 : 2) + ' Gbit/s';
  if (v >= 1e6) return (v / 1e6).toFixed(v >= 1e8 ? 0 : 1) + ' Mbit/s';
  if (v >= 1e3) return Math.round(v / 1e3) + ' kbit/s';
  return Math.round(v) + ' bit/s';
}
function fmtSpeed(bps) { return !bps ? '–' : bps >= 1e9 ? (bps / 1e9) + 'G' : bps >= 1e6 ? (bps / 1e6) + 'M' : (bps / 1e3) + 'k'; }
const managedAgents = customerId => S.agents.filter(a => customerOfAgent(a) === customerId && a.kind !== 'desktop');

/* ---------------- SNMP ---------------- */
S.snmp = [];
let snmpLoad = null, snmpT = 0;
async function loadSNMP(force) {
  if (!force && Date.now() - snmpT < 3000 && snmpLoad) return snmpLoad;
  snmpT = Date.now();
  snmpLoad = GET('/api/snmp').then(l => { S.snmp = l || []; return S.snmp; }).catch(() => S.snmp);
  return snmpLoad;
}
function snmpActive(d) { return Object.keys(S.active).some(k => (k.includes('|snmp:' + d.id + ':') || k.endsWith('|snmp:' + d.id)) && !S.acks[k]); }

function snmpChip(d) {
  const st = !d.t ? 'pending' : d.ok ? 'up' : 'offline';
  const el = h('span', { class: 'agent-chip snmp-chip clickable st-' + st, 'data-snmp': d.id, title: `${d.sysName || ''} ${d.host}`.trim(), onclick: () => snmpView(d.id) },
    h('span', { class: 'dot' }), h('b', null, d.name || d.host),
    h('small', null, st === 'pending' ? T('waiting for first poll') : st === 'up' ? T('{0}/{1} ports up', d.portsUp, d.ports) : T('no SNMP response')),
    d.ok && d.cpu >= 0 ? h('span', { class: 'hostmini' }, h('span', { class: 'hm ' + level(d.cpu) }, h('i', null, 'CPU'), fmtPct(d.cpu))) : null);
  if (snmpActive(d)) el.classList.add('alerting');
  return el;
}

// snmpRow ger raden med SNMP-enheter för en plats (fylls i när listan hämtats).
function snmpRow(site) {
  const row = h('div', { class: 'agents-row snmp-row', 'data-site': site.id });
  fillSnmpRow(row);
  return row;
}
function fillSnmpRow(row) {
  const site = row.dataset.site;
  const devs = S.snmp.filter(d => { const a = agentOf(d.agentId); return a && a.siteId === site; });
  row.replaceChildren(...(devs.length ? [h('span', { class: 'row-label muted' }, T('Network devices')), ...devs.map(snmpChip)] : []));
  row.style.display = devs.length ? '' : 'none';
}
function refreshSnmpRows() { document.querySelectorAll('.snmp-row').forEach(fillSnmpRow); }

function onSnmpEvent() {
  if (!document.querySelector('.snmp-row') && !snmpViewState) return;
  loadSNMP().then(() => { refreshSnmpRows(); if (snmpViewState) refreshSnmpView(); });
}

const AUTH_PROTOS = [['', T('– none –')], ['MD5', 'MD5'], ['SHA', 'SHA-1'], ['SHA256', 'SHA-256'], ['SHA512', 'SHA-512']];
const PRIV_PROTOS = [['', T('– none –')], ['DES', 'DES'], ['AES', 'AES-128'], ['AES256', 'AES-256']];

function snmpDialog(dev, customerId, agentId) {
  const d = dev ? JSON.parse(JSON.stringify(dev)) : { id: '', name: '', host: '', port: 161, version: '2c', community: 'public', intervalSec: 60, agentId: agentId || '' };
  const cust = customerId || customerOfAgent(agentOf(d.agentId));
  const ags = managedAgents(cust);
  if (!ags.length) { toast({ kind: 'info', title: T('No agent'), text: T('SNMP devices are polled by an installed agent at the site. Create an agent first.') }); return; }
  const agent = select(ags.map(a => [a.id, `${a.name} (${(siteOf(a.siteId) || {}).name || ''})`]), d.agentId || ags[0].id);
  const name = h('input', { type: 'text', value: d.name || '', placeholder: T('e.g. Core switch') });
  const host = h('input', { type: 'text', value: d.host || '', placeholder: '192.168.1.2' });
  const port = num(d.port || 161, 1, 65535);
  const ver = select([['2c', 'SNMP v2c'], ['3', 'SNMP v3'], ['1', 'SNMP v1']], d.version || '2c');
  const comm = h('input', { type: 'text', value: d.community || '', placeholder: 'public', autocomplete: 'off' });
  const user = h('input', { type: 'text', value: d.user || '', autocomplete: 'off' });
  const authP = select(AUTH_PROTOS, d.authProto || ''), privP = select(PRIV_PROTOS, d.privProto || '');
  const authPass = h('input', { type: 'password', value: d.authPass || '', autocomplete: 'new-password' });
  const privPass = h('input', { type: 'password', value: d.privPass || '', autocomplete: 'new-password' });
  const interval = select([[30, '30 s'], [60, '1 min'], [120, '2 min'], [300, '5 min']], d.intervalSec || 60);
  const cf = field('Community', comm), v3 = [field(T('User'), user, null, true), field(T('Authentication'), authP), field(T('Auth password'), authPass), field(T('Encryption'), privP), field(T('Encryption password'), privPass)];
  const syncVer = () => { const is3 = ver.value === '3'; cf.style.display = is3 ? 'none' : ''; v3.forEach(f => { f.style.display = is3 ? '' : 'none'; }); };
  ver.onchange = syncVer; syncVer();
  const collect = () => Object.assign(d, { agentId: agent.value, name: name.value.trim(), host: host.value.trim(), port: +port.value || 161, version: ver.value, community: comm.value.trim(),
    user: user.value.trim(), authProto: authP.value, authPass: authPass.value, privProto: privP.value, privPass: privPass.value, intervalSec: +interval.value });
  const result = h('div', { class: 'field full snmp-test' });
  const err = h('span', { class: 'form-error' });
  const testBtn = h('button', { class: 'btn', onclick: async () => {
    err.textContent = ''; testBtn.disabled = true;
    result.replaceChildren(h('small', { class: 'muted' }, T('Asking the agent to poll the device… (can take up to a minute)')));
    try {
      const p = await POST('/api/snmp/test', collect());
      if (!p.ok) { result.replaceChildren(h('div', { class: 'notice bad' }, TE(p.e || T('No response')))); return; }
      if (!name.value.trim() && p.sn) name.value = p.sn;
      const up = (p.ifs || []).filter(f => f.op === 1).length;
      result.replaceChildren(h('div', { class: 'notice ok' }, h('b', null, p.sn || d.host), ' – ', T('{0} ports ({1} up)', (p.ifs || []).length, up),
        p.cpu >= 0 ? ' · CPU ' + fmtPct(p.cpu) : '', p.mem >= 0 ? ' · RAM ' + fmtPct(p.mem) : '', h('div', { class: 'muted' }, (p.sd || '').slice(0, 160))));
    } catch (e) { result.replaceChildren(); err.textContent = e.message; } finally { testBtn.disabled = false; }
  } }, T('Test'));
  const close = modal({ title: dev ? T('Edit SNMP device') : T('Add SNMP device'), wide: true,
    body: h('div', { class: 'form' }, field(T('Polled by agent'), agent, T('The agent must be able to reach the device on UDP port 161.'), true),
      field(T('Address'), host), field('Port', port), field(T('Name'), name), field(T('Version'), ver), cf, ...v3, field(T('Interval'), interval), result),
    foot: [h('div', { class: 'left' }, err, testBtn, dev ? h('button', { class: 'btn danger', onclick: async () => {
      if (!await confirmDialog(T('Delete SNMP device'), T('Delete "{0}" and its history?', dev.name || dev.host), T('Delete'))) return;
      await DEL('/api/snmp/' + dev.id); close(); closeSnmpView(); await loadSNMP(true); refreshSnmpRows();
    } }, T('Delete')) : null),
      h('button', { class: 'btn', onclick: () => close() }, T('Cancel')),
      h('button', { class: 'btn primary', onclick: async () => {
        err.textContent = '';
        try { collect(); await (dev ? PUT('/api/snmp/' + dev.id, d) : POST('/api/snmp', d)); close(); await loadSNMP(true); refreshSnmpRows(); if (snmpViewState) refreshSnmpView(true); }
        catch (e) { err.textContent = e.message; }
      } }, T('Save'))] });
}

let snmpViewState = null, snmpChart = null;
const IF_RANGES = [[3600, '1 h'], [10800, '3 h'], [86400, '24 h'], [604800, '7 d'], [2592000, '30 d']];
function closeSnmpView() { const bd = snmpViewState && snmpViewState.bd; if (bd && bd._close) bd._close(); }
function snmpView(id) {
  const facts = h('div', { class: 'facts' }), meters = h('div', { class: 'meters' }), ifBox = h('div'), chartBox = h('div', { class: 'if-chart' });
  snmpViewState = { id, facts, meters, ifBox, chartBox, sel: null, range: 3600, showAll: false };
  const d = S.snmp.find(x => x.id === id) || { name: '', host: '' };
  modal({ title: d.name || d.host || 'SNMP', wide: true, body: h('div', null, facts, meters, ifBox, chartBox),
    foot: isAdmin() ? [h('div', { class: 'left muted' }, T('Polled by {0}', (agentOf(d.agentId) || {}).name || T('the agent'))),
      h('button', { class: 'btn', onclick: () => snmpDialog(S.snmp.find(x => x.id === id)) }, T('Settings…'))] : null,
    onClose: () => { snmpViewState = null; if (snmpChart) { snmpChart.destroy(); snmpChart = null; } } });
  snmpViewState.bd = document.querySelector('.backdrop:last-child');
  refreshSnmpView(true);
}
async function refreshSnmpView(full) {
  const sv = snmpViewState;
  if (!sv) return;
  let d;
  try { d = await GET('/api/snmp/' + sv.id); } catch (e) { sv.facts.replaceChildren(h('p', { class: 'form-error' }, e.message)); return; }
  if (snmpViewState !== sv) return;
  sv.dev = d;
  const fact = (k, v, cls) => h('div', null, h('small', null, k), h('b', { class: cls || '' }, v || '–'));
  sv.facts.replaceChildren(fact('Status', !d.t ? T('Waiting for first poll') : d.ok ? 'OK' : T('No response'), d.ok ? 'lvl-ok' : d.t ? 'lvl-bad' : ''),
    fact(T('Address'), `${d.host}:${d.port || 161}`), fact(T('Name (sysName)'), d.sysName), fact(T('Uptime'), d.uptime ? dur(d.uptime * 1000) : ''),
    fact(T('Response time'), d.rtt ? fmtMs(d.rtt) + ' ms' : ''), fact(T('Last poll'), d.t ? clock(d.t) : ''), fact('SNMP', 'v' + d.version));
  if (d.err && !d.ok) sv.facts.append(h('div', { class: 'full notice bad' }, TE(d.err)));
  sv.meters.replaceChildren(...[d.cpu >= 0 ? meter('CPU', d.cpu) : null, d.mem >= 0 ? meter(T('Memory'), d.mem) : null,
    d.sysDescr ? h('small', { class: 'muted descr' }, d.sysDescr) : null].filter(Boolean));
  const watch = new Set(d.watch || []);
  const ifs = (d.ifs || []).filter(f => sv.showAll || f.ad === 1 || watch.has(f.i));
  const hidden = (d.ifs || []).length - ifs.length;
  const saveWatch = async (idx, on) => {
    on ? watch.add(idx) : watch.delete(idx);
    const dev = S.snmp.find(x => x.id === d.id) || d;
    const body = Object.assign({}, dev, { watch: [...watch] });
    try { await PUT('/api/snmp/' + d.id, body); await loadSNMP(true); } catch (e) { toast({ kind: 'fire', title: T('Could not save'), text: e.message }); }
  };
  const errCell = f => h('td', { class: 'mono ' + (f.errMin > 0 ? 'lvl-high' : 'muted') }, f.errMin > 0 ? f.errMin.toFixed(f.errMin < 10 ? 1 : 0) + '/min' : '0');
  sv.ifBox.replaceChildren(h('div', { class: 'section-head', style: 'margin-top:14px' }, h('h2', null, T('Ports')),
    h('div', { class: 'tools' }, hidden > 0 || sv.showAll ? h('button', { class: 'btn ghost sm', onclick: () => { sv.showAll = !sv.showAll; refreshSnmpView(); } }, sv.showAll ? T('Hide disabled ports') : T('Show {0} disabled ports', hidden)) : null)),
    ifs.length ? h('div', { class: 'tbl-wrap' }, h('table', { class: 'tbl compact if-tbl' },
      h('thead', null, h('tr', null, ...[T('Port'), 'Status', T('Speed'), T('In'), T('Out'), T('Load'), T('Errors'), isAdmin() ? T('Watch') : null].filter(x => x !== null).map(x => h('th', null, x)))),
      h('tbody', null, ...ifs.map(f => h('tr', { class: 'clickable' + (sv.sel === f.i ? ' sel' : ''), onclick: e => { if (e.target.closest('input')) return; sv.sel = f.i; refreshSnmpView(); loadIfChart(); } },
        h('td', null, h('b', null, f.n || '#' + f.i), f.a ? h('div', { class: 'muted' }, f.a) : null),
        h('td', null, h('span', { class: 'pill ' + (f.ad !== 1 ? '' : f.op === 1 ? 'st-up' : 'st-down') }, h('span', { class: 'dot' }), f.ad !== 1 ? T('disabled') : f.op === 1 ? 'up' : 'down')),
        h('td', { class: 'mono' }, fmtSpeed(f.sp)),
        h('td', { class: 'mono' }, fmtBps(f.inBps)), h('td', { class: 'mono' }, fmtBps(f.outBps)),
        h('td', { class: 'mono ' + (f.util >= 0 ? 'lvl-' + (level(f.util) || 'ok') : 'muted') }, f.util >= 0 ? fmtPct(f.util) : '–'),
        errCell(f),
        isAdmin() ? h('td', null, h('input', { type: 'checkbox', checked: watch.has(f.i), title: T('Alert when the port goes down (rule "Watched port down")'), onchange: e => saveWatch(f.i, e.target.checked) })) : null)))))
      : h('p', { class: 'muted' }, d.ok ? T('The device reports no ports.') : T('No data yet.')));
  if (sv.sel == null && ifs.length) {
    const busiest = ifs.filter(f => f.op === 1).sort((a, b) => (b.inBps + b.outBps) - (a.inBps + a.outBps))[0] || ifs[0];
    sv.sel = busiest.i;
    sv.ifBox.querySelectorAll('tbody tr').forEach((tr, i) => tr.classList.toggle('sel', ifs[i].i === sv.sel));
  }
  if (full) loadIfChart();
}
async function loadIfChart() {
  const sv = snmpViewState;
  if (!sv || sv.sel == null) { sv && sv.chartBox.replaceChildren(); return; }
  const f = ((sv.dev || {}).ifs || []).find(x => x.i === sv.sel) || { n: '#' + sv.sel };
  const box = h('div', { class: 'hchart' });
  sv.chartBox.replaceChildren(h('div', { class: 'section-head', style: 'margin-top:14px' }, h('h2', null, T('Traffic – {0}', f.n || '#' + f.i)),
    h('div', { class: 'tools' }, h('div', { class: 'ranges' }, ...IF_RANGES.map(([sec, label]) => h('button', { class: sv.range === sec ? 'on' : '', onclick: () => { sv.range = sec; loadIfChart(); } }, label))))), box);
  const to = Date.now(), from = to - sv.range * 1000;
  const hs = await GET(`/api/snmp/${sv.id}/history?if=${sv.sel}&from=${from}&to=${to}`);
  if (snmpViewState !== sv) return;
  if (snmpChart) { snmpChart.destroy(); snmpChart = null; }
  if (!hs.t.length) { box.append(h('p', { class: 'muted' }, T('No history for the period yet.'))); return; }
  const c = css();
  const axis = { stroke: c.muted, grid: { stroke: c.border, width: 1 }, ticks: { stroke: c.border, width: 1 }, font: '11px ' + getComputedStyle(document.body).fontFamily };
  const pv = (u, v) => v == null ? '–' : fmtBps(v);
  const pts = { show: hs.t.length < 90, size: 4 };
  snmpChart = new uPlot({
    width: box.clientWidth || 600, height: 210,
    scales: { x: { time: true, range: () => [from / 1000, to / 1000] }, y: { range: (u, mn, mx) => [0, Math.max(1000, mx * 1.1)] } },
    axes: [Object.assign({}, axis, { values: (u, sp) => sp.map(v => timeLabel(v, sv.range)) }), Object.assign({}, axis, { size: 80, values: (u, sp) => sp.map(v => fmtBps(v)) })],
    series: [{ label: T('Time'), value: (u, v) => v == null ? '–' : new Date(v * 1000).toLocaleString('sv-SE') },
      { label: T('In'), stroke: c.accent, width: 1.6, fill: hexA(c.accent, .12), value: pv, points: pts },
      { label: T('Out'), stroke: '#8b7cf6', width: 1.6, fill: 'rgba(139,124,246,.10)', value: pv, points: pts },
      { label: T('Peak in'), stroke: hexA(c.accent, .4), width: 1, dash: [3, 3], value: pv, points: { show: false }, show: sv.range > 10800 },
      { label: T('Peak out'), stroke: 'rgba(139,124,246,.4)', width: 1, dash: [3, 3], value: pv, points: { show: false }, show: sv.range > 10800 }],
    legend: { live: true }, cursor: { drag: { x: false, y: false } },
  }, [hs.t.map(t => t / 1000), hs.in, hs.out, hs.inMax, hs.outMax], box);
}

/* ---------------- hastighetstest ---------------- */
let speedChart = null;
async function renderSpeed(agentId) {
  const box = $('#hvSpeed');
  if (!box) return;
  const a = agentOf(agentId);
  if (!a || a.kind === 'desktop') { box.replaceChildren(); return; }
  let res;
  try { res = await GET(`/api/agents/${agentId}/speed?from=${Date.now() - 30 * 86400000}`); } catch (e) { box.replaceChildren(); return; }
  if (!$('#hvSpeed')) return;
  const list = res.results || [], cfg = res.config || {};
  const last = list.filter(r => !r.err).slice(-1)[0];
  const status = h('span', { class: 'muted' });
  const target = select([['cloudflare', 'Cloudflare'], ['server', T('PingVakt server')]], cfg.target || 'cloudflare');
  const run = h('button', { class: 'btn sm primary', onclick: async () => {
    run.disabled = true; status.className = 'muted'; status.textContent = T('Measuring… (about 30 seconds)');
    try { const r = await POST(`/api/agents/${agentId}/speed/run`, { target: target.value });
      status.className = r.err ? 'form-error' : 'form-ok'; status.textContent = r.err ? TE(r.err) : `↓ ${r.down.toFixed(1)}  ↑ ${r.up.toFixed(1)} Mbit/s · ${r.latency.toFixed(1)} ms`;
      setTimeout(() => renderSpeed(agentId), 4000);
    } catch (e) { status.className = 'form-error'; status.textContent = e.message; } finally { run.disabled = false; }
  } }, T('Run speed test'));
  const sched = select([[0, T('Off')], [60, T('Every hour')], [240, T('Every 4 hours')], [720, T('Twice a day')], [1440, T('Once a day')]], cfg.intervalMin || 0);
  const saveCfg = async () => { try { await PUT(`/api/agents/${agentId}/speed`, { intervalMin: +sched.value, target: target.value }); status.className = 'form-ok'; status.textContent = T('Saved.'); } catch (e) { status.className = 'form-error'; status.textContent = e.message; } };
  sched.onchange = saveCfg;
  const fact = (k, v) => h('div', null, h('small', null, k), h('b', null, v || '–'));
  const chartBox = h('div', { class: 'hchart' });
  box.replaceChildren(h('h4', null, T('Speed test')),
    last ? h('div', { class: 'facts' }, fact(T('Download'), last.down.toFixed(1) + ' Mbit/s'), fact(T('Upload'), last.up.toFixed(1) + ' Mbit/s'),
      fact(T('Latency'), last.latency.toFixed(1) + ' ms'), fact('Jitter', last.jitter.toFixed(1) + ' ms'), fact(T('Measured'), dateTime(last.t)), fact(T('Against'), last.target === 'server' ? T('PingVakt server') : 'Cloudflare'))
      : h('p', { class: 'muted' }, T('No speed tests yet.')),
    h('div', { class: 'row speed-tools' }, target, run, isAdmin() ? h('label', { class: 'row' }, h('span', { class: 'muted' }, T('Automatic')), sched) : null, status),
    list.length > 1 ? chartBox : '');
  if (isAdmin()) target.onchange = () => { if (+sched.value) saveCfg(); };
  if (speedChart) { speedChart.destroy(); speedChart = null; }
  if (list.length < 2) return;
  const ok = list.filter(r => !r.err);
  const span = ok.length > 1 ? (ok[ok.length - 1].t - ok[0].t) / 1000 : 3600;
  const c = css();
  const axis = { stroke: c.muted, grid: { stroke: c.border, width: 1 }, ticks: { stroke: c.border, width: 1 }, font: '11px ' + getComputedStyle(document.body).fontFamily };
  const pv = (u, v) => v == null ? '–' : v.toFixed(1) + ' Mbit/s';
  speedChart = new uPlot({
    width: chartBox.clientWidth || 600, height: 180,
    scales: { x: { time: true }, y: { range: (u, mn, mx) => [0, Math.max(1, mx * 1.1)] } },
    axes: [Object.assign({}, axis, { values: (u, sp) => sp.map(v => timeLabel(v, span)) }), Object.assign({}, axis, { size: 60, values: (u, sp) => sp.map(v => v + ' M') })],
    series: [{ label: T('Time'), value: (u, v) => v == null ? '–' : new Date(v * 1000).toLocaleString('sv-SE') },
      { label: T('Download'), stroke: c.accent, width: 1.6, value: pv, points: { show: true, size: 5 } },
      { label: T('Upload'), stroke: '#8b7cf6', width: 1.6, value: pv, points: { show: true, size: 5 } }],
    legend: { live: true }, cursor: { drag: { x: false, y: false } },
  }, [ok.map(r => r.t / 1000), ok.map(r => r.down), ok.map(r => r.up)], chartBox);
  hostCharts.push(speedChart);
}

/* ---------------- mallar ---------------- */
async function templateDialog(customerId, agentId) {
  const ags = managedAgents(customerId);
  if (!ags.length) { toast({ kind: 'info', title: T('No agent'), text: T('Create an agent first – the agent is what measures the targets.') }); return agentDialog(customerId); }
  const agent = select(ags.map(a => [a.id, `${a.name} (${(siteOf(a.siteId) || {}).name || ''})`]), agentId || ags[0].id);
  const tplSel = h('div', { class: 'tpl-list' });
  const preview = h('div', { class: 'tpl-preview' });
  const group = h('input', { type: 'text', placeholder: T('Optional, e.g. Basics') });
  const gw = h('input', { type: 'text', placeholder: T('detected automatically') });
  const dns = h('input', { type: 'text', placeholder: T('detected automatically') });
  const err = h('span', { class: 'form-error' });
  let data = null, chosen = null;
  const skip = new Set();
  const load = async () => {
    preview.replaceChildren(h('p', { class: 'muted' }, T('Loading…')));
    data = await GET('/api/templates?agent=' + agent.value);
    gw.placeholder = data.gateway || T('not reported by the agent');
    dns.placeholder = (data.dns || []).join(', ') || T('not reported by the agent');
    if (!chosen || !data.templates.some(t => t.id === chosen)) chosen = data.templates[0] && data.templates[0].id;
    drawList(); drawPreview();
  };
  const drawList = () => tplSel.replaceChildren(...data.templates.map(t => h('label', { class: 'tpl' + (t.id === chosen ? ' on' : '') },
    h('input', { type: 'radio', name: 'tpl', checked: t.id === chosen, onchange: () => { chosen = t.id; skip.clear(); drawList(); drawPreview(); } }),
    h('div', null, h('b', null, t.name), h('small', { class: 'muted' }, t.note || T('{0} targets', t.targets.length))))));
  const drawPreview = () => {
    const t = data.templates.find(x => x.id === chosen);
    if (!t) { preview.replaceChildren(); return; }
    const manual = { '{gateway}': gw.value.trim(), '{dns1}': dns.value.split(/[ ,;]+/)[0] || '' };
    preview.replaceChildren(h('table', { class: 'tbl compact' }, h('thead', null, h('tr', null, h('th', null, ''), h('th', null, T('Name')), h('th', null, T('Check')), h('th', null, T('Host')))),
      h('tbody', null, ...t.resolved.map((r, i) => {
        const fill = r.missing && manual[r.missing];
        const host = fill ? r.host || manual[r.missing] : r.host;
        const off = (r.missing && !fill) || r.exists;
        return h('tr', { class: off ? 'muted' : '' },
          h('td', null, h('input', { type: 'checkbox', disabled: off, checked: !off && !skip.has(i), onchange: e => e.target.checked ? skip.delete(i) : skip.add(i) })),
          h('td', null, r.name || '–'), h('td', null, kindText(r)),
          h('td', { class: 'mono' }, r.exists ? T('{0} – already added', host) : r.missing && !fill ? T('{0} unknown – fill in below', r.missing) : host + (r.dnsServer && r.kind === 'dns' ? ' @' + (r.dnsServer || manual['{dns1}']) : '')));
      }))));
  };
  gw.oninput = dns.oninput = () => data && drawPreview();
  agent.onchange = () => { skip.clear(); load(); };
  const close = modal({ title: T('Add targets from template'), wide: true,
    body: h('div', { class: 'form' }, field(T('Measuring agent'), agent, null, true), h('div', { class: 'field full' }, h('span', null, T('Template')), tplSel),
      h('div', { class: 'field full' }, h('span', null, T('Targets')), preview),
      field(T('Gateway'), gw, T('{gateway} in the template – leave empty to use what the agent reports.')), field(T('DNS server'), dns, T('{dns1} in the template.')),
      field(T('Group'), group, T('Targets after the gateway are set to depend on it, so a gateway outage gives one alert instead of many.'), true)),
    foot: [h('div', { class: 'left' }, err, isAdmin() ? h('button', { class: 'btn ghost', onclick: () => { close(); location.hash = '#/settings'; setTimeout(() => { const el = $('#tplSection'); if (el) el.scrollIntoView(); }, 300); } }, T('Manage templates…')) : null),
      h('button', { class: 'btn', onclick: () => close() }, T('Cancel')),
      h('button', { class: 'btn primary', onclick: async () => {
        err.textContent = '';
        try { const added = await POST(`/api/templates/${chosen}/apply`, { agentId: agent.value, group: group.value.trim(), skip: [...skip], gateway: gw.value.trim(), dns: dns.value.trim() });
          close(); await afterChange(); toast({ kind: 'clear', title: T('Targets added'), text: T('{0} targets from the template.', added.length) });
        } catch (e) { err.textContent = e.message; }
      } }, T('Add'))] });
  try { await load(); } catch (e) { err.textContent = e.message; }
}

// templateEditor redigerar en egen mall (eller skapar en ny).
function templateEditor(tpl, onDone) {
  const t = tpl ? JSON.parse(JSON.stringify(tpl)) : { id: '', name: '', note: '', targets: [{ name: T('Gateway'), kind: 'icmp', host: '{gateway}' }] };
  const name = h('input', { type: 'text', value: t.name, placeholder: T('e.g. Standard office') });
  const note = h('input', { type: 'text', value: t.note || '', placeholder: T('Optional description') });
  const rowsBox = h('tbody');
  const rows = [];
  const addRow = tt => {
    const r = { name: h('input', { type: 'text', value: tt.name || '' }), kind: select(KINDS, tt.kind || 'icmp'), host: h('input', { type: 'text', value: tt.host || '', placeholder: '{gateway}, 1.1.1.1, https://…' }),
      port: num(tt.port || '', 0, 65535), dns: h('input', { type: 'text', value: tt.dnsServer || '', placeholder: '{dns1}' }) };
    const tr = h('tr', null, h('td', null, r.name), h('td', null, r.kind), h('td', null, r.host), h('td', null, r.port), h('td', null, r.dns),
      h('td', null, h('button', { class: 'icon-btn', title: T('Remove'), onclick: () => { tr.remove(); rows.splice(rows.indexOf(r), 1); } }, '✕')));
    rows.push(r); rowsBox.append(tr);
  };
  t.targets.forEach(addRow);
  formDialog({ title: tpl ? T('Edit template') : T('New template'), wide: true, saveLabel: T('Save'), fields: [field(T('Name'), name), field(T('Description'), note),
    h('div', { class: 'field full' }, h('span', null, T('Targets')),
      h('div', { class: 'tbl-wrap' }, h('table', { class: 'tbl compact tpl-edit' }, h('thead', null, h('tr', null, ...[T('Name'), T('Check'), T('Host'), 'Port', T('DNS server'), ''].map(x => h('th', null, x)))), rowsBox)),
      h('div', { class: 'row' }, h('button', { class: 'btn sm', type: 'button', onclick: () => addRow({}) }, T('+ Add row'))),
      h('small', null, T('Placeholders: {gateway} = the agent\'s default gateway, {dns1} and {dns2} = its DNS servers. They are filled in when the template is used.')))],
    onSave: async () => {
      Object.assign(t, { name: name.value.trim(), note: note.value.trim(), targets: rows.map(r => ({ name: r.name.value.trim(), kind: r.kind.value, host: r.host.value.trim(), port: +r.port.value || 0, dnsServer: r.dns.value.trim() })) });
      await (t.id ? PUT('/api/templates/' + t.id, t) : POST('/api/templates', t));
      onDone && onDone();
    } });
}

async function templateSection() {
  const box = h('div', { class: 'tpl-admin' }, h('p', { class: 'muted' }, T('Loading…')));
  const draw = async () => {
    const res = await GET('/api/templates');
    box.replaceChildren(h('table', { class: 'tbl compact' }, h('thead', null, h('tr', null, h('th', null, T('Name')), h('th', null, T('Targets')), h('th', null, ''))),
      h('tbody', null, ...res.templates.map(t => h('tr', null, h('td', null, h('b', null, t.name), t.builtin ? h('span', { class: 'tag' }, T('built-in')) : null, t.note ? h('div', { class: 'muted' }, t.note) : null),
        h('td', { class: 'muted' }, t.targets.map(x => x.name || x.host).join(', ')),
        h('td', { class: 'actions' }, t.builtin ? null : [h('button', { class: 'btn sm', onclick: () => templateEditor(t, draw) }, T('Edit')),
          h('button', { class: 'btn sm danger', onclick: async () => { if (!await confirmDialog(T('Delete template'), T('Delete "{0}"?', t.name), T('Delete'))) return; await DEL('/api/templates/' + t.id); draw(); } }, '✕')]))))),
      h('div', { class: 'row', style: 'margin-top:8px' }, h('button', { class: 'btn sm', onclick: () => templateEditor(null, draw) }, T('+ New template')),
        h('small', { class: 'muted' }, T('Tip: build the targets on one agent and choose "Save targets as template" in the agent menu.'))));
  };
  draw().catch(e => box.replaceChildren(h('p', { class: 'form-error' }, e.message)));
  return box;
}

function saveAsTemplateDialog(a) {
  const name = h('input', { type: 'text', value: '', placeholder: T('e.g. Standard office') });
  formDialog({ title: T('Save targets as template'), saveLabel: T('Save'), fields: [field(T('Name'), name, T('The agent\'s gateway and DNS servers are replaced with placeholders so the template works on other networks.'), true)],
    onSave: async () => { await POST('/api/templates', { name: name.value.trim(), fromAgent: a.id }); toast({ kind: 'clear', title: T('Template saved'), text: name.value.trim() }); } });
}

/* ---------------- Wi-Fi-survey ---------------- */
const BANDS = {
  '2.4': { lo: 2401, hi: 2495, ticks: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13], labels: null, f: c => c === 14 ? 2484 : 2407 + c * 5 },
  '5': { lo: 5150, hi: 5895, ticks: [36, 40, 44, 48, 52, 56, 60, 64, 100, 104, 108, 112, 116, 120, 124, 128, 132, 136, 140, 144, 149, 153, 157, 161, 165, 169, 173, 177],
    labels: [36, 44, 52, 60, 100, 108, 116, 124, 132, 140, 149, 157, 165, 173], f: c => 5000 + c * 5 },
  '6': { lo: 5925, hi: 7125, ticks: Array.from({ length: 59 }, (_, i) => 1 + i * 4), labels: [1, 33, 65, 97, 129, 161, 193, 225], f: c => 5950 + c * 5 },
};
const bandOf = f => f >= 5925 ? '6' : f >= 4900 ? '5' : '2.4';
const SV_PAL = ['#5b9cf5', '#8b7cf6', '#f2b33d', '#e46aa8', '#3ccf7a', '#ef8a50', '#4fc1c9', '#c1d045', '#b07ae0', '#f07a7a'];
function ssidColor(ssid) { let h = 0; for (const ch of ssid || '?') h = (h * 31 + ch.charCodeAt(0)) >>> 0; return SV_PAL[h % SV_PAL.length]; }

let surveyState = null;
async function renderSurvey(agentId, at) {
  const box = $('#hvSurvey');
  if (!box) return;
  let res;
  try { res = await GET(`/api/agents/${agentId}/survey${at ? '?t=' + at : ''}`); } catch (e) { box.replaceChildren(); return; }
  if (!$('#hvSurvey')) return;
  const a = agentOf(agentId) || {};
  const sc = res.scan, cur = res.current;
  const prev = surveyState && surveyState.agentId === agentId ? surveyState : {};
  surveyState = { agentId, res, band: prev.band || (cur && cur.freq ? bandOf(cur.freq) : (sc && sc.bss.length ? bandOf(sc.bss[0].freq) : '2.4')), filter: prev.filter || '', showAll: false };
  if (!sc && !cur && !isAdmin()) { box.replaceChildren(); return; }
  const status = h('span', { class: 'muted' });
  const run = h('button', { class: 'btn sm primary', onclick: async () => {
    run.disabled = true; status.className = 'muted'; status.textContent = T('Scanning… (up to a minute)');
    try { await POST(`/api/agents/${agentId}/survey/run`); status.textContent = ''; renderSurvey(agentId); }
    catch (e) { status.className = 'form-error'; status.textContent = TE(e.message); } finally { run.disabled = false; }
  } }, T('Scan now'));
  const sched = select([[0, T('Off')], [5, T('Every 5 minutes')], [15, T('Every 15 minutes')], [60, T('Every hour')]], res.interval || 0);
  sched.onchange = async () => { try { await PUT(`/api/agents/${agentId}/survey`, { intervalMin: +sched.value }); status.className = 'form-ok'; status.textContent = T('Saved.'); } catch (e) { status.className = 'form-error'; status.textContent = e.message; } };
  const times = res.times || [];
  const hist = times.length > 1 ? select(times.map(t => [t, dateTime(t)]), sc ? sc.t : '') : null;
  if (hist) hist.onchange = () => renderSurvey(agentId, +hist.value === times[0] ? 0 : +hist.value);
  const tools = h('div', { class: 'row speed-tools' }, run, hist, isAdmin() && a.kind === 'agent' ? h('label', { class: 'row' }, h('span', { class: 'muted' }, T('Automatic')), sched) : null, status);
  const head = h('h4', null, T('Wi-Fi networks nearby'));
  if (!sc) { box.replaceChildren(head, h('p', { class: 'muted' }, T('No scan yet. Scanning shows neighbouring networks, channels and signal strength as heard by this computer.')), tools); return; }
  const body = h('div');
  box.replaceChildren(...[head, tools, sc.err && !sc.bss.length ? h('div', { class: 'notice bad' }, TE(sc.err)) : null, body].filter(Boolean));
  drawSurvey(body);
}

function surveyInsights(list, cur) {
  const out = [];
  if (cur && cur.ch) {
    const mine = list.filter(b => b.ch === cur.ch && bandOf(b.freq) === bandOf(cur.freq || 0) && !(b.bssid && cur.bssid && b.bssid === cur.bssid.toLowerCase()));
    const strong = mine.filter(b => b.rssi >= -82);
    out.push([strong.length > 3 ? 'bad' : strong.length > 1 ? 'high' : 'ok', T('{0} other networks on channel {1} above −82 dBm (co-channel interference)', strong.length, cur.ch)]);
  }
  const odd = list.filter(b => bandOf(b.freq) === '2.4' && ![1, 6, 11].includes(b.ch) && b.rssi >= -85);
  if (odd.length) out.push(['high', T('{0} networks in 2.4 GHz use channels other than 1, 6 and 11 and overlap their neighbours', odd.length)]);
  const wide = list.filter(b => bandOf(b.freq) === '2.4' && b.width >= 40);
  if (wide.length) out.push(['high', T('{0} networks use 40 MHz in 2.4 GHz', wide.length)]);
  const open = list.filter(b => b.sec === 'Open' || b.sec === 'WEP');
  if (open.length) out.push(['high', T('{0} open or WEP networks', open.length)]);
  return out;
}

function drawSurvey(body) {
  const st = surveyState, sc = st.res.scan, cur = st.res.current;
  const list = sc.bss;
  const counts = { '2.4': 0, '5': 0, '6': 0 };
  for (const b of list) counts[bandOf(b.freq)]++;
  const tabs = h('div', { class: 'ranges' }, ...['2.4', '5', '6'].map(bd => h('button', { class: st.band === bd ? 'on' : '', onclick: () => { st.band = bd; drawSurvey(body); } }, `${bd} GHz (${counts[bd]})`)));
  const cv = h('canvas', { class: 'chan-canvas' });
  const filter = h('input', { type: 'search', placeholder: T('Filter on SSID, BSSID or manufacturer'), value: st.filter });
  const tbl = h('div', { class: 'tbl-wrap' });
  const fact = (k, v) => h('div', null, h('small', null, k), h('b', null, v || '–'));
  const ssids = new Set(list.map(b => b.ssid || '(hidden)'));
  const ins = surveyInsights(list, cur);
  body.replaceChildren(
    h('div', { class: 'facts' }, fact(T('Scanned'), dateTime(sc.t)), fact(T('Access points (BSSID)'), String(list.length)), fact(T('Networks (SSID)'), String(ssids.size)),
      cur ? fact(T('Connected to'), `${cur.ssid || ''} · ${T('channel')} ${cur.ch}`) : null, sc.tool ? fact(T('Source'), sc.tool) : null),
    ins.length ? h('ul', { class: 'insights' }, ...ins.map(([lv, t]) => h('li', { class: 'lvl-' + lv }, t))) : '',
    h('div', { class: 'section-head', style: 'margin-top:10px' }, h('h2', null, T('Channels')), h('div', { class: 'tools' }, tabs)), cv,
    h('div', { class: 'row', style: 'margin:10px 0 6px' }, filter), tbl);
  const drawTable = () => {
    const q = filter.value.trim().toLowerCase(); st.filter = filter.value;
    const rows = list.filter(b => !q || (b.ssid || '').toLowerCase().includes(q) || (b.bssid || '').includes(q) || (b.vendor || '').toLowerCase().includes(q));
    const shown = st.showAll ? rows : rows.slice(0, 60);
    tbl.replaceChildren(h('table', { class: 'tbl compact survey-tbl' },
      h('thead', null, h('tr', null, ...['SSID', 'BSSID', T('Manufacturer'), T('Channel'), T('Width'), 'Signal', T('Security'), 'Standard'].map(x => h('th', null, x)))),
      h('tbody', null, ...shown.map(b => h('tr', { class: b.conn || (cur && cur.bssid && b.bssid === cur.bssid.toLowerCase()) ? 'sel' : '' },
        h('td', null, h('span', { class: 'sw', style: `background:${ssidColor(b.ssid)}` }), b.ssid ? h('b', null, b.ssid) : h('i', { class: 'muted' }, T('hidden'))),
        h('td', { class: 'mono' }, b.bssid || '–'), h('td', { class: 'muted' }, b.vendor || (b.random ? T('(virtual BSSID)') : '')),
        h('td', { class: 'mono' }, `${b.ch} · ${bandOf(b.freq)} GHz`), h('td', { class: 'mono' }, b.width + ' MHz'),
        h('td', { class: 'mono lvl-' + rssiLevel(b.rssi) }, b.rssi + ' dBm'),
        h('td', { class: b.sec === 'Open' || b.sec === 'WEP' ? 'lvl-high' : '' }, b.sec || '–'), h('td', { class: 'muted' }, b.std || ''))))),
      rows.length > shown.length ? h('button', { class: 'btn ghost sm', onclick: () => { st.showAll = true; drawTable(); } }, T('Show all {0}', rows.length)) : '');
  };
  filter.oninput = drawTable;
  drawTable();
  requestAnimationFrame(() => drawChannels(cv, list.filter(b => bandOf(b.freq) === st.band), st.band, cur));
}

// chanSpan ger frekvensområdet ett nät upptar, med kanalsammanslagning (40/80/160 MHz) i rätt block.
function chanSpan(b) {
  const w = b.width || 20, f = b.freq;
  if (w <= 20) return [f - 10, f + 10];
  if (f < 4900) return b.ch <= 7 ? [f - 10, f + 30] : [f - 30, f + 10];
  const base = f >= 5925 ? 5945 : f < 5490 ? 5170 : f < 5735 ? 5490 : 5735;
  const start = base + Math.floor((f - base) / w) * w;
  return [start, start + w];
}
function drawChannels(cv, list, band, cur) {
  const B = BANDS[band], c = css();
  const W = cv.parentElement ? cv.parentElement.clientWidth : 600, H = 230, dpr = window.devicePixelRatio || 1;
  cv.width = W * dpr; cv.height = H * dpr; cv.style.width = W + 'px'; cv.style.height = H + 'px';
  const g = cv.getContext('2d'); g.scale(dpr, dpr);
  const L = 44, R = 8, T0 = 10, Bm = 26, pw = W - L - R, ph = H - T0 - Bm;
  const x = f => L + (f - B.lo) / (B.hi - B.lo) * pw;
  const y = r => T0 + (Math.min(-20, Math.max(-100, r)) + 20) / -80 * ph;
  g.font = '11px ' + getComputedStyle(document.body).fontFamily;
  g.strokeStyle = c.border; g.fillStyle = c.muted; g.lineWidth = 1;
  for (const r of [-30, -50, -67, -80, -90]) { g.beginPath(); g.moveTo(L, y(r)); g.lineTo(W - R, y(r)); g.setLineDash(r === -67 ? [4, 3] : []); g.stroke(); g.fillText(r, 8, y(r) + 4); }
  g.setLineDash([]);
  for (const ch of B.ticks) {
    const px = x(B.f(ch));
    g.beginPath(); g.moveTo(px, T0 + ph); g.lineTo(px, T0 + ph + 4); g.stroke();
    if (!B.labels || B.labels.includes(ch)) { const t = String(ch); g.fillText(t, px - g.measureText(t).width / 2, H - 8); }
  }
  const curB = cur && cur.bssid ? cur.bssid.toLowerCase() : '';
  const sorted = list.slice().sort((a, b) => a.rssi - b.rssi);
  const labels = [];
  for (const b of sorted) {
    const [f0, f1] = chanSpan(b), x0 = x(f0), x1 = x(f1), top = y(b.rssi), base = T0 + ph, slope = Math.min(10, (x1 - x0) / 5);
    const mine = b.conn || (curB && b.bssid === curB);
    const col = ssidColor(b.ssid);
    g.beginPath(); g.moveTo(x0, base); g.lineTo(x0 + slope, top); g.lineTo(x1 - slope, top); g.lineTo(x1, base);
    g.fillStyle = hexA(col, mine ? .35 : .12); g.fill();
    g.strokeStyle = mine ? c.accent : col; g.lineWidth = mine ? 2.5 : 1.2; g.stroke();
    labels.push({ x: (x0 + x1) / 2, y: top, t: b.ssid || T('hidden'), col: mine ? c.accent : col, r: b.rssi });
  }
  // etiketter för de starkaste, utan att de krockar
  g.lineWidth = 1;
  const placed = [];
  for (const lb of labels.sort((a, b) => b.r - a.r).slice(0, 18)) {
    const w = g.measureText(lb.t).width; let ly = lb.y - 4;
    while (placed.some(p => Math.abs(p.x - lb.x) < (p.w + w) / 2 + 4 && Math.abs(p.y - ly) < 12)) ly -= 12;
    if (ly < T0 + 8) continue;
    placed.push({ x: lb.x, y: ly, w });
    g.fillStyle = lb.col; g.fillText(lb.t, Math.max(L, Math.min(W - R - w, lb.x - w / 2)), ly);
  }
  if (!list.length) { g.fillStyle = c.muted; const t = T('No networks in this band'); g.fillText(t, L + pw / 2 - g.measureText(t).width / 2, T0 + ph / 2); }
}

/* ---------------- UniFi, Meraki och Aruba Central ---------------- */
const VENDORS = [['unifi', 'Ubiquiti UniFi'], ['meraki', 'Cisco Meraki'], ['aruba', 'HPE Aruba Central']];
const vendorLabel = k => (VENDORS.find(v => v[0] === k) || [k, k])[1];
const DEV_TYPES = { ap: T('Access point'), switch: 'Switch', gateway: 'Gateway', other: T('Other') };
S.integrations = {};

function integRow(customerId) {
  const row = h('div', { class: 'agents-row integ-row', 'data-customer': customerId, style: 'display:none' });
  loadIntegRow(row);
  return row;
}
async function loadIntegRow(row) {
  const cid = row.dataset.customer;
  let list = [];
  try { list = await GET('/api/integrations?customer=' + cid); } catch (e) { /* inte inloggad längre */ }
  S.integrations[cid] = list;
  row.replaceChildren(...(list.length ? [h('span', { class: 'row-label muted' }, T('Integrations')), ...list.map(integChip)] : []));
  row.style.display = list.length ? '' : 'none';
}
function refreshIntegRows() { document.querySelectorAll('.integ-row').forEach(loadIntegRow); }
function integChip(v) {
  const st = !v.t ? 'pending' : v.err ? 'offline' : v.online < v.total ? 'warn' : 'up';
  const el = h('span', { class: 'agent-chip vendor-chip clickable st-' + (st === 'warn' ? 'up' : st), onclick: () => integrationView(v.id) },
    h('span', { class: 'dot' }), h('b', null, v.name),
    h('small', null, !v.t ? T('waiting for first poll') : v.err ? T('cannot connect') : T('{0}/{1} devices online', v.online, v.total)));
  if (st === 'warn' || Object.keys(S.active).some(k => k.includes('|int:' + v.id + ':') && !S.acks[k])) el.classList.add('alerting');
  return el;
}

let integViewState = null;
async function integrationView(id) {
  const body = h('div', null, h('p', { class: 'muted' }, T('Loading…')));
  integViewState = { id, body, sel: new Set(), filter: '' };
  const v0 = Object.values(S.integrations).flat().find(x => x.id === id) || { name: '' };
  modal({ title: v0.name || T('Integration'), wide: true, body,
    foot: isAdmin() ? [h('div', { class: 'left' }, h('button', { class: 'btn', id: 'integAdd', disabled: true, onclick: () => addIntegTargets() }, T('Add as ping targets…'))),
      h('button', { class: 'btn', onclick: async () => { const v = await GET('/api/integrations/' + id); integrationDialog(v, v.customerId); } }, T('Settings…'))] : null,
    onClose: () => { integViewState = null; } });
  refreshIntegView();
}
async function refreshIntegView() {
  const st = integViewState;
  if (!st) return;
  let v;
  try { v = await GET('/api/integrations/' + st.id); } catch (e) { st.body.replaceChildren(h('p', { class: 'form-error' }, e.message)); return; }
  if (integViewState !== st) return;
  st.v = v;
  const devs = v.devices || [];
  const fact = (k, val, cls) => h('div', null, h('small', null, k), h('b', { class: cls || '' }, val || '–'));
  const filter = h('input', { type: 'search', placeholder: T('Filter on name, model or IP'), value: st.filter });
  const tbl = h('div', { class: 'tbl-wrap' });
  const draw = () => {
    st.filter = filter.value; const q = filter.value.trim().toLowerCase();
    const rows = devs.filter(d => !q || [d.name, d.model, d.ip, d.mac, d.site].some(x => (x || '').toLowerCase().includes(q)));
    tbl.replaceChildren(h('table', { class: 'tbl compact' },
      h('thead', null, h('tr', null, ...[isAdmin() ? '' : null, 'Status', T('Name'), T('Type'), T('Model'), 'IP', T('Clients'), T('Uptime'), 'Firmware', T('Site')].filter(x => x !== null).map(x => h('th', null, x)))),
      h('tbody', null, ...rows.map(d => h('tr', null,
        isAdmin() ? h('td', null, h('input', { type: 'checkbox', disabled: !d.ip, checked: st.sel.has(d.id), onchange: e => { e.target.checked ? st.sel.add(d.id) : st.sel.delete(d.id); const b = $('#integAdd'); if (b) b.disabled = !st.sel.size; } })) : null,
        h('td', null, h('span', { class: 'pill ' + (d.online ? 'st-up' : 'st-down') }, h('span', { class: 'dot' }), d.online ? 'online' : (d.status || 'offline'))),
        h('td', null, h('b', null, d.name), d.mac ? h('div', { class: 'muted mono' }, d.mac) : null),
        h('td', null, DEV_TYPES[d.type] || d.type || ''), h('td', { class: 'muted' }, d.model || ''), h('td', { class: 'mono' }, d.ip || '–'),
        h('td', { class: 'mono' }, d.clients ? String(d.clients) : '–'), h('td', { class: 'mono' }, d.uptime ? dur(d.uptime * 1000) : '–'),
        h('td', { class: 'muted' }, d.firmware || ''), h('td', { class: 'muted' }, d.site || ''))))));
  };
  filter.oninput = draw;
  const byType = {};
  for (const d of devs) { const k = d.type || 'other'; byType[k] = byType[k] || [0, 0]; byType[k][0]++; if (d.online) byType[k][1]++; }
  st.body.replaceChildren(h('div', { class: 'facts' }, fact(T('System'), vendorLabel(v.kind)), fact('Status', !v.t ? T('Waiting for first poll') : v.err ? T('Error') : 'OK', v.err ? 'lvl-bad' : v.t ? 'lvl-ok' : ''),
    fact(T('Last poll'), v.t ? clock(v.t) : ''), ...Object.entries(byType).map(([k, [n, on]]) => fact(DEV_TYPES[k] || k, `${on} / ${n} online`, on < n ? 'lvl-bad' : ''))),
    v.err ? h('div', { class: 'notice bad', style: 'margin-top:10px' }, TE(v.err)) : '',
    h('div', { class: 'row', style: 'margin:12px 0 6px' }, filter), tbl);
  draw();
}

function addIntegTargets() {
  const st = integViewState;
  if (!st || !st.v) return;
  const devs = (st.v.devices || []).filter(d => st.sel.has(d.id) && d.ip);
  const ags = managedAgents(st.v.customerId);
  if (!ags.length) { toast({ kind: 'info', title: T('No agent'), text: T('Create an agent first – the agent is what measures the targets.') }); return; }
  const agent = select(ags.map(a => [a.id, `${a.name} (${(siteOf(a.siteId) || {}).name || ''})`]), ags[0].id);
  const group = h('input', { type: 'text', value: st.v.name });
  formDialog({ title: T('Add as ping targets'), saveLabel: T('Add'), fields: [h('p', { class: 'field full muted' }, devs.map(d => `${d.name} (${d.ip})`).join(', ')),
    field(T('Measuring agent'), agent, null, true), field(T('Group'), group, null, true)],
    onSave: async () => {
      const names = {}; for (const d of devs) names[d.ip] = d.name;
      await POST('/api/targets', { agentId: agent.value, hosts: devs.map(d => d.ip).join('\n'), kind: 'icmp', group: group.value.trim(), names });
      await afterChange(); toast({ kind: 'clear', title: T('Targets added'), text: T('{0} targets.', devs.length) });
    } });
}

function integrationDialog(v, customerId) {
  const d = v ? JSON.parse(JSON.stringify(v)) : { id: '', kind: 'unifi', name: '', intervalSec: 120, customerId };
  const kind = select(VENDORS, d.kind);
  const inp = (val, ph, type) => h('input', { type: type || 'text', value: val || '', placeholder: ph || '', autocomplete: type === 'password' ? 'new-password' : 'off' });
  const name = inp(d.name, T('e.g. UniFi at the office'));
  const url = inp(d.url), site = inp(d.site), key = inp(d.apiKey, '', 'password'), user = inp(d.username), pass = inp(d.password, '', 'password'), org = inp(d.orgId, T('Optional – first organisation'));
  const cid = inp(d.clientId), csec = inp(d.clientSecret, '', 'password'), atok = inp(d.accessToken, '', 'password'), rtok = inp(d.refreshToken, '', 'password');
  const insecure = h('input', { type: 'checkbox', checked: !!d.insecure });
  const interval = select([[60, '1 min'], [120, '2 min'], [300, '5 min'], [900, '15 min']], d.intervalSec || 120);
  const F = {
    url: field(T('Address'), url), site: field(T('Site'), site), key: field(T('API key'), key), user: field(T('Username'), user), pass: field(T('Password'), pass),
    org: field(T('Organisation ID'), org), cid: field('Client ID', cid), csec: field('Client secret', csec), atok: field(T('Access token'), atok), rtok: field(T('Refresh token'), rtok),
    insecure: h('div', { class: 'field full' }, h('label', { class: 'check' }, insecure, T('Accept a self-signed certificate (local UniFi controller)'))),
  };
  const help = h('small', { class: 'field full muted' });
  const sync = () => {
    const k = kind.value;
    const show = { unifi: ['url', 'site', 'key', 'user', 'pass', 'insecure'], meraki: ['key', 'org', 'site'], aruba: ['url', 'cid', 'csec', 'atok', 'rtok', 'site'] }[k];
    for (const [n, f] of Object.entries(F)) f.style.display = show.includes(n) ? '' : 'none';
    url.placeholder = k === 'aruba' ? 'https://apigw-eucentral3.central.arubanetworks.com' : 'https://192.168.1.1  (UniFi OS)  /  https://unifi.local:8443';
    site.placeholder = { unifi: T('Empty = default'), meraki: T('Network name – empty = all networks'), aruba: T('Empty = all sites') }[k];
    key.placeholder = k === 'unifi' ? T('UniFi Network → Settings → Control Plane → Integrations') : T('Organization → API & Webhooks');
    help.textContent = { unifi: T('Use an API key (UniFi Network 9 or later) or a local admin account. The server must reach the controller – for a controller at the customer, publish it or use UniFi\'s remote access address.'),
      meraki: T('Create the key under Organization → Settings → Dashboard API access. Read-only access is enough.'),
      aruba: T('Create an API client under Organization → Platform Integration → REST API, and generate a token for it. PingVakt renews the token automatically.') }[k];
  };
  kind.onchange = sync; sync();
  const collect = () => Object.assign(d, { kind: kind.value, name: name.value.trim(), url: url.value.trim(), site: site.value.trim(), apiKey: key.value.trim(), username: user.value.trim(),
    password: pass.value, orgId: org.value.trim(), clientId: cid.value.trim(), clientSecret: csec.value, accessToken: atok.value.trim(), refreshToken: rtok.value.trim(),
    insecure: insecure.checked, intervalSec: +interval.value, customerId: d.customerId || customerId });
  const result = h('div', { class: 'field full' });
  const err = h('span', { class: 'form-error' });
  const testBtn = h('button', { class: 'btn', onclick: async () => {
    err.textContent = ''; testBtn.disabled = true; result.replaceChildren(h('small', { class: 'muted' }, T('Connecting…')));
    try {
      const r = await POST('/api/integrations/test', collect());
      if (r.tokens) { atok.value = r.accessToken; rtok.value = r.refreshToken; }
      const on = r.devices.filter(x => x.online).length;
      result.replaceChildren(h('div', { class: 'notice ok' }, T('Connected – {0} devices, {1} online.', r.devices.length, on), r.tokens ? ' ' + T('New tokens were issued – save now.') : ''));
    } catch (e) { result.replaceChildren(); err.textContent = TE(e.message); } finally { testBtn.disabled = false; }
  } }, T('Test'));
  const close = modal({ title: v ? T('Edit integration') : T('Add integration'), wide: true,
    body: h('div', { class: 'form' }, field(T('System'), kind), field(T('Name'), name), help, F.url, F.insecure, F.key, F.user, F.pass, F.org, F.cid, F.csec, F.atok, F.rtok, F.site, field(T('Poll every'), interval), result),
    foot: [h('div', { class: 'left' }, err, testBtn, v ? h('button', { class: 'btn danger', onclick: async () => {
      if (!await confirmDialog(T('Delete integration'), T('Delete "{0}"?', v.name), T('Delete'))) return;
      await DEL('/api/integrations/' + v.id); close(); document.querySelectorAll('.backdrop').forEach(b => b._close()); refreshIntegRows();
    } }, T('Delete')) : null),
      h('button', { class: 'btn', onclick: () => close() }, T('Cancel')),
      h('button', { class: 'btn primary', onclick: async () => {
        err.textContent = '';
        try { collect(); await (v ? PUT('/api/integrations/' + v.id, d) : POST('/api/integrations', d)); close(); refreshIntegRows(); if (integViewState) setTimeout(refreshIntegView, 1500); }
        catch (e) { err.textContent = TE(e.message); }
      } }, T('Save'))] });
}

function onIntegrationEvent(m) {
  refreshIntegRows();
  if (integViewState && integViewState.id === m.id) refreshIntegView();
}
