'use strict';

// ===== Global State =====
let myQQ = 0;
let bubbleRight = true;
let showRawID = true;
let allTables = [];
let currentTable = null;   // currently open table name
let currentOffset = 0;
let pageSize = 50;
let totalMessages = 0;
let currentKeyword = '';
let currentSenderUins = new Set(); // multi-select sender filter
let isLoading = false;
let minTime = 0;
let maxTime = 0;

// Context jump: after loading a page via anchorTime, highlight this message (time+rand)
let pendingHighlight = null; // { time, rand }

// Nickname polling: UINs already confirmed as fetched (stop polling)
const nickFetched = new Set();

// Avatar refresh timer
let avatarRefreshTimer = null;

// ===== Init =====
document.addEventListener('DOMContentLoaded', async () => {
  loadConfigFromServer();
  await loadTables();
  bindSidebarEvents();
  bindSettingsEvents();
  bindChatEvents();
  startAvatarRefresh();
});

// ===== Config =====
function loadConfigFromServer() {
  const sc = window.SERVER_CONFIG || {};
  myQQ = Number(sc.my_qq) || 0;
  bubbleRight = sc.bubble_on_right !== false;
  document.getElementById('myQQ').value = myQQ || '';
  document.getElementById('bubbleRight').checked = bubbleRight;
  document.getElementById('imageBaseDir').value = sc.image_base_dir || '';
  showRawID = localStorage.getItem('showRawID') !== 'false';
  document.getElementById('showRawID').checked = showRawID;
}

// ===== Sidebar: conversation list =====
async function loadTables() {
  try {
    const r = await fetch('/api/tables');
    allTables = await r.json();
    renderConvList();
  } catch (e) {
    document.getElementById('convList').innerHTML =
      '<div class="conv-loading" style="color:#e74c3c">加载失败</div>';
  }
}

function renderConvList() {
  const search = (document.getElementById('convSearch').value || '').toLowerCase();
  const activeKind = document.querySelector('.conv-tab.active')?.dataset.kind || 'all';
  const list = document.getElementById('convList');

  // Preserve scroll position
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

  if (!filtered.length) {
    list.innerHTML = '<div class="conv-loading">暂无记录</div>';
    return;
  }

  list.innerHTML = filtered.map(t => {
    const label = t.nickname ? t.nickname : t.display;
    const isActive = t.name === currentTable ? ' active' : '';
    const badge = t.kind === 'group' ? '<span class="conv-badge">群</span>' : '';

    let avatarHtml;
    if (t.kind === 'group') {
      avatarHtml = groupAvatarSVGImg();
    } else {
      avatarHtml = `<img class="conv-avatar" src="/api/avatar/${escHtml(t.id)}" alt=""
        onerror="this.style.display='none';this.nextElementSibling.style.display='flex'" loading="lazy">
        <div class="conv-avatar-placeholder" style="display:none">${escHtml(label.slice(0,2))}</div>`;
    }

    let displayLabel = label;
    if (t.kind === 'group') {
      const id = t.id;
      if (id.length > 5) {
        displayLabel = '*'.repeat(id.length - 5) + id.slice(-5);
      } else {
        displayLabel = id;
      }
      if (t.nickname) displayLabel = t.nickname;
    }

    return `<div class="conv-item${isActive}" data-name="${escHtml(t.name)}" onclick="openChat('${escHtml(t.name)}')">
      ${avatarHtml}
      <div class="conv-info">
        <div class="conv-name">${escHtml(displayLabel)}${badge}</div>
        <div class="conv-meta">${t.kind === 'buddy' ? `QQ: ${t.id}` : (showRawID ? `群原始ID: ${t.id}` : '')}</div>
      </div>
    </div>`;
  }).join('');

  list.scrollTop = scrollTop;
}

