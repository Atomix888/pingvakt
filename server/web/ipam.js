/* ---------------- IPAM: IP-adresser per kund ---------------- */
'use strict';

const IPAM = { data: null, sub: null, filter: 'used', q: '', block: 0, searchQ: '' };

const ip2n = ip => ip.split('.').reduce((a, b) => a * 256 + +b, 0);
const n2ip = n => [n >>> 24 & 255, n >>> 16 & 255, n >>> 8 & 255, n & 255].join('.');
const DAY = 86400000;

function utilBar(pct, label, warn) {
  const st = pct >= (warn || 90) ? 'down' : pct >= 75 ? 'warn' : 'up';
  return h('div', { class: 'ip-util st-' + st, title: label || '' },
    h('div', { class: 'ip-util-bar' }, h('i', { style: `width:${Math.min(100, pct)}%` })), h('span', null, label || Math.round(pct) + ' %'));
}

function renderIPAM(page, parts) {
  if (parts[1] === 's' && parts[2]) return renderSubnet(page, parts[2]);
  return renderIPAMList(page, parts[1] === 'c' ? parts[2] : '');
}

/* ---------- lista ---------- */
async function renderIPAMList(page, custId) {
  const c = custId && byId(S.customers, custId);
  const search = h('input', { type: 'search', class: 'ov-search', placeholder: T('Search IP, MAC or name…'), value: IPAM.searchQ,
    oninput: e => { IPAM.searchQ = e.target.value; clearTimeout(IPAM.st); IPAM.st = setTimeout(() => ipamSearch(custId), 250); } });
  const custSel = select([['', T('All customers')], ...S.customers.map(x => [x.id, x.name])], custId);
  custSel.onchange = () => { location.hash = custSel.value ? '#/ipam/c/' + custSel.value : '#/ipam'; };
  const head = h('div', { class: 'page-head' }, h('h1', null, T('IP addresses')), S.customers.length > 1 ? custSel : null,
    h('div', { class: 'tools' }, search, isAdmin() && S.customers.length ? h('button', { class: 'btn primary', onclick: () => subnetDialog(null, custId || (S.customers[0] || {}).id) }, T('+ New subnet')) : null));
  const body = h('div', { id: 'ipamBody' }, h('p', { class: 'muted' }, T('Loading…')));
  page.replaceChildren(head, body);
  let res;
  try { res = await GET('/api/ipam' + (custId ? '?customer=' + custId : '')); } catch (e) { body.replaceChildren(h('p', { class: 'form-error' }, e.message)); return; }
  IPAM.data = res;
  if (IPAM.searchQ.length >= 2) return ipamSearch(custId);
  drawIPAMList(body, res, custId, c);
}

