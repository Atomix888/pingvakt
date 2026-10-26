'use strict';
(function () {
  const key = location.pathname.split('/').pop();
  const $ = s => document.querySelector(s);
  const LABEL = { up: 'I drift', warn: 'Störning', down: 'Nere', offline: 'Ingen kontakt', paused: 'Pausad', pending: 'Väntar', maint: 'Service' };
  const OVERALL = { up: 'Alla system fungerar', warn: 'Delvis störning', offline: 'Delvis okänd status', down: 'Driftstörning' };
  const el = (tag, cls, ...kids) => { const e = document.createElement(tag); if (cls) e.className = cls; for (const k of kids) if (k != null) e.append(k.nodeType ? k : document.createTextNode(k)); return e; };
  const pct = v => v < 0 ? '–' : (v >= 99.995 ? '100' : v.toFixed(2).replace('.', ',')) + ' %';
  async function load() {
    try {
      const res = await fetch('/api/public/status/' + encodeURIComponent(key), { cache: 'no-store' });
      if (!res.ok) throw new Error(res.status === 404 ? 'Statussidan finns inte eller har stängts av.' : 'Kunde inte hämta status.');
      const d = await res.json();
      document.title = 'Driftstatus – ' + d.customer;
      $('#customer').textContent = d.customer;
      $('#company').textContent = d.company || '';
      $('#updated').textContent = 'Uppdaterad ' + new Date(d.generated).toLocaleTimeString('sv-SE');
      const ov = $('#overall');
      ov.className = 'sp-overall st-' + d.overall;
      ov.replaceChildren(el('span', 'dot'), OVERALL[d.overall] || '');
      $('#maint').replaceChildren(...(d.maint || []).map(m => el('div', 'sp-maint', '🔧 Pågående service: ' + m)));
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