function groupAvatarSVGImg() {
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="40" viewBox="0 0 40 40">
    <rect width="40" height="40" rx="8" fill="#5B9BD5"/>
    <circle cx="14" cy="14" r="5" fill="white" opacity="0.9"/>
    <path d="M4 32 Q4 22 14 22 Q24 22 24 32" fill="white" opacity="0.9"/>
    <circle cx="26" cy="13" r="4" fill="white" opacity="0.6"/>
    <path d="M18 31 Q18 22 26 22 Q34 22 34 31" fill="white" opacity="0.6"/>
  </svg>`;
  const encoded = 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(svg);
  return `<img class="conv-avatar" src="${encoded}" alt="群">`;
}

// ===== Open a chat (SPA: no page reload) =====
async function openChat(tableName) {
  if (tableName === currentTable) return;

  currentTable = tableName;
  currentOffset = 0;
  currentKeyword = '';
  currentSenderUins = new Set();
  totalMessages = 0;
  pendingHighlight = null;
  nickFetched.clear();

  // Reset search UI
  document.getElementById('searchInput').value = '';
  document.getElementById('clearSearchBtn').style.display = 'none';
  resetSenderMultiselect();

  // Show chat UI, hide welcome screen
  document.getElementById('welcomeScreen').style.display = 'none';
  document.getElementById('chatHeader').style.display = 'flex';
  document.getElementById('messagesArea').style.display = 'block';
  document.getElementById('paginationBar').style.display = 'flex';
  document.getElementById('settingsPanel').style.display = 'none';

  // Update active item in sidebar
  renderConvList();

  // Update title
  const tableInfo = allTables.find(t => t.name === tableName);
  if (tableInfo) {
    const label = tableInfo.nickname ? tableInfo.nickname : tableInfo.display;
    document.getElementById('chatTitle').textContent = label;
  } else {
    document.getElementById('chatTitle').textContent = tableName;
  }
  document.getElementById('chatSubtitle').textContent = '';

  // Load time range
  try {
    const r = await fetch(`/api/timerange/${encodeURIComponent(tableName)}`);
    const tr = await r.json();
    minTime = tr.min || 0;
    maxTime = tr.max || 0;
    setupTimeRange();
  } catch (e) { /* ignore */ }

  await loadSenders(tableName);
  await loadMessages(0);
}

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

// ===== Multi-select sender filter =====

// Sender list data: [{uin, nickname}]
let senderList = [];

async function loadSenders(tableName) {
  try {
    const r = await fetch(`/api/senders/${encodeURIComponent(tableName)}`);
    senderList = await r.json();
    buildSenderMultiselect(senderList);
    document.getElementById('chatSubtitle').textContent = `共 ${senderList.length} 位成员`;
  } catch (e) { senderList = []; }
}

function buildSenderMultiselect(senders) {
  const listEl = document.getElementById('senderMsList');
  listEl.innerHTML = senders.map(s => {
    const label = s.nickname ? `${s.nickname} (${s.uin})` : String(s.uin);
    return `<label class="sender-ms-item">
      <input type="checkbox" class="sender-ms-cb" value="${s.uin}"> ${escHtml(label)}
    </label>`;
  }).join('');

  // Bind individual checkboxes
  listEl.querySelectorAll('.sender-ms-cb').forEach(cb => {
    cb.addEventListener('change', onSenderCheckboxChange);
  });

  updateSenderMsLabel();
}

function onSenderCheckboxChange() {
  // Rebuild currentSenderUins from checked boxes
  currentSenderUins = new Set();
  document.querySelectorAll('.sender-ms-cb:checked').forEach(cb => {
    currentSenderUins.add(Number(cb.value));
  });

  // Sync "全部成员" master checkbox
  const allCb = document.getElementById('senderMsAll');
  const total = document.querySelectorAll('.sender-ms-cb').length;
  const checked = document.querySelectorAll('.sender-ms-cb:checked').length;
  allCb.checked = checked === 0 || checked === total;
  allCb.indeterminate = checked > 0 && checked < total;

  updateSenderMsLabel();
  currentOffset = 0;
  loadMessages(0);
}

function onSenderMsAllChange() {
  const allCb = document.getElementById('senderMsAll');
  const cbs = document.querySelectorAll('.sender-ms-cb');
  cbs.forEach(cb => { cb.checked = false; }); // always uncheck all (selecting "全部" = no filter)
  allCb.checked = true;
  allCb.indeterminate = false;
  currentSenderUins = new Set();
  updateSenderMsLabel();
  currentOffset = 0;
  loadMessages(0);
}

function updateSenderMsLabel() {
  const labelEl = document.getElementById('senderMsLabel');
  const size = currentSenderUins.size;
  if (size === 0) {
    labelEl.textContent = '全部成员';
  } else if (size === 1) {
    const uin = [...currentSenderUins][0];
    const s = senderList.find(x => x.uin === uin);
    labelEl.textContent = s && s.nickname ? s.nickname : String(uin);
  } else {
    labelEl.textContent = `已选 ${size} 人`;
  }
}

function resetSenderMultiselect() {
  currentSenderUins = new Set();
  document.querySelectorAll('.sender-ms-cb').forEach(cb => { cb.checked = false; });
  const allCb = document.getElementById('senderMsAll');
  if (allCb) { allCb.checked = true; allCb.indeterminate = false; }
  updateSenderMsLabel();
}

function toggleSenderDropdown() {
  const dd = document.getElementById('senderMsDropdown');
  const isOpen = dd.style.display !== 'none';
  dd.style.display = isOpen ? 'none' : 'block';
  if (!isOpen) {
    // Auto-focus search box when opening
    const searchEl = document.getElementById('senderMsSearch');
    if (searchEl) {
      searchEl.value = '';
      filterSenderList();
      setTimeout(() => searchEl.focus(), 50);
    }
  }
}

// Filter the visible sender items by search text
function filterSenderList() {
  const kw = (document.getElementById('senderMsSearch').value || '').toLowerCase();
  document.querySelectorAll('#senderMsList .sender-ms-item').forEach(item => {
    const text = item.textContent.toLowerCase();
    item.style.display = (!kw || text.includes(kw)) ? '' : 'none';
  });
}

// Close dropdown when clicking outside
document.addEventListener('click', e => {
  const ms = document.getElementById('senderMultiselect');
  if (ms && !ms.contains(e.target)) {
    const dd = document.getElementById('senderMsDropdown');
    if (dd) dd.style.display = 'none';
  }
});

// ===== Load messages =====
async function loadMessages(offset, anchorTime, scrollMode = 'top') {
  if (!currentTable) return;
  if (isLoading) return;
  isLoading = true;

  const area = document.getElementById('messagesArea');
  const loading = document.getElementById('msgLoading');
  loading.style.display = 'flex';

  const params = new URLSearchParams({ offset, page_size: pageSize });
  if (currentKeyword) params.set('keyword', currentKeyword);
  // Append multiple sender_uin params
  currentSenderUins.forEach(u => params.append('sender_uin', u));
  if (anchorTime) params.set('anchor_time', anchorTime);

  try {
    const r = await fetch(`/api/messages/${encodeURIComponent(currentTable)}?${params}`);
    const data = await r.json();

    currentOffset = (data.offset !== undefined && data.offset !== null) ? data.offset : offset;
    totalMessages = data.total || 0;

    renderMessages(data.messages || [], area);
    updatePagination(data);

    // Handle scrolling
    if (pendingHighlight) {
      // Context jump: highlight and center
      const { time, rand } = pendingHighlight;
      pendingHighlight = null;
      scrollToAndHighlight(time, rand);
    } else if (scrollMode === 'bottom') {
      // Prev page: scroll to bottom
      area.scrollTop = area.scrollHeight;
    } else {
      // Default/Next page: scroll to top
      area.scrollTop = 0;
    }
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
    if (idx === 0 || (msg.time - lastTimestamp) > 300) {
      html += `<div class="time-divider"><span>${formatTime(msg.time)}</span></div>`;
    }
    lastTimestamp = msg.time;

    const senderUin = Number(msg.sender_uin);
    const isSelf = myQQ > 0 && senderUin === myQQ;
    const side = (isSelf && bubbleRight) ? 'right' : 'left';
    const nick = msg.nickname || String(senderUin);
    const avatarUrl = `/api/avatar/${senderUin}`;

    // Context button: shown when in any filtered view (keyword or sender filter)
    const ctxBtn = `<button class="ctx-btn" title="查看上下文"
      onclick="jumpToContext(${msg.time}, ${msg.rand})" aria-label="查看上下文">
      <svg width="13" height="13" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg">
        <circle cx="8" cy="8" r="6.5" stroke="currentColor" stroke-width="1.5"/>
        <line x1="8" y1="5" x2="8" y2="8.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
        <circle cx="8" cy="11" r="0.8" fill="currentColor"/>
      </svg>
      上下文
    </button>`;

    html += `<div class="msg-row ${side}" data-uin="${senderUin}" data-time="${msg.time}" data-rand="${msg.rand}">
      <img class="msg-avatar" src="${avatarUrl}" alt="${escHtml(nick)}" title="${escHtml(nick)}"
           onerror="this.style.display='none';this.nextElementSibling.style.display='flex'"
           data-uin="${senderUin}" loading="lazy">
      <div class="msg-avatar-placeholder" style="display:none" title="${escHtml(nick)}">${escHtml(nick.slice(0,2))}</div>
      <div class="msg-content">
        <div class="msg-nick">${escHtml(nick)}</div>
        <div class="msg-bubble">${msg.html || escHtml(msg.decoded_msg)}</div>
        ${ctxBtn}
      </div>
    </div>`;
  });

  area.innerHTML = html;

  // Update ctx-btn visibility based on current filter state
  updateCtxBtnVisibility();

  // Seed nickFetched for messages that already have nicknames from server
  messages.forEach(m => {
    if (m.nickname) nickFetched.add(Number(m.sender_uin));
  });
}

// Show/hide context buttons based on whether we're in a filtered view
// (keyword search OR one or more sender filters active)
function updateCtxBtnVisibility() {
  const inFilteredView = currentKeyword !== '' || currentSenderUins.size > 0;
  document.querySelectorAll('.ctx-btn').forEach(btn => {
    btn.style.display = inFilteredView ? 'inline-flex' : 'none';
  });
}

// ===== Jump to context =====
window.jumpToContext = function(time, rand) {
  if (!currentTable) return;

  // Clear all filter state (keyword + all sender selections)
  currentKeyword = '';
  currentSenderUins = new Set();
  document.getElementById('searchInput').value = '';
  document.getElementById('clearSearchBtn').style.display = 'none';
  resetSenderMultiselect();

  // Set pending highlight so renderMessages will scroll to it after load
  pendingHighlight = { time, rand };

  // Load messages centered around the target time (anchorTime)
  currentOffset = 0;
  loadMessages(0, time);
};

// Scroll to the target message and apply highlight animation
function scrollToAndHighlight(time, rand) {
  const area = document.getElementById('messagesArea');
  const row = area.querySelector(`.msg-row[data-time="${time}"][data-rand="${rand}"]`);
  if (!row) {
    const fallback = area.querySelector(`.msg-row[data-time="${time}"]`);
    if (fallback) highlightRow(fallback);
    return;
  }
  highlightRow(row);
}

function highlightRow(row) {
  row.scrollIntoView({ block: 'center', behavior: 'smooth' });
  row.classList.remove('msg-highlight');
  void row.offsetWidth;
  row.classList.add('msg-highlight');
  row.addEventListener('animationend', () => {
    row.classList.remove('msg-highlight');
  }, { once: true });
}

// ===== Avatar & nickname refresh =====
function startAvatarRefresh() {
  if (avatarRefreshTimer) clearInterval(avatarRefreshTimer);
  avatarRefreshTimer = setInterval(refreshPending, 3000);
}

function refreshPending() {
  if (!currentTable) return;

  // 1. Retry failed avatars
  document.querySelectorAll('.msg-avatar[data-uin]').forEach(img => {
    if (img.style.display === 'none') {
      const uin = img.dataset.uin;
      const probe = new Image();
      probe.onload = () => {
        img.src = probe.src;
        img.style.display = '';
        const ph = img.nextElementSibling;
        if (ph && ph.classList.contains('msg-avatar-placeholder')) {
          ph.style.display = 'none';
        }
      };
      probe.src = `/api/avatar/${uin}?t=${Date.now()}`;
    }
  });

  // 2. Update nicknames — only for UINs not yet confirmed as fetched
  const toFetch = new Set();
  document.querySelectorAll('.msg-row[data-uin]').forEach(row => {
    const uin = Number(row.dataset.uin);
    if (nickFetched.has(uin)) return;
    const nickEl = row.querySelector('.msg-nick');
    if (nickEl && nickEl.textContent === String(uin)) {
      toFetch.add(uin);
    }
  });

  toFetch.forEach(uin => {
    nickFetched.add(uin);
    fetch(`/api/nickname/${uin}`)
      .then(r => r.json())
      .then(d => {
        if (d.nickname) {
          document.querySelectorAll(`.msg-row[data-uin="${uin}"]`).forEach(row => {
            const nickEl = row.querySelector('.msg-nick');
            if (nickEl) nickEl.textContent = d.nickname;
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
  const end = Math.min(currentOffset + pageSize, data.total);

  document.getElementById('pageInfo').textContent =
    `共 ${data.total.toLocaleString()} 条，第 ${currentOffset + 1}–${end} 条`;
  document.getElementById('pageNum').textContent = `${currentPage} / ${totalPages}`;

  document.getElementById('btnFirst').disabled = !data.has_prev;
  document.getElementById('btnPrev').disabled = !data.has_prev;
  document.getElementById('btnNext').disabled = !data.has_next;
  document.getElementById('btnLast').disabled = !data.has_next;
}

// ===== Event bindings =====
function bindSidebarEvents() {
  document.getElementById('convSearch').addEventListener('input', renderConvList);
  document.querySelectorAll('.conv-tab').forEach(btn => {
    btn.addEventListener('click', () => {
      document.querySelectorAll('.conv-tab').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      renderConvList();
    });
  });
}

function bindSettingsEvents() {
  document.getElementById('settingsToggle').addEventListener('click', () => {
    const panel = document.getElementById('settingsPanel');
    panel.style.display = panel.style.display === 'none' ? 'block' : 'none';
  });
  document.getElementById('saveSettings').addEventListener('click', saveSettings);
}

function bindChatEvents() {
  document.getElementById('searchBtn').addEventListener('click', doSearch);
  document.getElementById('searchInput').addEventListener('keydown', e => {
    if (e.key === 'Enter') doSearch();
  });
  document.getElementById('clearSearchBtn').addEventListener('click', clearSearch);

  // Multi-select sender filter
  document.getElementById('senderMsTrigger').addEventListener('click', e => {
    e.stopPropagation();
    toggleSenderDropdown();
  });
  document.getElementById('senderMsAll').addEventListener('change', onSenderMsAllChange);
  // Prevent dropdown close when clicking inside search box
  document.getElementById('senderMsSearch').addEventListener('click', e => e.stopPropagation());
  document.getElementById('senderMsSearch').addEventListener('input', filterSenderList);

  document.getElementById('timeJumpBtn').addEventListener('click', doTimeJump);
  document.getElementById('timeJumpInput').addEventListener('keydown', e => {
    if (e.key === 'Enter') doTimeJump();
  });

  document.getElementById('exportBtn').addEventListener('click', openExportModal);

  // Pagination
  document.getElementById('btnFirst').addEventListener('click', () => loadMessages(0, null, 'top'));
  document.getElementById('btnPrev').addEventListener('click', () => {
    loadMessages(Math.max(0, currentOffset - pageSize), null, 'bottom');
  });
  document.getElementById('btnNext').addEventListener('click', () => {
    loadMessages(currentOffset + pageSize, null, 'top');
  });
  document.getElementById('btnLast').addEventListener('click', () => {
    const lastOffset = Math.max(0, (Math.ceil(totalMessages / pageSize) - 1) * pageSize);
    loadMessages(lastOffset, null, 'top');
  });
  document.getElementById('pageSizeSelect').addEventListener('change', e => {
    pageSize = parseInt(e.target.value);
    currentOffset = 0;
    loadMessages(0);
  });

  // Keyboard shortcuts
  document.addEventListener('keydown', e => {
    if (e.key === 'Escape') closeLightbox();
  });
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
  const showRawIDVal = document.getElementById('showRawID').checked;

  myQQ = myQQVal;
  bubbleRight = bubbleRightVal;

  showRawID = showRawIDVal;
  localStorage.setItem('showRawID', showRawIDVal);

  try {
    await fetch('/api/config', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        user: { my_qq: myQQVal, bubble_on_right: bubbleRightVal },
        images: { base_dir: imageDirVal }
      })
    });
    const s = document.getElementById('saveStatus');
    s.textContent = '已保存';
    setTimeout(() => s.textContent = '', 2000);
    renderConvList();
    if (currentTable) {
      nickFetched.clear();
      loadMessages(currentOffset);
    }
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
    let p = h < 6 ? '凌晨' : h < 12 ? '上午' : h < 14 ? '中午' : h < 18 ? '下午' : '晚上';
    return `${p}${hm}`;
  } else if (diffDays === 1) {
    return `昨天 ${hm}`;
  } else if (diffDays < 7) {
    return `星期${'日一二三四五六'[d.getDay()]} ${hm}`;
  } else {
    const y = d.getFullYear();
    const mo = String(d.getMonth()+1).padStart(2,'0');
    const da = String(d.getDate()).padStart(2,'0');
    return y === now.getFullYear() ? `${mo}/${da} ${hm}` : `${y}/${mo}/${da} ${hm}`;
  }
}

function escHtml(s) {
  if (s == null) return '';
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

// ===== Export =====

function openExportModal() {
  if (!currentTable) return;
  // Update page export info
  const total = totalMessages;
  const end = Math.min(currentOffset + pageSize, total);
  const info = document.getElementById('exportPageInfo');
  if (info) {
    let desc = `当前页：第 ${currentOffset + 1}–${end} 条（共 ${total.toLocaleString()} 条）`;
    if (currentKeyword) desc += `，关键词：「${escHtml(currentKeyword)}」`;
    if (currentSenderUins.size > 0) desc += `，已过滤 ${currentSenderUins.size} 位发言人`;
    info.textContent = desc;
  }

  // Populate bulk sender checkboxes from senderList
  const listEl = document.getElementById('exportSenderList');
  if (listEl) {
    listEl.innerHTML = senderList.map(s => {
      const label = s.nickname ? `${s.nickname} (${s.uin})` : String(s.uin);
      return `<label class="export-sender-item">
        <input type="checkbox" class="export-sender-cb" value="${s.uin}"> ${escHtml(label)}
      </label>`;
    }).join('');
  }

  // Pre-fill time range from current table
  const fromEl = document.getElementById('bulkTimeFrom');
  const toEl = document.getElementById('bulkTimeTo');
  if (fromEl && minTime) {
    fromEl.min = toLocalDatetimeStr(minTime);
    fromEl.max = maxTime ? toLocalDatetimeStr(maxTime) : '';
    if (!fromEl.value) fromEl.value = toLocalDatetimeStr(minTime);
  }
  if (toEl && maxTime) {
    toEl.min = minTime ? toLocalDatetimeStr(minTime) : '';
    toEl.max = toLocalDatetimeStr(maxTime);
    if (!toEl.value) toEl.value = toLocalDatetimeStr(maxTime);
  }

  // Pre-fill keyword from current search
  const kwEl = document.getElementById('bulkKeyword');
  if (kwEl) kwEl.value = currentKeyword || '';

  document.getElementById('exportModal').style.display = 'flex';
}

window.closeExportModal = function() {
  document.getElementById('exportModal').style.display = 'none';
  document.getElementById('exportProgress').style.display = 'none';
  document.getElementById('exportBulkBtn').disabled = false;
};

window.switchExportTab = function(tab) {
  document.getElementById('exportTabPage').classList.toggle('active', tab === 'page');
  document.getElementById('exportTabBulk').classList.toggle('active', tab === 'bulk');
  document.getElementById('exportPagePanel').style.display = tab === 'page' ? '' : 'none';
  document.getElementById('exportBulkPanel').style.display = tab === 'bulk' ? '' : 'none';
};

window.doExportPage = function() {
  if (!currentTable) return;
  const params = new URLSearchParams({
    offset: currentOffset,
    page_size: pageSize,
  });
  if (currentKeyword) params.set('keyword', currentKeyword);
  currentSenderUins.forEach(u => params.append('sender_uin', u));

  // Trigger download via hidden anchor
  const url = `/api/export/page/${encodeURIComponent(currentTable)}?${params}`;
  triggerDownload(url);
};

window.doExportBulk = function() {
  if (!currentTable) return;

  const keyword = document.getElementById('bulkKeyword').value.trim();
  const timeFromVal = document.getElementById('bulkTimeFrom').value;
  const timeToVal = document.getElementById('bulkTimeTo').value;
  const chunkSize = parseInt(document.getElementById('bulkChunkSize').value) || 500;

  const senderUins = [];
  document.querySelectorAll('.export-sender-cb:checked').forEach(cb => {
    senderUins.push(Number(cb.value));
  });

  const timeFrom = timeFromVal ? Math.floor(new Date(timeFromVal).getTime() / 1000) : 0;
  const timeTo = timeToVal ? Math.floor(new Date(timeToVal).getTime() / 1000) : 0;

  const body = { keyword, sender_uins: senderUins, time_from: timeFrom, time_to: timeTo, chunk_size: chunkSize };

  // Show progress
  document.getElementById('exportProgress').style.display = 'flex';
  document.getElementById('exportBulkBtn').disabled = true;

  fetch(`/api/export/bulk/${encodeURIComponent(currentTable)}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
    .then(async resp => {
      if (!resp.ok) {
        const text = await resp.text();
        throw new Error(text || resp.statusText);
      }
      // Get filename from Content-Disposition header
      const cd = resp.headers.get('Content-Disposition') || '';
      const match = cd.match(/filename="?([^"]+)"?/);
      const filename = match ? match[1] : 'chat_export.zip';
      const blob = await resp.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = filename;
      document.body.appendChild(a);
      a.click();
      setTimeout(() => { URL.revokeObjectURL(url); a.remove(); }, 1000);
    })
    .catch(err => {
      alert('导出失败：' + err.message);
    })
    .finally(() => {
      document.getElementById('exportProgress').style.display = 'none';
      document.getElementById('exportBulkBtn').disabled = false;
    });
};

function triggerDownload(url) {
  const a = document.createElement('a');
  a.href = url;
  a.download = '';
  document.body.appendChild(a);
  a.click();
  setTimeout(() => a.remove(), 500);
}

function toLocalDatetimeStr(ts) {
  const d = new Date(ts * 1000);
  return d.getFullYear() + '-' +
    String(d.getMonth()+1).padStart(2,'0') + '-' +
    String(d.getDate()).padStart(2,'0') + 'T' +
    String(d.getHours()).padStart(2,'0') + ':' +
    String(d.getMinutes()).padStart(2,'0');
}