function drawIPAMList(body, res, custId) {
  const out = [];
  if (custId && isAdmin() && res.suggestions.length) {
    out.push(h('div', { class: 'notice ip-sugg' }, h('b', null, T('Networks your agents are on')), h('span', { class: 'muted' }, ' – ' + T('add them to start tracking addresses automatically.')),
      h('div', { class: 'ip-sugg-list' }, ...res.suggestions.map(sg => h('div', { class: 'ip-sugg-row' },
        h('span', { class: 'mono' }, sg.cidr), h('span', { class: 'muted' }, T('via {0}', sg.agentName) + (sg.gateway ? ' · gw ' + sg.gateway : '')),
        h('button', { class: 'btn sm primary', onclick: () => subnetDialog(null, custId, { cidr: sg.cidr, gateway: sg.gateway, scanAgent: sg.agentId, siteId: sg.siteId, scanHours: 6 }) }, T('Add')))))));
  }
  if (!res.subnets.length) {
    out.push(h('div', { class: 'empty', style: 'display:block' }, h('h2', null, T('No subnets yet')),
      h('p', null, T('Add the customer\'s networks. PingVakt then fills in the addresses from the agents\' network scans, the routers\' ARP tables (SNMP) and the agents themselves, and you can add reservations and notes.')),
      isAdmin() && S.customers.length ? h('div', { class: 'quick' }, h('button', { class: 'btn primary', onclick: () => subnetDialog(null, custId || S.customers[0].id) }, T('+ New subnet'))) : null));
    body.replaceChildren(...out);
    return;
  }
  const byCust = new Map();
  for (const sn of res.subnets) { if (!byCust.has(sn.customerId)) byCust.set(sn.customerId, []); byCust.get(sn.customerId).push(sn); }
  for (const [cid, list] of byCust) {
    const cust = byId(S.customers, cid);
    out.push(h('div', { class: 'section' }, custId ? null : h('div', { class: 'section-head' }, h('h2', null, h('a', { href: '#/ipam/c/' + cid }, cust ? cust.name : cid))),
      h('div', { class: 'tbl-wrap' }, h('table', { class: 'tbl ip-subnets' },
        h('thead', null, h('tr', null, ...[T('Subnet'), 'VLAN', T('Site'), T('Used'), T('DHCP pool'), T('Online now'), T('Last scan'), ''].map(x => h('th', null, x)))),
        h('tbody', null, ...list.map(sn => {
          const st = sn.stats;
          return h('tr', { class: 'clickable', onclick: () => { location.hash = '#/ipam/s/' + sn.id; } },
            h('td', null, h('b', null, sn.name || sn.cidr), sn.name ? h('div', { class: 'mono muted' }, sn.cidr) : null),
            h('td', { class: 'mono' }, sn.vlan ? String(sn.vlan) : '–'),
            h('td', null, sn.siteName || '–'),
            h('td', null, utilBar(st.util, `${st.used}/${st.size} · ${Math.round(st.util)} %`)),
            h('td', null, st.dhcpSize ? utilBar(st.dhcpUtil, `${st.dhcpUsed}/${st.dhcpSize}`) : h('span', { class: 'muted' }, '–')),
            h('td', { class: 'mono' }, String(st.active)),
            h('td', { class: 'muted' }, sn.lastScan ? dur(Date.now() - sn.lastScan) : (sn.scanHours ? T('soon') : T('manual'))),
            h('td', null, st.conflicts ? h('span', { class: 'pill st-down' }, h('span', { class: 'dot' }), T('{0} conflicts', st.conflicts)) : null));
        }))))));
  }
  body.replaceChildren(...out);
}

async function ipamSearch(custId) {
  const body = $('#ipamBody');
  if (!body) return;
  const q = IPAM.searchQ.trim();
  if (q.length < 2) { if (IPAM.data) drawIPAMList(body, IPAM.data, custId); return; }
  let hits = [];
  try { hits = await GET('/api/ipam/search?q=' + encodeURIComponent(q)); } catch (e) { return; }
  if (custId) hits = hits.filter(x => x.customerId === custId);
  body.replaceChildren(h('div', { class: 'section' }, h('div', { class: 'section-head' }, h('h2', null, T('{0} matches', hits.length))),
    hits.length ? h('div', { class: 'tbl-wrap' }, h('table', { class: 'tbl' },
      h('thead', null, h('tr', null, ...[T('Address'), T('Name'), 'MAC', T('Subnet'), T('Customer'), T('Last seen')].map(x => h('th', null, x)))),
      h('tbody', null, ...hits.map(a => h('tr', { class: 'clickable', onclick: () => { IPAM.focus = a.ip; location.hash = '#/ipam/s/' + a.subnetId; } },
        h('td', { class: 'mono' }, a.ip), h('td', null, a.host || '–', a.desc ? h('div', { class: 'muted' }, a.desc) : null),
        h('td', { class: 'mono' }, a.mac || '–', a.vendor ? h('div', { class: 'muted' }, a.vendor) : null),
        h('td', null, a.subnetName || a.cidr), h('td', null, (byId(S.customers, a.customerId) || {}).name || ''),
        h('td', { class: 'muted' }, a.last ? dur(Date.now() - a.last) : T('never'))))))) : h('p', { class: 'muted' }, T('Nothing found.'))));
}

