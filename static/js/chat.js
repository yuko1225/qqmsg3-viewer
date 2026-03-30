'use strict';

// ===== State =====
const TABLE = window.CHAT_TABLE || '';
let myQQ = 0;        // will be loaded from /api/config as a number
let bubbleRight = true;
let minTime = window.MIN_TIME || 0;
let maxTime = window.MAX_TIME || 0;

let currentOffset = 0;
let pageSize = 50;
let totalMessages = 0;
let currentKeyword = '';
let currentSenderUin = 0;
let isLoading = false;
let allTables = [];
let senders = [];

// Nickname polling: track UINs we've already confirmed as "fetched" (even if empty)
// so we don't keep polling the same UIN over and over.
const nickFetched = new Set();  // UINs whose nickname status is final

// Avatar retry: UINs whose avatar img failed and we should retry
const pendingAvatarRetry = new Set();
let avatarRefreshTimer = null;

// ===== Init =====
document.addEventListener('DOMContentLoaded', async () => {
  await loadConfig();
  setupTimeRange();
  await loadTables();
  await loadSenders();
  await loadMessages(0);
  bindEvents();
  startAvatarRefresh();
});

// ===== Config =====
async function loadConfig() {
  try {
    const r = await fetch('/api/config');
    const cfg = await r.json();
    if (cfg.user) {
      // my_qq comes as a JSON number; ensure we store it as a number
      myQQ = Number(cfg.user.my_qq) || 0;
      bubbleRight = cfg.user.bubble_on_right !== false;
      document.getElementById('myQQ').value = myQQ || '';
      document.getElementById('bubbleRight').checked = bubbleRight;
    }
    if (cfg.images) {
      document.getElementById('imageBaseDir').value = cfg.images.base_dir || '';
    }
  } catch (e) { console.warn('load config:', e); }
}

// ===== Time range setup =====
function setupTimeRange() {
  const inp = document.getElementById('timeJumpInput');
  if (minTime && maxTime) {
    const toLocal = ts => {
      const d = new Date(ts * 1000);
      return d.getFullYear() + '-' +
        String(d.getMonth()+1).padStart(2,'0') + '-' +
        String(d.getDate()).padStart(2,'0') + 'T' +
        String(d.getHours()).padStart(2,'0') + ':' +
        String(d.getMinutes()).padStart(2,'0');
    };
    inp.min = toLocal(minTime);
    inp.max = toLocal(maxTime);
    inp.value = toLocal(minTime);
  }
}

// ===== Load conversation list =====
async function loadTables() {
  try {
    const r = await fetch('/api/tables');
    allTables = await r.json();
    renderConvList();
  } catch (e) { console.warn('load tables:', e); }
}

function renderConvList() {
  const search = (document.getElementById('convSearch').value || '').toLowerCase();
  const activeKind = document.querySelector('.conv-tab.active')?.dataset.kind || 'all';
  const list = document.getElementById('convList');

  // Save scroll position before re-render
  const scrollTop = list.scrollTop;

  let filtered = allTables;
  if (activeKind !== 'all') filtered = filtered.filter(t => t.kind === activeKind);
  if (search) {
    filtered = filtered.filter(t =>
      t.id.includes(search) ||
      (t.nickname && t.nickname.toLowerCase().includes(search)) ||
      t.display.toLowerCase().includes(search)
    );
  }

  if (!filtered.length) { list.innerHTML = '<div class="conv-loading">暂无记录</div>'; return; }

  list.innerHTML = filtered.map(t => {
    const label = t.nickname ? t.nickname : t.display;
    const sub = t.kind === 'buddy' ? `QQ: ${t.id}` : `群号后5位: ${t.id}`;
    const badge = t.kind === 'group' ? '<span class="conv-badge">群</span>' : '';
    const isActive = t.name === TABLE ? ' active' : '';
    // Use correct avatar API: group uses /api/groupavatar/, buddy uses /api/avatar/
    const avatarSrc = t.kind === 'group'
      ? `/api/groupavatar/${escHtml(t.id)}`
      : `/api/avatar/${escHtml(t.id)}`;
    return `<div class="conv-item${isActive}" onclick="gotoChat('${escHtml(t.name)}')">
      <img class="conv-avatar" src="${avatarSrc}" alt="" onerror="this.style.display='none';this.nextElementSibling.style.display='flex'" loading="lazy">
      <div class="conv-avatar-placeholder" style="display:none">${escHtml(label.slice(0,2))}</div>
      <div class="conv-info">
        <div class="conv-name">${escHtml(label)}${badge}</div>
        <div class="conv-meta">${escHtml(sub)}</div>
      </div>
    </div>`;
  }).join('');

  // Restore scroll position after re-render
  list.scrollTop = scrollTop;

  // Scroll active item into view only on initial load (scrollTop was 0)
  if (scrollTop === 0) {
    const activeItem = list.querySelector('.conv-item.active');
    if (activeItem) {
      activeItem.scrollIntoView({ block: 'nearest' });
    }
  }
}

