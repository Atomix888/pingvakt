/* Brygga mot mätmotorn.
 * I skrivbordsappen (Wails) anropas Go-metoderna via window.go.main.App.
 * Öppnas sidan i en vanlig webbläsare körs ett demoläge med simulerad data,
 * vilket gör gränssnittet lätt att utveckla och testa. */
'use strict';

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
    { id: 'down', name: 'Nere', condition: 'down', threshold: 0, count: 3, samples: 3, popup: true, sound: true, soundName: 'alarm', notify: false, email: false, emailTo: '', log: true, repeatMin: 0, onClear: true },
    { id: 'latency', name: 'Hög latens', condition: 'latency', threshold: 150, count: 6, samples: 10, popup: true, sound: false, soundName: 'beep', notify: false, email: false, emailTo: '', log: true, repeatMin: 0, onClear: true },
    { id: 'loss', name: 'Paketförlust', condition: 'loss', threshold: 5, count: 60, samples: 60, popup: true, sound: false, soundName: 'beep', notify: false, email: false, emailTo: '', log: true, repeatMin: 0, onClear: true },
  ];
  const mk = (host, name, extra = {}) => Object.assign({ id: id(), host, name, kind: 'icmp', port: 0, intervalMs: 1000, timeoutMs: 2000, size: 32, ipMode: 'auto', group: '', warnMs: 0, alertIds: ['down'], paused: false }, extra);
  const targets = [
    mk('192.168.1.1', 'Router kontor'),
    mk('10.20.0.1', 'Core-switch'),
    mk('10.20.0.21', 'AP Lager 1', { alertIds: ['down', 'latency'] }),
    mk('10.20.0.22', 'AP Lager 2', { alertIds: ['down', 'latency'] }),
    mk('10.20.0.23', 'AP Butik'),
    mk('1.1.1.1', 'Cloudflare DNS'),
    mk('8.8.8.8', 'Google DNS'),
    mk('spelaretro.se', 'Webbutik', { kind: 'tcp', port: 443 }),
    mk('10.20.0.40', 'Skrivare Kassa'),
    mk('vpn.kund.se', 'Site-to-site VPN', { alertIds: ['down', 'loss'] }),
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
      addr: /^[\d.]+$/.test(t.host) ? t.host : '93.184.216.' + (t.host.length * 7 % 250), err: consec ? 'Tidsgräns överskriden' : '' };
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
        emit('alert', { kind: 'fire', alertId: 'down', alertName: 'Nere', targetId: t.id, targetName: t.name || t.host, host: t.host, time: tm, detail: '3 av 3 senaste paket förlorade', popup: true, sound: false, soundName: 'alarm' });
      } else if (before === 'down' && stats[t.id].status !== 'down' && active[key]) {
        delete active[key];
        emit('alert', { kind: 'clear', alertId: 'down', alertName: 'Nere', targetId: t.id, targetName: t.name || t.host, host: t.host, time: tm, detail: 'Larmet varade en stund', popup: true, sound: false });
      }
    }
    emit('tick', { stats: Object.assign({}, stats), samples, active: Object.assign({}, active) });
  }, 1000);

  let defaults = { intervalMs: 1000, timeoutMs: 2000, size: 32, alertIds: ['down'] };
  let smtp = { host: '', port: 587, security: 'starttls', username: '', password: '', from: '', minMinutes: 5 };
  let ui = { theme: 'auto', columns: 0, density: 'normal' };
  const api = {
    GetState: () => ({ version: 'demo', platform: 'web', configPath: '(demoläge)', targets: targets.slice(), stats: Object.assign({}, stats), alerts: alerts.slice(),
      settings, defaults, smtp, hasPassword: false, alertLog: '(demoläge)', ui, active: Object.assign({}, active) }),
    AddHosts: (text, inp) => {
      const out = [];
      for (const tok of text.split(/[\s,;]+/).filter(Boolean)) {
        const m = tok.match(/^(.+):(\d+)$/);
        const t = mk(m ? m[1] : tok, '', m ? { kind: 'tcp', port: +m[2] } : { kind: inp.kind, port: inp.port });
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
    TestAlert: a => { emit('alert', { kind: 'test', alertId: a.id, alertName: a.name, targetName: 'Testmål', host: '192.0.2.1', time: now(), detail: 'Detta är ett test av larmet', popup: a.popup, sound: a.sound, soundName: a.soundName }); },
    TestEmail: () => { throw new Error('E-post kan inte skickas i demoläget'); },
    AlertHistory: () => [],
    StartTrace: host => {
      const hops = ['192.168.1.1', '10.0.0.1', '', '62.20.1.9', '213.248.70.5', '1.1.1.1'];
      hops.forEach((a, i) => setTimeout(() => emit('trace:hop', { n: i + 1, addr: a, name: a ? '' : '', rtt: a ? r2(1 + i * 3.7 + Math.random()) : -1 }), 300 * (i + 1)));
      setTimeout(() => emit('trace:done', ''), 300 * (hops.length + 1));
    },
    StopTrace: () => {},
    GetServer: () => ({ enabled: false, url: '', insecure: false, hasToken: false, status: {} }),
    TestServer: () => { throw new Error('Inte tillgängligt i demoläget'); },
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