/* ---------- subnät ---------- */
function addrState(a, sn, now) {
  if (!a) return 'free';
  if (a.conflict && now - a.conflict < DAY) return 'conflict';
  const seen = a.last && now - a.last < 30 * DAY;
  if (a.last && now - a.last < DAY) return a.reserved ? 'reserved-on' : 'active';
  if (seen) return 'seen';
  if (a.reserved) return 'reserved';
  if (a.host || a.desc) return 'noted';
  return a.last ? 'stale' : 'free';
}
const STATE_TEXT = () => ({ active: T('Online'), 'reserved-on': T('Reserved, online'), seen: T('Seen within 30 days'), reserved: T('Reserved'), noted: T('Documented'), stale: T('Not seen for 30+ days'), free: T('Free'), conflict: T('Possible conflict'), gateway: 'Gateway' });

async function renderSubnet(page, id) {
  let res;
  try { res = await GET('/api/ipam/subnets/' + id); } catch (e) { page.replaceChildren(h('p', { class: 'form-error' }, e.message)); return; }
  if (!S.view || S.view.name !== 'ipam/s/' + id) return;
  IPAM.sub = res;
  const sn = res.subnet, st = sn.stats, cust = byId(S.customers, sn.customerId);
  const now = res.now;
  const byIP = new Map(res.addrs.map(a => [a.ip, a]));
  const [base, bits] = sn.cidr.split('/');
  const net0 = ip2n(base), size = 2 ** (32 - +bits);
  const first = +bits >= 31 ? net0 : net0 + 1, last = +bits >= 31 ? net0 + size - 1 : net0 + size - 2;
  const dF = sn.dhcpFrom ? ip2n(sn.dhcpFrom) : -1, dT = sn.dhcpTo ? ip2n(sn.dhcpTo) : -1;
  const inPool = n => dF >= 0 && n >= Math.min(dF, dT) && n <= Math.max(dF, dT);
  const refs = res.refs || {};

  // huvud
  const scanBtn = h('button', { class: 'btn', onclick: async () => {
    if (!sn.scanAgent) return subnetDialog(sn, sn.customerId, null, T('Choose which agent should scan the network.'));
    scanBtn.disabled = true;
    try { await POST(`/api/ipam/subnets/${sn.id}/scan`); toast({ kind: 'info', title: T('Scanning {0}…', sn.cidr), text: T('The list updates when the agent is done (up to a few minutes).') }); }
    catch (e) { toast({ kind: 'fire', title: T('Could not scan'), text: e.message }); } finally { scanBtn.disabled = false; }
  } }, res.scanning ? T('Scanning…') : T('Scan now'));
  const head = h('div', { class: 'page-head' },
    h('div', null, h('div', { class: 'crumbs' }, h('a', { href: '#/ipam/c/' + sn.customerId }, T('IP addresses')), ' › ', cust ? cust.name : ''),
      h('h1', null, sn.name || sn.cidr, sn.name ? h('span', { class: 'mono muted', style: 'font-size:15px;margin-left:10px' }, sn.cidr) : null)),
    h('div', { class: 'tools' },
      h('button', { class: 'btn primary', onclick: async () => { try { const r = await GET(`/api/ipam/subnets/${sn.id}/next`); addrDialog(sn, r.ip, null, refs[r.ip]); } catch (e) { toast({ kind: 'fire', title: e.message }); } } }, T('Next free IP')),
      isAdmin() ? scanBtn : null,
      isAdmin() ? h('button', { class: 'btn', onclick: () => subnetDialog(sn, sn.customerId) }, T('Edit')) : null,
      h('button', { class: 'btn', onclick: e => showMenu(e.currentTarget, [
        { label: T('Export CSV'), action: () => ipamExport(sn, res.addrs) },
        isAdmin() ? { label: T('Import CSV…'), action: () => ipamImport(sn) } : null,
        isAdmin() ? '-' : null,
        isAdmin() ? { label: T('Delete subnet…'), danger: true, action: async () => {
          if (!await confirmDialog(T('Delete subnet'), T('Delete {0} and everything noted about its addresses?', sn.cidr), T('Delete'))) return;
          await DEL('/api/ipam/subnets/' + sn.id); location.hash = '#/ipam/c/' + sn.customerId;
        } } : null].filter(Boolean)) }, T('More ▾'))));
  const info = h('div', { class: 'ip-info muted' }, [sn.vlan ? 'VLAN ' + sn.vlan : '', sn.gateway ? 'Gateway ' + sn.gateway : '', sn.dhcpFrom ? `DHCP ${sn.dhcpFrom} – ${sn.dhcpTo}` : T('No DHCP pool set'),
    sn.siteName ? T('Site: {0}', sn.siteName) : '', sn.agentName ? (sn.scanHours ? T('scanned by {0} every {1} h', sn.agentName, sn.scanHours) : T('scanned by {0} on demand', sn.agentName)) : T('no automatic scanning'),
    sn.lastScan ? T('last scan {0} ago', dur(Date.now() - sn.lastScan)) : ''].filter(Boolean).join(' · '), sn.notes ? h('div', null, sn.notes) : null);
  const kpi = (label, val, sub, stc) => h('div', { class: 'kpi st-' + stc }, h('small', null, label), h('div', { class: 'kpi-val' }, h('b', null, val)), h('div', { class: 'kpi-sub' }, sub || ' '));
  const kpis = h('div', { class: 'kpis' },
    kpi(T('Used'), `${Math.round(st.util)} %`, T('{0} of {1} addresses · {2} free', st.used, st.size, st.free), st.util >= 90 ? 'down' : st.util >= 75 ? 'warn' : 'up'),
    kpi(T('Online now'), String(st.active), T('seen the last 24 hours'), 'up'),
    kpi(T('DHCP pool'), st.dhcpSize ? `${Math.round(st.dhcpUtil)} %` : '–', st.dhcpSize ? T('{0} of {1} handed out (7 days)', st.dhcpUsed, st.dhcpSize) : T('set the pool under Edit'), st.dhcpUtil >= 90 ? 'down' : st.dhcpUtil >= 75 ? 'warn' : 'up'),
    kpi(T('Conflicts'), String(st.conflicts), st.conflicts ? T('two devices on the same address') : T('none found'), st.conflicts ? 'down' : 'up'));

  // karta: en ruta per adress, 256 åt gången
  const blocks = Math.ceil(size / 256);
  if (IPAM.block >= blocks || IPAM.blockSub !== sn.id) { IPAM.block = 0; IPAM.blockSub = sn.id; }
  if (IPAM.focus && byIP.has(IPAM.focus)) IPAM.block = Math.floor((ip2n(IPAM.focus) - net0) / 256);
  const blockSel = blocks > 1 ? select([...Array(blocks).keys()].map(i => [i, `${n2ip(net0 + i * 256)} – ${n2ip(Math.min(net0 + size - 1, net0 + i * 256 + 255))}`]), IPAM.block) : null;
  if (blockSel) blockSel.onchange = () => { IPAM.block = +blockSel.value; renderSubnet(page, id); };
  const cells = [];
  const b0 = net0 + IPAM.block * 256;
  const stText = STATE_TEXT();
  for (let n = b0; n < Math.min(net0 + size, b0 + 256); n++) {
    const ip = n2ip(n), a = byIP.get(ip);
    const usable = n >= first && n <= last;
    let s = usable ? addrState(a, sn, now) : 'net';
    if (ip === sn.gateway && s === 'free') s = 'gateway';
    const cls = 'ipc s-' + s + (inPool(n) ? ' pool' : '') + (ip === IPAM.focus ? ' focus' : '');
    const tip = usable ? [ip, stText[s] || '', a && a.host, a && a.mac, a && a.vendor, inPool(n) ? T('DHCP pool') : ''].filter(Boolean).join('\n') : (n === net0 ? T('Network address') : T('Broadcast address'));
    cells.push(h('div', { class: cls, title: tip, onclick: usable ? () => addrDialog(sn, ip, a, refs[ip]) : null }, String(n & 255)));
  }
  const legend = h('div', { class: 'ip-legend' }, ...[['active', T('Online')], ['seen', T('Seen within 30 days')], ['reserved', T('Reserved')], ['noted', T('Documented')], ['stale', T('Not seen for 30+ days')], ['free', T('Free')], ['conflict', T('Conflict')]]
    .map(([k, l]) => h('span', null, h('i', { class: 'ipc s-' + k }), l)), sn.dhcpFrom ? h('span', null, h('i', { class: 'ipc s-free pool' }), T('DHCP pool')) : null);
  const mapSec = h('div', { class: 'section' }, h('div', { class: 'section-head' }, h('h2', null, T('Address map')), blockSel ? h('div', { class: 'tools' }, blockSel) : null),
    h('div', { class: 'ip-map' }, ...cells), legend);

  // tabell
  const filters = [['used', T('In use')], ['active', T('Online')], ['reserved', T('Reserved')], ['conflict', T('Conflicts')], ['stale', T('Not seen 30+ days')], ['all', T('All known')]];
  const q = h('input', { type: 'search', class: 'ov-search', placeholder: T('Filter…'), value: IPAM.q, oninput: e => { IPAM.q = e.target.value; drawRows(); } });
  const fbar = h('div', { class: 'cust-filter' }, q, ...filters.map(([k, l]) => h('button', { class: 'btn sm' + (IPAM.filter === k ? ' on' : ''), onclick: () => { IPAM.filter = k; renderSubnet(page, id); } }, l)));
  const tbody = h('tbody');
  const drawRows = () => {
    const qq = IPAM.q.trim().toLowerCase();
    const rows = res.addrs.filter(a => {
      const s = addrState(a, sn, now);
      const ok = IPAM.filter === 'all' || (IPAM.filter === 'used' && ['active', 'reserved-on', 'seen', 'reserved', 'noted', 'conflict'].includes(s)) ||
        (IPAM.filter === 'active' && (s === 'active' || s === 'reserved-on')) || (IPAM.filter === 'reserved' && a.reserved) ||
        (IPAM.filter === 'conflict' && s === 'conflict') || (IPAM.filter === 'stale' && s === 'stale');
      return ok && (!qq || [a.ip, a.host, a.mac, a.vendor, a.desc, a.owner].some(x => x && x.toLowerCase().includes(qq)));
    });
    tbody.replaceChildren(...(rows.length ? rows.map(a => {
      const s = addrState(a, sn, now), r = refs[a.ip] || [];
      return h('tr', { class: 'clickable' + (a.ip === IPAM.focus ? ' focus' : ''), onclick: () => addrDialog(sn, a.ip, a, r) },
        h('td', { class: 'mono' }, h('span', { class: 'ipc-dot s-' + s }), a.ip, inPool(ip2n(a.ip)) ? h('small', { class: 'muted' }, ' DHCP') : null),
        h('td', null, a.host || (r.length ? h('span', { class: 'muted' }, r[0].name) : h('span', { class: 'muted' }, '–')), a.ip === sn.gateway ? h('span', { class: 'tag', style: 'margin-left:6px' }, 'gateway') : null, r.length ? h('div', { class: 'ip-refs' }, ...r.map(x => h('span', { class: 'tag' }, { target: '◎ ', agent: '⬢ ', snmp: '⇄ ' }[x.kind] + x.name))) : null),
        h('td', { class: 'mono' }, a.mac || '–', a.vendor ? h('div', { class: 'muted' }, a.vendor) : null, a.prevMac && Date.now() - a.macAt < 7 * DAY ? h('div', { class: 'lvl-high', title: T('The MAC address changed') }, '↺ ' + a.prevMac) : null),
        h('td', null, a.desc || '', a.owner ? h('div', { class: 'muted' }, a.owner) : null),
        h('td', null, s === 'conflict' ? h('span', { class: 'pill st-down' }, T('Conflict with {0}', a.confMac || '?')) : a.reserved ? h('span', { class: 'pill' }, T('Reserved')) : null),
        h('td', { class: 'muted', title: a.last ? dateTime(a.last) : '' }, a.last ? dur(Date.now() - a.last) : T('never'), a.src ? h('div', null, { scan: T('scan'), arp: 'ARP', agent: 'agent' }[a.src] || a.src) : null));
    }) : [h('tr', null, h('td', { colspan: 6, class: 'muted' }, T('No addresses match.')))]));
  };
  drawRows();
  const tblSec = h('div', { class: 'section' }, h('div', { class: 'section-head' }, h('h2', null, T('Addresses'))), fbar,
    h('div', { class: 'tbl-wrap' }, h('table', { class: 'tbl ip-addrs' }, h('thead', null, h('tr', null, ...[T('Address'), T('Name'), 'MAC', T('Description'), 'Status', T('Last seen')].map(x => h('th', null, x)))), tbody)));
  const keep = [page.scrollTop, window.scrollY];
  page.replaceChildren(head, info, kpis, mapSec, tblSec);
  page.scrollTop = keep[0]; window.scrollTo(0, keep[1]);
  IPAM.focus = null;
}