function gotoChat(name) {
  if (name === TABLE) return;
  window.location.href = `/chat/${encodeURIComponent(name)}`;
}

// ===== Load senders =====
async function loadSenders() {
  try {
    const r = await fetch(`/api/senders/${encodeURIComponent(TABLE)}`);
    senders = await r.json();
    const sel = document.getElementById('senderFilter');
    sel.innerHTML = '<option value="">全部成员</option>' +
      senders.map(s => {
        const label = s.nickname ? `${s.nickname} (${s.uin})` : String(s.uin);
        return `<option value="${s.uin}">${escHtml(label)}</option>`;
      }).join('');

    // Update chat title with table info
    const tableInfo = allTables.find(t => t.name === TABLE);
    if (tableInfo) {
      const title = tableInfo.nickname ? tableInfo.nickname : tableInfo.display;
      document.getElementById('chatTitle').textContent = title;
      document.getElementById('chatSubtitle').textContent = `共 ${senders.length} 位成员`;
    }
  } catch (e) { console.warn('load senders:', e); }
}

// ===== Load messages =====
async function loadMessages(offset, anchorTime) {
  if (isLoading) return;
  isLoading = true;

  const area = document.getElementById('messagesArea');
  const loading = document.getElementById('msgLoading');
  loading.style.display = 'flex';

  const params = new URLSearchParams({
    offset: offset,
    page_size: pageSize,
  });
  if (currentKeyword) params.set('keyword', currentKeyword);
  if (currentSenderUin) params.set('sender_uin', currentSenderUin);
  if (anchorTime) params.set('anchor_time', anchorTime);

  try {
    const r = await fetch(`/api/messages/${encodeURIComponent(TABLE)}?${params}`);
    const data = await r.json();

    currentOffset = data.offset !== undefined ? data.offset : offset;
    totalMessages = data.total || 0;

    renderMessages(data.messages || [], area);
    updatePagination(data);
  } catch (e) {
    area.innerHTML = '<div class="msg-loading" style="color:#e74c3c">加载失败</div>';
    console.error('load messages:', e);
  } finally {
    loading.style.display = 'none';
    isLoading = false;
  }
}

// ===== Render messages =====
function renderMessages(messages, area) {
  if (!messages.length) {
    area.innerHTML = '<div class="msg-loading">暂无消息</div>';
    return;
  }

  let html = '';
  let lastTimestamp = 0;

  messages.forEach((msg, idx) => {
    // Time divider: show if gap > 5 minutes or first message
    if (idx === 0 || (msg.time - lastTimestamp) > 300) {
      html += `<div class="time-divider"><span>${formatTime(msg.time)}</span></div>`;
    }
    lastTimestamp = msg.time;

    // Compare as numbers to avoid string/number mismatch
    const senderUin = Number(msg.sender_uin);
    const isSelf = myQQ > 0 && senderUin === myQQ;
    const side = (isSelf && bubbleRight) ? 'right' : 'left';
    const nick = msg.nickname || String(senderUin);
    const avatarUrl = `/api/avatar/${senderUin}`;

    html += `<div class="msg-row ${side}" data-uin="${senderUin}">
      <img class="msg-avatar" src="${avatarUrl}" alt="${escHtml(nick)}" title="${escHtml(nick)}"
           onerror="this.style.display='none';this.nextElementSibling.style.display='flex'"
           data-uin="${senderUin}" loading="lazy">
      <div class="msg-avatar-placeholder" style="display:none" title="${escHtml(nick)}">${escHtml(nick.slice(0,2))}</div>
      <div class="msg-body">
        <div class="msg-nick">${escHtml(nick)}</div>
        <div class="msg-bubble">${msg.html || ''}</div>
      </div>
    </div>`;
  });

  area.innerHTML = html;
  area.scrollTop = 0;

  // Seed nickFetched for messages that already have nicknames
  messages.forEach(m => {
    if (m.nickname) {
      nickFetched.add(Number(m.sender_uin));
    }
  });

  // Collect UINs that need avatar retry
  pendingAvatarRetry.clear();
  messages.forEach(m => pendingAvatarRetry.add(Number(m.sender_uin)));
}

