'use strict';
function T(s) { const d = window.PV_DICT || {}; return d[s] != null ? d[s] : s; }
(function () {
  const key = location.pathname.split('/').pop();
  const $ = s => document.querySelector(s);
  const LABEL = { up: T('Operational'), warn: T('Degraded'), down: T('Down'), offline: T('No contact'), paused: T('Paused'), pending: T('Pending'), maint: T('Maintenance') };
  const OVERALL = { up: T('All systems operational'), warn: T('Partial disruption'), offline: T('Partially unknown status'), down: T('Service disruption') };
  const el = (tag, cls, ...kids) => { const e = document.createElement(tag); if (cls) e.className = cls; for (const k of kids) if (k != null) e.append(k.nodeType ? k : document.createTextNode(k)); return e; };
  const pct = v => v < 0 ? '–' : (v >= 99.995 ? '100' : (window.PV_LANG === 'sv' ? v.toFixed(2).replace('.', ',') : v.toFixed(2))) + ' %';
  $('#foot').textContent = T('Updates automatically · PingVakt');
  $('#customer').textContent = T('Service status');
  async function load() {
    try {
      const res = await fetch('/api/public/status/' + encodeURIComponent(key), { cache: 'no-store' });
      if (!res.ok) throw new Error(res.status === 404 ? T('The status page does not exist or has been turned off.') : T('Could not load status.'));
      const d = await res.json();
      document.title = T('Service status – ') + d.customer;
      $('#customer').textContent = d.customer;
      $('#company').textContent = d.company || '';
      $('#updated').textContent = T('Updated ') + new Date(d.generated).toLocaleTimeString('sv-SE');
      const ov = $('#overall');
      ov.className = 'sp-overall st-' + d.overall;
      ov.replaceChildren(el('span', 'dot'), OVERALL[d.overall] || '');
      $('#maint').replaceChildren(...(d.maint || []).map(m => el('div', 'sp-maint', T('🔧 Ongoing maintenance: ') + m)));
      $('#sites').replaceChildren(...d.sites.map(s => el('section', 'sp-site', el('h2', null, s.name),
        ...s.targets.map(t => el('div', 'sp-row st-' + t.status,
          el('div', 'nm', el('span', 'dot'), el('span', null, t.name)),
          el('div', 'up', el('b', null, pct(t.uptime24)), ' 24 h'),
          el('div', 'up', el('b', null, pct(t.uptime30)), ' 30 d'),
          el('div', 'lbl', LABEL[t.status] || t.status))))));
    } catch (e) {
      $('#sites').replaceChildren(el('div', 'sp-err', e.message));
    }
  }
  load();
  setInterval(load, 15000);
})();