function ipamOnScan(run) {
  if (run.pending || !IPAM.sub || !S.view || !S.view.name.startsWith('ipam/s/')) return;
  if (run.cidr === IPAM.sub.subnet.cidr || run.agentId === IPAM.sub.subnet.scanAgent) setTimeout(() => renderSubnet($('#page'), IPAM.sub.subnet.id), 500);
}

/* ---------- dialoger ---------- */
function subnetDialog(sn, custId, preset, note) {
  const v = Object.assign({ cidr: '', name: '', vlan: 0, siteId: '', gateway: '', dhcpFrom: '', dhcpTo: '', scanAgent: '', scanHours: 6, notes: '' }, sn || {}, preset || {});
  const cust = select(S.customers.map(c => [c.id, c.name]), sn ? sn.customerId : custId);
  if (sn) cust.disabled = true;
  const cidr = h('input', { type: 'text', value: v.cidr, placeholder: '192.168.1.0/24', spellcheck: 'false' });
  const name = h('input', { type: 'text', value: v.name, placeholder: T('e.g. Office LAN, Guest Wi-Fi, Cameras') });
  const vlan = num(v.vlan || '', 1, 4094);
  const gw = h('input', { type: 'text', value: v.gateway, placeholder: '192.168.1.1' });
  const df = h('input', { type: 'text', value: v.dhcpFrom, placeholder: '192.168.1.100' });
  const dt = h('input', { type: 'text', value: v.dhcpTo, placeholder: '192.168.1.199' });
  const notes = h('input', { type: 'text', value: v.notes, placeholder: T('Optional') });
  const siteBox = h('span'), agentBox = h('span');
  let site, agent;
  const fill = () => {
    const cid = cust.value;
    site = select([['', '–'], ...S.sites.filter(s => s.customerId === cid).map(s => [s.id, s.name])], v.siteId);
    agent = select([['', T('None – only addresses seen otherwise')], ...S.agents.filter(a => customerOfAgent(a) === cid && a.kind !== 'desktop').map(a => [a.id, a.name])], v.scanAgent);
    siteBox.replaceChildren(site); agentBox.replaceChildren(agent);
  };
  cust.onchange = fill; fill();
  const every = select([[0, T('Only when I click "Scan now"')], [1, T('Every hour')], [6, T('Every 6 hours')], [24, T('Once a day')]], v.scanHours || 0);
  formDialog({ title: sn ? T('Edit subnet') : T('New subnet'), wide: true, fields: [
    note ? h('div', { class: 'notice full' }, note) : null,
    field(T('Customer'), cust), field(T('Site'), siteBox),
    field(T('Network (CIDR)'), cidr, T('IPv4, from /16 to /32.')), field(T('Name'), name),
    field('VLAN', vlan), field('Gateway', gw),
    field(T('DHCP pool from'), df), field(T('DHCP pool to'), dt),
    field(T('Scan with agent'), agentBox, T('The agent pings and checks common ports on every address and reads MAC addresses and names. Routers and switches added as SNMP devices also contribute their ARP tables.')),
    field(T('Scan automatically'), every), field(T('Note'), notes, null, true)].filter(Boolean),
    onSave: async () => {
      const body = { customerId: cust.value, siteId: site.value, cidr: cidr.value, name: name.value, vlan: +vlan.value || 0, gateway: gw.value, dhcpFrom: df.value, dhcpTo: dt.value,
        scanAgent: agent.value, scanHours: agent.value ? +every.value : 0, notes: notes.value };
      const r = sn ? await PUT('/api/ipam/subnets/' + sn.id, body) : await POST('/api/ipam/subnets', body);
      location.hash = '#/ipam/s/' + r.id;
      if (sn) renderSubnet($('#page'), r.id);
    } });
}