// ===== Avatar & nickname refresh =====
function startAvatarRefresh() {
  if (avatarRefreshTimer) clearInterval(avatarRefreshTimer);
  avatarRefreshTimer = setInterval(refreshPending, 3000);
}

function refreshPending() {
  // 1. Retry broken avatar images
  document.querySelectorAll('.msg-avatar[data-uin]').forEach(img => {
    if (img.style.display === 'none') {
      const uin = img.dataset.uin;
      const newSrc = `/api/avatar/${uin}?t=${Date.now()}`;
      const probe = new Image();
      probe.onload = () => {
        img.src = newSrc;
        img.style.display = '';
        const placeholder = img.nextElementSibling;
        if (placeholder) placeholder.style.display = 'none';
      };
      probe.src = newSrc;
    }
  });

  // 2. Update nicknames for rows that still show raw UIN as name,
  //    but ONLY if we haven't already confirmed the fetch result for that UIN.
  const toFetch = new Set();
  document.querySelectorAll('.msg-row[data-uin]').forEach(row => {
    const uin = Number(row.dataset.uin);
    if (nickFetched.has(uin)) return; // already resolved, skip
    const nickEl = row.querySelector('.msg-nick');
    if (nickEl && nickEl.textContent === String(uin)) {
      toFetch.add(uin);
    }
  });

  toFetch.forEach(uin => {
    fetch(`/api/nickname/${uin}`)
      .then(r => r.json())
      .then(d => {
        // Mark as fetched regardless of result to stop future polling
        if (d.fetched) {
          nickFetched.add(uin);
        }
        if (d.nickname) {
          // Update all rows with this UIN
          document.querySelectorAll(`.msg-row[data-uin="${uin}"]`).forEach(row => {
            const nickEl = row.querySelector('.msg-nick');
            if (nickEl) nickEl.textContent = d.nickname;
            const avatar = row.querySelector('.msg-avatar');
            if (avatar) avatar.alt = d.nickname;
            const placeholder = row.querySelector('.msg-avatar-placeholder');
            if (placeholder) placeholder.title = d.nickname;
          });
        }
      })
      .catch(() => {});
  });
}

// ===== Pagination =====
function updatePagination(data) {
  const totalPages = Math.ceil(data.total / pageSize) || 1;
  const currentPage = Math.floor(currentOffset / pageSize) + 1;

  document.getElementById('pageInfo').textContent =
    `共 ${data.total.toLocaleString()} 条消息，第 ${currentOffset + 1}–${Math.min(currentOffset + pageSize, data.total)} 条`;
  document.getElementById('pageNum').textContent = `${currentPage} / ${totalPages}`;

  document.getElementById('btnFirst').disabled = !data.has_prev;
  document.getElementById('btnPrev').disabled = !data.has_prev;
  document.getElementById('btnNext').disabled = !data.has_next;
  document.getElementById('btnLast').disabled = !data.has_next;
}

// ===== Events =====
function bindEvents() {
  // Conversation list search
  document.getElementById('convSearch').addEventListener('input', renderConvList);

  // Conversation tabs
  document.querySelectorAll('.conv-tab').forEach(btn => {
    btn.addEventListener('click', () => {
      document.querySelectorAll('.conv-tab').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      renderConvList();
    });
  });

  // Search
  document.getElementById('searchBtn').addEventListener('click', doSearch);
  document.getElementById('searchInput').addEventListener('keydown', e => {
    if (e.key === 'Enter') doSearch();
  });
  document.getElementById('clearSearchBtn').addEventListener('click', clearSearch);
  document.getElementById('senderFilter').addEventListener('change', e => {
    currentSenderUin = parseInt(e.target.value) || 0;
    currentOffset = 0;
    loadMessages(0);
  });

  // Time jump
  document.getElementById('timeJumpBtn').addEventListener('click', doTimeJump);
  document.getElementById('timeJumpInput').addEventListener('keydown', e => {
    if (e.key === 'Enter') doTimeJump();
  });

  // Pagination
  document.getElementById('btnFirst').addEventListener('click', () => loadMessages(0));
  document.getElementById('btnPrev').addEventListener('click', () => {
    const newOffset = Math.max(0, currentOffset - pageSize);
    loadMessages(newOffset);
  });
  document.getElementById('btnNext').addEventListener('click', () => {
    loadMessages(currentOffset + pageSize);
  });
  document.getElementById('btnLast').addEventListener('click', () => {
    const lastOffset = Math.max(0, (Math.ceil(totalMessages / pageSize) - 1) * pageSize);
    loadMessages(lastOffset);
  });
  document.getElementById('pageSizeSelect').addEventListener('change', e => {
    pageSize = parseInt(e.target.value);
    currentOffset = 0;
    loadMessages(0);
  });

  // Settings toggle
  document.getElementById('settingsToggle').addEventListener('click', () => {
    const panel = document.getElementById('settingsPanel');
    panel.style.display = panel.style.display === 'none' ? 'block' : 'none';
  });

  // Save settings
  document.getElementById('saveSettings').addEventListener('click', saveSettings);
}

