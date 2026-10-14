(function () {
  const PAGES = [
    { path: '/fedlet/index',  label: 'Bridge' },
    { path: '/p2pin/index',   label: 'p2put'  },
    { path: '/fedlet/config', label: 'Config' },
  ];
  const css =
    '#fedlet-nav{display:flex;gap:6px;margin:0 0 20px}' +
    '.fedlet-tab{font-size:13px;color:#8b949e;text-decoration:none;padding:4px 12px;border:1px solid #30363d;border-radius:6px}' +
    '.fedlet-tab:hover{color:#58a6ff;border-color:#58a6ff}' +
    '.fedlet-tab.active{color:#58a6ff;border-color:#1f6feb;background:#161b22}';
  const st = document.createElement('style');
  st.textContent = css;
  document.head.appendChild(st);
  const host = document.getElementById('fedlet-nav');
  if (!host) return;
  const here = location.pathname.replace(/\/$/, '');
  PAGES.forEach(p => {
    const a = document.createElement('a');
    a.className = 'fedlet-tab' + (here === p.path ? ' active' : '');
    a.href = p.path;
    a.textContent = p.label;
    host.appendChild(a);
  });
})();