function addrDialog(sn, ip, a, refs) {
  a = a || { ip };
  const now = Date.now();
  const host = h('input', { type: 'text', value: a.hostSet || !a.last ? a.host || '' : '', placeholder: a.host || T('e.g. printer-floor2') });
  const desc = h('input', { type: 'text', value: a.desc || '', placeholder: T('What is it, where is it?') });
  const owner = h('input', { type: 'text', value: a.owner || '', placeholder: T('Optional, e.g. IT, Reception') });
  const resMac = h('input', { type: 'text', value: a.resMac || '', placeholder: 'aa:bb:cc:dd:ee:ff', spellcheck: 'false' });
  let reserved = !!a.reserved, resolve = false;
  const resRow = field(T('Reserved for MAC (optional)'), resMac, T('A warning is raised if another device uses the address.'));
  resRow.style.display = reserved ? '' : 'none';
  const fact = (k, v) => v ? h('div', null, h('small', null, k), h('b', null, v)) : null;
  const conflict = a.conflict && now - a.conflict < DAY;
  const facts = h('div', { class: 'facts full' },
    fact('MAC', a.mac), fact(T('Manufacturer'), a.vendor), fact(T('Type'), a.kind),
    fact(T('First seen'), a.first ? dateTime(a.first) : ''), fact(T('Last seen'), a.last ? dateTime(a.last) : T('never')),
    fact(T('Source'), { scan: T('network scan'), arp: T('ARP table (SNMP)'), agent: T('the agent itself') }[a.src]),
    a.prevMac ? fact(T('Previous MAC'), `${a.prevMac} (${dateTime(a.macAt)})`) : null);
  const canEdit = isAdmin();
  const fields = [facts,
    conflict ? h('div', { class: 'notice bad full' }, T('Possible IP conflict: {0} and {1} both used this address ({2}).', a.confMac || '?', a.mac || '?', dateTime(a.conflict)),
      canEdit ? h('div', null, check(T('Mark as resolved'), false, e => { resolve = e.target.checked; })) : null) : null,
    (refs || []).length ? h('div', { class: 'full ip-refs' }, h('small', { class: 'muted' }, T('Used in PingVakt: ')), ...refs.map(x => h('span', { class: 'tag' }, x.name))) : null,
    field(T('Name'), host, a.host && !a.hostSet ? T('Found: {0}. Type a name to replace it.', a.host) : null), field(T('Description'), desc), field(T('Responsible'), owner),
    h('div', { class: 'field' }, h('span', null, T('Reservation')), check(T('Reserved (static address)'), reserved, e => { reserved = e.target.checked; resRow.style.display = reserved ? '' : 'none'; })),
    resRow];
  if (!canEdit) fields.splice(fields.indexOf(host.parentNode), 99);
  const close = formDialog({ title: ip + (sn.name ? ' – ' + sn.name : ''), wide: true, saveLabel: T('Save'), fields,
    onSave: async () => {
      if (!canEdit) return;
      await PUT(`/api/ipam/subnets/${sn.id}/ip/${ip}`, { host: host.value, desc: desc.value, owner: owner.value, reserved, resMac: reserved ? resMac.value : '', resolve });
      renderSubnet($('#page'), sn.id);
    } });
  // extra knappar i foten
  const foot = document.querySelector('.backdrop:last-child .modal-foot .left');
  if (foot && canEdit) {
    foot.prepend(h('button', { class: 'btn sm', onclick: () => { close(); copyText(ip); addTargetsDialog(sn.customerId, ip); } }, T('Monitor with ping')),
      a.last || a.host || a.reserved ? h('button', { class: 'btn sm ghost danger', onclick: async () => { await DEL(`/api/ipam/subnets/${sn.id}/ip/${ip}`); close(); renderSubnet($('#page'), sn.id); } }, T('Forget')) : '');
  }
}

