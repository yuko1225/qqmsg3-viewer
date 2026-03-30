'use strict';

// ===== State =====
let allTables = [];
let currentKind = 'all';
let cfg = {};

// ===== Init =====
document.addEventListener('DOMContentLoaded', async () => {
  await loadConfig();
  await loadTables();
  bindEvents();
});

async function loadConfig() {
  try {
    const r = await fetch('/api/config');
    cfg = await r.json();
    if (cfg.user) {
      document.getElementById('myQQ').value = cfg.user.my_qq || '';
      document.getElementById('bubbleRight').checked = cfg.user.bubble_on_right !== false;
      document.getElementById('imageBaseDir').value = (cfg.images && cfg.images.base_dir) || '';
    }
  } catch (e) { console.warn('load config:', e); }
}

async function loadTables() {
  try {
    const r = await fetch('/api/tables');
    allTables = await r.json();
    renderConvList();
  } catch (e) {
    document.getElementById('convList').innerHTML = '<div class="conv-loading" style="color:#e74c3c">加载失败</div>';
  }
}

function renderConvList() {
  const search = (document.getElementById('convSearch').value || '').toLowerCase();
  const list = document.getElementById('convList');

  // Preserve scroll position
  const scrollTop = list.scrollTop;

  let filtered = allTables;
  if (currentKind !== 'all') {
    filtered = filtered.filter(t => t.kind === currentKind);
  }
  if (search) {
    filtered = filtered.filter(t =>
      t.id.includes(search) ||
      (t.nickname && t.nickname.toLowerCase().includes(search)) ||
      t.display.toLowerCase().includes(search)
    );
  }

  if (filtered.length === 0) {
    list.innerHTML = '<div class="conv-loading">暂无记录</div>';
    return;
  }

  list.innerHTML = filtered.map(t => {
    const label = t.nickname ? t.nickname : t.display;
    const sub = t.kind === 'buddy' ? `QQ: ${t.id}` : `群号后5位: ${t.id}`;
    const badge = t.kind === 'group' ? '<span class="conv-badge">群</span>' : '';
    // Use correct avatar API: group uses /api/groupavatar/, buddy uses /api/avatar/
    const avatarSrc = t.kind === 'group'
      ? `/api/groupavatar/${escHtml(t.id)}`
      : `/api/avatar/${escHtml(t.id)}`;
    return `<div class="conv-item" data-table="${escHtml(t.name)}" onclick="openChat('${escHtml(t.name)}')">
      <img class="conv-avatar" src="${avatarSrc}" alt="" onerror="this.style.display='none';this.nextElementSibling.style.display='flex'" loading="lazy">
      <div class="conv-avatar-placeholder" style="display:none">${escHtml(label.slice(0,2))}</div>
      <div class="conv-info">
        <div class="conv-name">${escHtml(label)}${badge}</div>
        <div class="conv-meta">${escHtml(sub)}</div>
      </div>
    </div>`;
  }).join('');

  // Restore scroll position
  list.scrollTop = scrollTop;
}

function openChat(tableName) {
  window.location.href = `/chat/${encodeURIComponent(tableName)}`;
}

function bindEvents() {
  document.getElementById('convSearch').addEventListener('input', renderConvList);

  document.querySelectorAll('.conv-tab').forEach(btn => {
    btn.addEventListener('click', () => {
      document.querySelectorAll('.conv-tab').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      currentKind = btn.dataset.kind;
      renderConvList();
    });
  });

  document.getElementById('saveSettings').addEventListener('click', saveSettings);
}

async function saveSettings() {
  const myQQ = Number(document.getElementById('myQQ').value) || 0;
  const bubbleRight = document.getElementById('bubbleRight').checked;
  const imageBaseDir = document.getElementById('imageBaseDir').value.trim();

  const payload = {
    user: { my_qq: myQQ, bubble_on_right: bubbleRight },
    images: { base_dir: imageBaseDir }
  };
  try {
    await fetch('/api/config', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
    const s = document.getElementById('saveStatus');
    s.textContent = '已保存';
    setTimeout(() => s.textContent = '', 2000);
  } catch (e) {
    document.getElementById('saveStatus').textContent = '保存失败';
  }
}

function escHtml(s) {
  if (!s) return '';
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}
