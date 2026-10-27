/* Brygga mot mätmotorn.
 * I skrivbordsappen (Wails) anropas Go-metoderna via window.go.main.App.
 * Öppnas sidan i en vanlig webbläsare körs ett demoläge med simulerad data,
 * vilket gör gränssnittet lätt att utveckla och testa. */
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
// translateDOM översätter den statiska HTML-texten (engelska i källan).
function translateDOM(root) {
  const d = window.PV_DICT || {};
  if (LANG() === 'en') return;
  root.querySelectorAll('[data-i18n]').forEach(el => { const v = d[el.dataset.i18n]; if (v) el.innerHTML = v; });
  const w = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  for (let n; (n = w.nextNode());) { const t = n.nodeValue.trim(); if (t && d[t] != null) n.nodeValue = n.nodeValue.replace(t, d[t]); }
  root.querySelectorAll('[title],[placeholder],[aria-label]').forEach(el => {
    for (const a of ['title', 'placeholder', 'aria-label']) { const v = el.getAttribute(a); if (v && d[v] != null) el.setAttribute(a, d[v]); }
  });
}

window.connectAPI = async function connectAPI() {
  for (let i = 0; i < 40; i++) {
    if (window.go && window.go.main && window.go.main.App && window.runtime) break;
    await new Promise(r => setTimeout(r, 25));
  }
  if (window.go && window.go.main && window.go.main.App && window.runtime) {
    const A = window.go.main.App;
    return {
      demo: false,
      call: (name, ...args) => A[name](...args),
      on: (ev, fn) => window.runtime.EventsOn(ev, fn),
      setTitle: t => { try { window.runtime.WindowSetTitle(t); } catch (e) { /* ignorera */ } },
    };
  }
  return createDemoAPI();
};

