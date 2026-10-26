/* PingVakt – nätverksskanning (samma fil i server/web och desktop/frontend/dist).
 * Använder h, modal, select, toast, fmtMs och T från värdappen. */
'use strict';

const SCAN_PORTS = { 22: 'SSH', 23: 'Telnet', 53: 'DNS', 80: 'HTTP', 139: 'NetBIOS', 443: 'HTTPS', 445: 'SMB', 515: 'LPD', 548: 'AFP', 554: 'RTSP',
  631: 'IPP', 3389: 'RDP', 5000: 'UPnP/NAS', 8080: 'HTTP-alt', 8291: 'Winbox', 8443: 'HTTPS-alt', 9100: 'Print', 62078: 'iOS' };
function scanKindText(d) {
  if (d.self) return T('This computer');
  return ({ router: T('Router/firewall'), printer: T('Printer'), windows: T('Windows computer'), nas: 'NAS', camera: T('Camera'), ap: T('Access point'),
    web: T('Web interface'), ssh: T('Linux/SSH'), apple: 'Apple', phone: T('Phone/tablet') })[d.kind] || '';
}

// scanDialog öppnar dialogen. opt: { title, sub, networks: [], start(cidr), stop(), add(devices), known: Set, live }
// Returnerar { progress(p), done(devices, err) } som värdappen anropar.
function scanDialog(opt) {
  const nets = opt.networks || [];
  const netSel = select([...nets.map(n => [n.cidr, `${n.cidr} – ${n.iface} (${n.hosts} ${T('addresses')})`]), ['', T('Other network…')]], nets.length ? nets[0].cidr : '');
  const custom = h('input', { type: 'text', placeholder: '192.168.1.0/24, 10.0.0.1-10.0.0.50', spellcheck: 'false', value: nets.length ? nets[0].cidr : '' });
  const syncSel = () => { if (netSel.value) custom.value = netSel.value; custom.style.display = netSel.value ? 'none' : ''; if (!netSel.value) custom.focus(); };
  netSel.onchange = syncSel;
  const btn = h('button', { class: 'btn primary' }, T('Scan'));
  const status = h('div', { class: 'scan-status muted' }, nets.length ? T('Choose a network and click Scan.') : T('Enter a network, e.g. 192.168.1.0/24.'));
  const bar = h('div', { class: 'scan-bar hidden' }, h('span'));
  const filter = h('input', { type: 'search', placeholder: T('Filter…'), class: 'scan-filter' });
  const tbody = h('tbody');
  const allBox = h('input', { type: 'checkbox' });
  const addBtn = h('button', { class: 'btn primary', disabled: true }, T('Add selected as targets'));
  const copyBtn = h('button', { class: 'btn', disabled: true }, T('Copy as CSV'));
  let devices = [], running = false, started = 0, timer = null;
  const sel = new Set();
  const known = opt.known || new Set();

  const updateButtons = () => {
    addBtn.disabled = sel.size === 0;
    addBtn.textContent = sel.size ? T('Add {0} as targets', sel.size) : T('Add selected as targets');
    copyBtn.disabled = devices.length === 0;
  };
  const render = () => {
    const q = filter.value.trim().toLowerCase();
    const list = devices.filter(d => !q || [d.ip, d.name, d.mac, d.vendor, scanKindText(d), (d.ports || []).map(p => SCAN_PORTS[p] || p).join(' ')].join(' ').toLowerCase().includes(q));
    tbody.replaceChildren(...list.map(d => {
      const mon = known.has(d.ip);
      const cb = h('input', { type: 'checkbox', checked: sel.has(d.ip), disabled: mon, onchange: e => { e.target.checked ? sel.add(d.ip) : sel.delete(d.ip); updateButtons(); } });
      return h('tr', { class: (mon ? 'monitored ' : '') + (d.self ? 'self' : '') },
        h('td', null, cb), h('td', { class: 'mono' }, d.ip),
        h('td', null, d.name || h('span', { class: 'muted' }, '–')),
        h('td', null, scanKindText(d) || h('span', { class: 'muted' }, '–')),
        h('td', null, d.vendor ? (d.vendor === 'Private address' ? h('span', { class: 'muted', title: T('The device uses a random (private) MAC address – common for phones, tablets and virtual machines.') }, T('Private address')) : d.vendor) : h('span', { class: 'muted' }, '–')),
        h('td', { class: 'mono' }, d.mac || '–'),
        h('td', null, (d.ports || []).length ? h('div', { class: 'scan-ports' }, ...d.ports.map(p => h('span', { class: 'tag', title: 'TCP ' + p }, SCAN_PORTS[p] || String(p)))) : h('span', { class: 'muted' }, '–')),
        h('td', { class: 'mono' }, d.rtt >= 0 ? fmtMs(d.rtt) + ' ms' : '–'),
        h('td', null, mon ? h('span', { class: 'tag' }, T('Monitored')) : null));
    }));
    updateButtons();
  };
  filter.oninput = render;
  allBox.onchange = () => { for (const d of devices) if (!known.has(d.ip)) allBox.checked ? sel.add(d.ip) : sel.delete(d.ip); render(); };

  const finish = () => { running = false; btn.textContent = T('Scan'); bar.classList.add('hidden'); clearInterval(timer); };
  const ctl = {
    progress(p) {
      if (!running) return;
      bar.classList.remove('hidden');
      bar.firstChild.style.width = (p.total ? p.done * 100 / p.total : 0) + '%';
      status.textContent = T('Scanning… {0} of {1} addresses · {2} found', p.done, p.total, p.found);
    },
    done(list, err) {
      if (!running && !list) return;
      finish();
      devices = list || [];
      sel.clear();
      for (const d of devices) if (!known.has(d.ip) && !d.self) sel.add(d.ip);
      allBox.checked = true;
      status.className = 'scan-status ' + (err ? 'form-error' : 'muted');
      status.textContent = err ? err : T('{0} active devices found in {1} s.', devices.length, Math.round((Date.now() - started) / 1000));
      render();
    },
    show(run) { // tidigare resultat (servern)
      devices = run.devices || []; render();
      status.textContent = T('Last scan {0}: {1} devices.', new Date(run.t).toLocaleString('sv-SE'), devices.length);
      custom.value = run.cidr;
    },
  };
  btn.onclick = async () => {
    if (running) { finish(); try { await opt.stop(); } catch (e) { /* */ } status.textContent = T('Cancelled'); return; }
    const cidr = custom.value.trim();
    if (!cidr) return custom.focus();
    running = true; started = Date.now(); btn.textContent = T('Stop');
    status.className = 'scan-status muted';
    status.textContent = opt.live ? T('Scanning…') : T('Waiting for the agent…');
    if (!opt.live) { clearInterval(timer); timer = setInterval(() => { if (running) status.textContent = T('The agent is scanning {0}… {1} s', cidr, Math.round((Date.now() - started) / 1000)); }, 1000); }
    try { await opt.start(cidr); } catch (e) { finish(); status.className = 'scan-status form-error'; status.textContent = e.message || String(e); }
  };
  custom.addEventListener('keydown', e => { if (e.key === 'Enter') btn.onclick(); });
  addBtn.onclick = async () => {
    const list = devices.filter(d => sel.has(d.ip));
    addBtn.disabled = true;
    try {
      await opt.add(list);
      for (const d of list) known.add(d.ip);
      sel.clear(); render();
      toast({ kind: 'info', title: T('Targets added'), text: T('{0} targets added', list.length) });
    } catch (e) { toast({ kind: 'error', title: T('Could not add'), text: e.message || String(e) }); updateButtons(); }
  };
  copyBtn.onclick = async () => {
    const rows = [['IP', T('Name'), T('Type'), T('Manufacturer'), 'MAC', T('Ports'), 'ms'], ...devices.map(d => [d.ip, d.name || '', scanKindText(d), d.vendor || '', d.mac || '', (d.ports || []).join(' '), d.rtt >= 0 ? d.rtt : ''])];
    const txt = rows.map(r => r.map(x => /[",;\n]/.test(String(x)) ? '"' + String(x).replace(/"/g, '""') + '"' : x).join(';')).join('\n');
    try { await navigator.clipboard.writeText(txt); copyBtn.textContent = T('Copied'); setTimeout(() => { copyBtn.textContent = T('Copy as CSV'); }, 1500); } catch (e) { /* */ }
  };
  if (!opt.add) addBtn.style.display = 'none';
  syncSel();
  if (!nets.length) { netSel.style.display = 'none'; custom.style.display = ''; }
  const body = h('div', { class: 'scan' },
    opt.sub ? h('p', { class: 'muted', style: 'margin:0 0 10px' }, opt.sub) : null,
    h('div', { class: 'row scan-head' }, netSel, custom, btn),
    status, bar,
    h('div', { class: 'row', style: 'margin-top:10px' }, filter),
    h('div', { class: 'scan-table' }, h('table', { class: 'tbl compact' },
      h('thead', null, h('tr', null, h('th', null, allBox), ...['IP', T('Name'), T('Type'), T('Manufacturer'), 'MAC', T('Open ports'), T('Ping'), ''].map(x => h('th', null, x)))), tbody)),
    h('small', { class: 'muted' }, T('Finds devices that answer ping or one of {0} common TCP ports. Devices with a firewall that blocks everything are not visible.', Object.keys(SCAN_PORTS).length)));
  modal({ title: opt.title || T('Scan network'), wide: true, body,
    foot: [h('div', { class: 'left' }, copyBtn), addBtn],
    onClose: () => { if (running) { try { opt.stop(); } catch (e) { /* */ } } clearInterval(timer); if (opt.onClose) opt.onClose(); } });
  return ctl;
}