function doSearch() {
  const kw = document.getElementById('searchInput').value.trim();
  currentKeyword = kw;
  currentOffset = 0;
  document.getElementById('clearSearchBtn').style.display = kw ? 'flex' : 'none';
  loadMessages(0);
}

function clearSearch() {
  document.getElementById('searchInput').value = '';
  currentKeyword = '';
  document.getElementById('clearSearchBtn').style.display = 'none';
  currentOffset = 0;
  loadMessages(0);
}

function doTimeJump() {
  const val = document.getElementById('timeJumpInput').value;
  if (!val) return;
  const ts = Math.floor(new Date(val).getTime() / 1000);
  if (isNaN(ts)) return;
  currentOffset = 0;
  loadMessages(0, ts);
}

async function saveSettings() {
  const myQQVal = Number(document.getElementById('myQQ').value) || 0;
  const bubbleRightVal = document.getElementById('bubbleRight').checked;
  const imageDirVal = document.getElementById('imageBaseDir').value.trim();

  myQQ = myQQVal;
  bubbleRight = bubbleRightVal;

  const payload = {
    user: { my_qq: myQQVal, bubble_on_right: bubbleRightVal },
    images: { base_dir: imageDirVal }
  };
  try {
    await fetch('/api/config', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
    const s = document.getElementById('saveStatus');
    s.textContent = '已保存';
    setTimeout(() => s.textContent = '', 2000);
    // Clear nickFetched cache so nicknames reload with new settings
    nickFetched.clear();
    // Reload messages to apply new bubble side
    loadMessages(currentOffset);
  } catch (e) {
    document.getElementById('saveStatus').textContent = '保存失败';
  }
}

// ===== Lightbox =====
window.openLightbox = function(src) {
  document.getElementById('lightboxImg').src = src;
  document.getElementById('lightbox').style.display = 'flex';
};

window.closeLightbox = function() {
  document.getElementById('lightbox').style.display = 'none';
  document.getElementById('lightboxImg').src = '';
};

document.addEventListener('keydown', e => {
  if (e.key === 'Escape') closeLightbox();
});

// ===== Helpers =====
function formatTime(ts) {
  const d = new Date(ts * 1000);
  const now = new Date();
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const msgDay = new Date(d.getFullYear(), d.getMonth(), d.getDate());
  const diffDays = Math.floor((today - msgDay) / 86400000);

  const hm = String(d.getHours()).padStart(2,'0') + ':' + String(d.getMinutes()).padStart(2,'0');

  if (diffDays === 0) {
    const h = d.getHours();
    let period = '';
    if (h < 6) period = '凌晨';
    else if (h < 12) period = '上午';
    else if (h < 14) period = '中午';
    else if (h < 18) period = '下午';
    else period = '晚上';
    return `${period}${hm}`;
  } else if (diffDays === 1) {
    return `昨天 ${hm}`;
  } else if (diffDays < 7) {
    const days = ['日','一','二','三','四','五','六'];
    return `星期${days[d.getDay()]} ${hm}`;
  } else {
    const y = d.getFullYear();
    const mo = String(d.getMonth()+1).padStart(2,'0');
    const da = String(d.getDate()).padStart(2,'0');
    if (y === now.getFullYear()) return `${mo}/${da} ${hm}`;
    return `${y}/${mo}/${da} ${hm}`;
  }
}

function escHtml(s) {
  if (s == null) return '';
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}