function copyText(t) { try { navigator.clipboard.writeText(t); } catch (e) { /* inte tillåtet */ } }

function ipamExport(sn, addrs) {
  const sv = LANG() === 'sv', sep = sv ? ';' : ',';
  const q = v => { const s = String(v == null ? '' : v); return /[";,\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s; };
  const head = ['IP', T('Name'), 'MAC', T('Manufacturer'), T('Description'), T('Responsible'), T('Reserved'), T('Reserved for MAC'), T('First seen'), T('Last seen'), T('Source')];
  const rows = addrs.map(a => [a.ip, a.host, a.mac, a.vendor, a.desc, a.owner, a.reserved ? 'x' : '', a.resMac, a.first ? dateTime(a.first) : '', a.last ? dateTime(a.last) : '', a.src]);
  const csv = [head, ...rows].map(r => r.map(q).join(sep)).join('\r\n');
  const a = h('a', { href: URL.createObjectURL(new Blob(['﻿' + csv], { type: 'text/csv' })), download: `ipam-${sn.cidr.replace(/[./]/g, '-')}.csv` });
  document.body.append(a); a.click(); a.remove();
}

function parseCSV(text) {
  text = text.replace(/^﻿/, '');
  const first = text.split(/\r?\n/)[0] || '';
  const sep = (first.match(/;/g) || []).length > (first.match(/,/g) || []).length ? ';' : (first.includes('\t') ? '\t' : ',');
  const rows = []; let row = [], cur = '', inQ = false;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (inQ) { if (ch === '"' && text[i + 1] === '"') { cur += '"'; i++; } else if (ch === '"') inQ = false; else cur += ch; }
    else if (ch === '"') inQ = true;
    else if (ch === sep) { row.push(cur); cur = ''; }
    else if (ch === '\n' || ch === '\r') { if (ch === '\r' && text[i + 1] === '\n') i++; row.push(cur); rows.push(row); row = []; cur = ''; }
    else cur += ch;
  }
  if (cur || row.length) { row.push(cur); rows.push(row); }
  return rows.filter(r => r.some(x => x.trim()));
}

function ipamImport(sn) {
  const file = h('input', { type: 'file', accept: '.csv,.txt,text/csv' });
  const info = h('div', { class: 'muted' });
  let parsed = [];
  file.onchange = async () => {
    const rows = parseCSV(await file.files[0].text());
    if (!rows.length) return;
    const hdr = rows[0].map(x => x.trim().toLowerCase());
    const col = (...names) => hdr.findIndex(x => names.some(n => x === n || x.startsWith(n)));
    let ci = { ip: col('ip', 'address', 'adress'), host: col('name', 'namn', 'host', 'värdnamn', 'hostname'), mac: col('mac'), desc: col('description', 'beskrivning', 'desc', 'comment', 'kommentar'),
      owner: col('responsible', 'ansvarig', 'owner'), res: col('reserved', 'reserverad', 'static', 'statisk') };
    let data = rows.slice(1);
    if (ci.ip < 0) { ci = { ip: 0, host: 1, mac: 2, desc: 3, owner: -1, res: -1 }; data = rows; } // ingen rubrikrad
    const g = (r, i) => i >= 0 ? (r[i] || '').trim() : '';
    parsed = data.map(r => ({ ip: g(r, ci.ip), host: g(r, ci.host), mac: g(r, ci.mac), desc: g(r, ci.desc), owner: g(r, ci.owner), reserved: /^(x|1|yes|ja|true|sant)$/i.test(g(r, ci.res)) })).filter(r => /^\d+\.\d+\.\d+\.\d+$/.test(r.ip));
    info.textContent = T('{0} rows with an IP address found.', parsed.length);
  };
  formDialog({ title: T('Import addresses to {0}', sn.cidr), saveLabel: T('Import'), fields: [field(T('CSV file'), file, T('Columns are recognised by their headings: IP, Name, MAC, Description, Responsible, Reserved. Without headings the order IP, Name, MAC, Description is assumed. Exported files from PingVakt and most IPAM tools work.'), true), info],
    onSave: async () => {
      if (!parsed.length) throw new Error(T('Choose a file with at least one IP address.'));
      const r = await POST(`/api/ipam/subnets/${sn.id}/import`, parsed);
      toast({ kind: 'clear', title: T('{0} addresses imported', r.added), text: r.skipped ? T('{0} rows were outside the subnet and skipped.', r.skipped) : '' });
      renderSubnet($('#page'), sn.id);
    } });
}