function createDemoAPI() {
  const listeners = {};
  const emit = (ev, data) => (listeners[ev] || []).forEach(fn => fn(data));
  let n = 0;
  const id = () => 'demo' + (++n);
  const now = () => Date.now();
  const alerts = [
    { id: 'down', name: T('Down'), condition: 'down', threshold: 0, count: 3, samples: 3, popup: true, sound: true, soundName: 'alarm', notify: false, email: false, emailTo: '', log: true, repeatMin: 0, onClear: true },
    { id: 'latency', name: T('High latency'), condition: 'latency', threshold: 150, count: 6, samples: 10, popup: true, sound: false, soundName: 'beep', notify: false, email: false, emailTo: '', log: true, repeatMin: 0, onClear: true },
    { id: 'loss', name: T('Packet loss'), condition: 'loss', threshold: 5, count: 60, samples: 60, popup: true, sound: false, soundName: 'beep', notify: false, email: false, emailTo: '', log: true, repeatMin: 0, onClear: true },
  ];
  const mk = (host, name, extra = {}) => Object.assign({ id: id(), host, name, kind: 'icmp', port: 0, intervalMs: 1000, timeoutMs: 2000, size: 32, ipMode: 'auto', group: '', warnMs: 0, alertIds: ['down'], paused: false }, extra);
  const targets = [
    mk('192.168.1.1', T('Office router')),
    mk('10.20.0.1', T('Core switch')),
    mk('10.20.0.21', T('AP Warehouse 1'), { alertIds: ['down', 'latency'] }),
    mk('10.20.0.22', T('AP Warehouse 2'), { alertIds: ['down', 'latency'] }),
    mk('10.20.0.23', T('AP Store')),
    mk('1.1.1.1', 'Cloudflare DNS'),
    mk('8.8.8.8', 'Google DNS'),
    mk('spelaretro.se', T('Web shop'), { kind: 'tcp', port: 443 }),
    mk('10.20.0.40', T('Checkout printer')),
    mk('vpn.customer.com', 'Site-to-site VPN', { alertIds: ['down', 'loss'] }),
  ];
  const profile = { 'Router kontor': [0.6, 0.3], 'Core-switch': [0.9, 0.4], 'AP Lager 1': [3.2, 2.5], 'AP Lager 2': [18, 30],
    'AP Butik': [2.4, 1.2], 'Cloudflare DNS': [8.5, 1.4], 'Google DNS': [11.2, 2.1], 'Webbutik': [24, 6],
    'Skrivare Kassa': [1.2, 0.5], 'Site-to-site VPN': [31, 9] };
  const r2 = v => Math.round(v * 100) / 100;
  const hist = {}, stats = {};
  const settings = { statsWindow: 60, warnMs: 100, warnLossPct: 2, downAfter: 3, historySize: 21600 };
  const sample = (t, tm) => {
    const [base, j] = profile[t.name] || [15, 4];
    if (t.name === 'Skrivare Kassa' && (tm / 1000) % 600 > 520) return -1;
    if (t.name === 'AP Lager 2' && Math.random() < 0.04) return -1;
    if (t.name === 'Site-to-site VPN' && Math.random() < 0.015) return -1;
    let v = base + Math.abs(gauss()) * j;
    if (t.name === 'AP Lager 2' && ((tm / 1000) % 300) > 240) v += 140 + Math.random() * 90;
    return Math.round(v * 1000) / 1000;
  };
  function gauss() { let u = 0, v = 0; while (!u) u = Math.random(); while (!v) v = Math.random(); return Math.sqrt(-2 * Math.log(u)) * Math.cos(2 * Math.PI * v); }
  const t0 = now();
  for (const t of targets) {
    hist[t.id] = [];
    for (let i = 3600; i > 0; i--) { const tm = t0 - i * 1000; hist[t.id].push({ t: tm, r: sample(t, tm) }); }
    stats[t.id] = compute(t);
  }
  function compute(t) {
    const h = hist[t.id] || [];
    const w = h.slice(-settings.statsWindow);
    const ok = w.filter(s => s.r >= 0).map(s => s.r);
    let consec = 0; for (let i = h.length - 1; i >= 0 && h[i].r < 0; i--) consec++;
    const avg = ok.length ? ok.reduce((a, b) => a + b, 0) / ok.length : 0;
    let jit = 0; for (let i = 1; i < ok.length; i++) jit += Math.abs(ok[i] - ok[i - 1]); jit = ok.length > 1 ? jit / (ok.length - 1) : 0;
    const loss = w.length ? (w.length - ok.length) * 100 / w.length : 0;
    let status = 'up';
    if (t.paused) status = 'paused';
    else if (consec >= settings.downAfter) status = 'down';
    else if (consec > 0 || loss > settings.warnLossPct || avg > (t.warnMs || settings.warnMs)) status = 'warn';
    const prev = stats[t.id];
    return { status, since: prev && prev.status === status ? prev.since : now() - (prev ? 0 : 3600e3 * Math.random()),
      last: h.length ? h[h.length - 1].r : -1, avg: r2(avg), min: ok.length ? Math.min(...ok) : 0, max: ok.length ? Math.max(...ok) : 0,
      jitter: r2(jit), loss: r2(loss), window: w.length, sent: h.length, lost: h.filter(s => s.r < 0).length, consecLost: consec,
      addr: /^[\d.]+$/.test(t.host) ? t.host : '93.184.216.' + (t.host.length * 7 % 250), err: consec ? T('Timed out') : '' };
  }
  const active = {};
  setInterval(() => {
    const tm = now(), samples = {};
    for (const t of targets) {
      if (t.paused) { stats[t.id] = compute(t); continue; }
      const s = { t: tm, r: sample(t, tm) };
      hist[t.id].push(s); if (hist[t.id].length > 21600) hist[t.id].shift();
      samples[t.id] = [s];
      const before = stats[t.id].status;
      stats[t.id] = compute(t);
      const key = 'down|' + t.id;
      if (stats[t.id].status === 'down' && before !== 'down' && t.alertIds.includes('down')) {
        active[key] = tm;
        emit('alert', { kind: 'fire', alertId: 'down', alertName: T('Down'), targetId: t.id, targetName: t.name || t.host, host: t.host, time: tm, detail: T('3 of 3 latest packets lost'), popup: true, sound: false, soundName: 'alarm' });
      } else if (before === 'down' && stats[t.id].status !== 'down' && active[key]) {
        delete active[key];
        emit('alert', { kind: 'clear', alertId: 'down', alertName: T('Down'), targetId: t.id, targetName: t.name || t.host, host: t.host, time: tm, detail: T('The alert lasted a while'), popup: true, sound: false });
      }
    }
    emit('tick', { stats: Object.assign({}, stats), samples, active: Object.assign({}, active) });
  }, 1000);

  let defaults = { intervalMs: 1000, timeoutMs: 2000, size: 32, alertIds: ['down'] };
  let smtp = { host: '', port: 587, security: 'starttls', username: '', password: '', from: '', minMinutes: 5 };
  let ui = { theme: 'auto', columns: 0, density: 'normal' };
  const api = {
    GetState: () => ({ version: 'demo', platform: 'web', configPath: T('(demo mode)'), targets: targets.slice(), stats: Object.assign({}, stats), alerts: alerts.slice(),
      settings, defaults, smtp, hasPassword: false, alertLog: T('(demo mode)'), ui, active: Object.assign({}, active) }),
    AddHosts: (text, inp) => {
      const out = [];
      for (const tok of text.split(/[\s,;]+/).filter(Boolean)) {
        const web = /^https?:\/\//i.test(tok) || inp.kind === 'http';
        const m = !web && inp.kind !== 'dns' && tok.match(/^(.+):(\d+)$/);
        const t = web ? mk(/:\/\//.test(tok) ? tok : 'https://' + tok, '', { kind: 'http', port: 0 })
          : mk(m ? m[1] : tok, '', m ? { kind: 'tcp', port: +m[2] } : { kind: inp.kind, port: inp.port });
        targets.push(t); hist[t.id] = []; stats[t.id] = { status: 'pending', since: now(), last: -1 }; out.push(t);
      }
      return out;
    },
    SaveTarget: t => { const i = targets.findIndex(x => x.id === t.id); if (i >= 0) targets[i] = t; else { t.id = id(); targets.push(t); hist[t.id] = []; } return t; },
    RemoveTargets: ids => { for (const i of ids) { const k = targets.findIndex(t => t.id === i); if (k >= 0) targets.splice(k, 1); delete stats[i]; } },
    SetPaused: (ids, p) => { for (const t of targets) if (ids.includes(t.id)) t.paused = p; },
    PauseAll: p => { for (const t of targets) t.paused = p; },
    Reorder: ids => { targets.sort((a, b) => ids.indexOf(a.id) - ids.indexOf(b.id)); },
    GetHistory: (tid, since) => { const h = (hist[tid] || []).filter(s => s.t > since); return { t: h.map(s => s.t), r: h.map(s => s.r) }; },
    SaveSettings: s => { Object.assign(settings, s.settings); defaults = s.defaults; smtp = s.smtp; ui = s.ui; },
    SaveAlerts: l => { alerts.splice(0, alerts.length, ...l.map(a => Object.assign({ id: a.id || id() }, a))); return alerts.slice(); },
    AssignAlert: (aid, ids, on) => { for (const t of targets) if (ids.includes(t.id)) { t.alertIds = t.alertIds.filter(x => x !== aid); if (on) t.alertIds.push(aid); } },
    TestAlert: a => { emit('alert', { kind: 'test', alertId: a.id, alertName: a.name, targetName: T('Test target'), host: '192.0.2.1', time: now(), detail: T('This is a test of the alert'), popup: a.popup, sound: a.sound, soundName: a.soundName }); },
    TestEmail: () => { throw new Error(T('Email cannot be sent in demo mode')); },
    AlertHistory: () => [],
    StartTrace: host => {
      const hops = ['192.168.1.1', '10.0.0.1', '', '62.20.1.9', '213.248.70.5', '1.1.1.1'];
      hops.forEach((a, i) => setTimeout(() => emit('trace:hop', { n: i + 1, addr: a, name: a ? '' : '', rtt: a ? r2(1 + i * 3.7 + Math.random()) : -1 }), 300 * (i + 1)));
      setTimeout(() => emit('trace:done', ''), 300 * (hops.length + 1));
    },
    StopTrace: () => {},
    Networks: () => [{ iface: 'eth0', addr: '192.168.1.42', cidr: '192.168.1.0/24', hosts: 254 }],
    StartScan: cidr => {
      const devs = [
        { ip: '192.168.1.1', mac: '24:a4:3c:01:02:03', vendor: 'Ubiquiti', name: 'gateway.lan', rtt: 0.6, ports: [22, 53, 80, 443], kind: 'router' },
        { ip: '192.168.1.10', mac: '00:11:32:aa:bb:cc', vendor: 'Synology', name: 'nas.lan', rtt: 0.9, ports: [80, 139, 443, 445, 5000], kind: 'nas' },
        { ip: '192.168.1.20', mac: '3c:2a:f4:11:22:33', vendor: 'Brother', name: 'BRN3C2AF4112233', rtt: 2.1, ports: [80, 515, 631, 9100], kind: 'printer' },
        { ip: '192.168.1.21', mac: 'b8:27:eb:12:34:56', vendor: 'Raspberry Pi', name: 'pi-kontor', rtt: 1.2, ports: [22], kind: 'ssh' },
        { ip: '192.168.1.42', mac: '9c:b6:d0:01:02:03', vendor: '', name: 'jonas-laptop', rtt: 0.05, ports: [], kind: '', self: true },
        { ip: '192.168.1.57', mac: 'da:a1:19:22:33:44', vendor: 'Private address', name: '', rtt: 48, ports: [62078], kind: 'apple' },
        { ip: '192.168.1.60', mac: '78:8a:20:aa:bb:cc', vendor: 'Ubiquiti', name: 'U6-Pro-Lager', rtt: 1.4, ports: [22, 443], kind: 'ap' },
      ];
      let done = 0;
      const tick = setInterval(() => {
        done = Math.min(254, done + 32);
        emit('scan:progress', { done, total: 254, found: Math.round(devs.length * done / 254) });
        if (done >= 254) { clearInterval(tick); emit('scan:done', { devices: devs, err: '' }); }
      }, 250);
    },
    StopScan: () => {},
    WiFiSurvey: async () => {
      await new Promise(r => setTimeout(r, 800));
      const j = v => v + Math.round(Math.random() * 6 - 3);
      const bss = [
        ['Kontoret', '24:5a:4c:11:22:33', -58, 36, 5180, 80, 'WPA2/WPA3', 'Ubiquiti', true], ['Kontoret', '24:5a:4c:11:22:44', -66, 52, 5260, 80, 'WPA2/WPA3', 'Ubiquiti'],
        ['Kontoret', '24:5a:4c:11:22:34', -55, 6, 2437, 20, 'WPA2/WPA3', 'Ubiquiti'], ['Kontoret-Gäst', '26:5a:4c:11:22:33', -59, 36, 5180, 80, 'Open', 'Private address'],
        ['Granne AB', 'f0:9f:c2:aa:bb:cc', -71, 40, 5200, 40, 'WPA2 Enterprise', 'Ubiquiti'], ['Telia-7A2C', '00:1d:aa:12:34:56', -74, 3, 2422, 40, 'WPA2', 'DrayTek'],
        ['HP-Print-3C', '3c:52:82:01:02:03', -80, 11, 2462, 20, 'WPA2', 'HP'], ['', '70:3a:cb:01:02:03', -84, 1, 2412, 20, 'WPA2', 'Google'],
        ['Comhem_5G', 'e4:8d:8c:ab:cd:ef', -77, 100, 5500, 160, 'WPA2', 'Routerboard'], ['Lager', '24:5a:4c:99:88:77', -69, 37, 6135, 160, 'WPA3', 'Ubiquiti'],
      ].map(([ssid, bssid, rssi, ch, freq, width, sec, vendor, conn]) => ({ ssid, bssid, rssi: j(rssi), ch, freq, width, sec, vendor, conn: !!conn, std: freq > 5900 ? '802.11ax' : '802.11ac' }));
      bss.sort((a, b) => b.rssi - a.rssi);
      return { t: Date.now(), tool: 'demo', bss, cur: { ssid: 'Kontoret', bssid: '24:5a:4c:11:22:33', rssi: bss.find(b => b.conn).rssi, ch: 36, freq: 5180 } };
    },
    SaveTextFile: async name => name,
    GetServer: () => ({ enabled: false, url: '', insecure: false, hasToken: false, status: {} }),
    TestServer: () => { throw new Error(T('Not available in demo mode')); },
    SaveServer: () => {},
    ExportCSV: () => '',
    OpenPath: () => {},
  };
  return {
    demo: true,
    call: async (name, ...args) => api[name](...args),
    on: (ev, fn) => { (listeners[ev] = listeners[ev] || []).push(fn); },
    setTitle: t => { document.title = t; },
  };
}
