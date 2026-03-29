/**
 * app.ts — UCware Mail 클라이언트 메인 로직
 *
 * 상태(state) 기반 SPA 렌더.
 * - 로그인/로그아웃
 * - 폴더 네비게이션
 * - 메시지 목록 / 상세 보기
 * - 편지 쓰기 (compose)
 * - localStorage: drafts, starred
 */
import { Auth, api, ApiError } from './api.js';
// ─── 폴더 정의 ───────────────────────────────────────────────────────────────
const FOLDERS = [
    { key: 'INBOX', label: '받은편지함', ic: '📥' },
    { key: 'SENT', label: '보낸편지함', ic: '📤' },
    { key: 'DRAFTS', label: '임시보관함', ic: '📝', local: true },
    { key: 'STARRED', label: '별표편지함', ic: '⭐', virtual: true },
    { key: 'IMPORTANT', label: '중요편지함', ic: '🏷️' },
    { key: 'SPAM', label: '스팸함', ic: '🚫' },
    { key: 'TRASH', label: '휴지통', ic: '🗑️' },
];
// ─── 애플리케이션 상태 ────────────────────────────────────────────────────────
const state = {
    user: null,
    mailboxes: [],
    activeFolder: 'INBOX',
    messages: [],
    activeMessage: null,
    readIds: new Set(),
    starredIds: new Set(),
    drafts: [],
    loading: false,
};
// ─── localStorage 헬퍼 ────────────────────────────────────────────────────────
function lsGet(key, fallback) {
    try {
        const raw = localStorage.getItem(key);
        return raw !== null ? JSON.parse(raw) ?? fallback : fallback;
    }
    catch {
        return fallback;
    }
}
function lsSet(key, val) {
    localStorage.setItem(key, JSON.stringify(val));
}
function loadLocalState() {
    state.readIds = new Set(lsGet('ucw_read', []));
    state.starredIds = new Set(lsGet('ucw_starred', []));
    state.drafts = lsGet('ucw_drafts', []);
}
function saveLocalState() {
    lsSet('ucw_read', [...state.readIds]);
    lsSet('ucw_starred', [...state.starredIds]);
    lsSet('ucw_drafts', state.drafts);
}
// ─── DOM 참조 ─────────────────────────────────────────────────────────────────
function getEl(id) {
    const el = document.getElementById(id);
    if (!el)
        throw new Error(`Element #${id} not found`);
    return el;
}
let dom;
function initDom() {
    dom = {
        loginScreen: getEl('login-screen'),
        appScreen: getEl('app-screen'),
        loginEmail: getEl('login-email'),
        loginPass: getEl('login-pass'),
        loginBtn: getEl('login-btn'),
        loginErr: getEl('login-err'),
        userAvatar: getEl('user-avatar'),
        navList: getEl('nav-list'),
        folderTitle: getEl('folder-title'),
        messageList: getEl('message-list'),
        messageDetail: getEl('message-detail'),
        composeWindow: getEl('compose-window'),
        composeBtn: getEl('compose-btn'),
        composeTo: getEl('compose-to'),
        composeSubj: getEl('compose-subj'),
        composeBody: getEl('compose-body'),
        sendBtn: getEl('send-btn'),
        searchInput: getEl('search-input'),
        toastContainer: getEl('toast-container'),
    };
}
// ─── Toast 알림 ───────────────────────────────────────────────────────────────
function toast(msg, duration = 3000) {
    const el = document.createElement('div');
    el.className = 'toast';
    el.textContent = msg;
    dom.toastContainer.appendChild(el);
    requestAnimationFrame(() => el.classList.add('show'));
    setTimeout(() => {
        el.classList.remove('show');
        setTimeout(() => el.remove(), 300);
    }, duration);
}
// ─── 로그인 ───────────────────────────────────────────────────────────────────
async function handleLogin() {
    const email = dom.loginEmail.value.trim();
    const pass = dom.loginPass.value;
    if (!email || !pass) {
        dom.loginErr.textContent = '이메일과 비밀번호를 입력하세요.';
        return;
    }
    dom.loginBtn.disabled = true;
    dom.loginErr.textContent = '';
    try {
        const data = await api.login(email, pass);
        state.user = { email: data.email, role: data.role };
        await enterApp();
    }
    catch (e) {
        dom.loginErr.textContent =
            e instanceof ApiError && e.status === 401
                ? '이메일 또는 비밀번호가 올바르지 않습니다.'
                : (e instanceof Error ? e.message : '로그인 실패');
    }
    finally {
        dom.loginBtn.disabled = false;
    }
}
// ─── 앱 진입 ─────────────────────────────────────────────────────────────────
async function enterApp() {
    loadLocalState();
    dom.loginScreen.hidden = true;
    dom.appScreen.hidden = false;
    // 아바타 이니셜
    const initial = (state.user.email[0] ?? '?').toUpperCase();
    dom.userAvatar.textContent = initial;
    dom.userAvatar.title = `${state.user.email} (${state.user.role})`;
    await loadMailboxes();
    renderNav();
    await openFolder('INBOX');
}
// ─── 로그아웃 ─────────────────────────────────────────────────────────────────
async function handleLogout() {
    try {
        await api.logout();
    }
    catch { /* ignore */ }
    state.user = null;
    state.mailboxes = [];
    state.messages = [];
    state.activeMessage = null;
    dom.appScreen.hidden = true;
    dom.loginScreen.hidden = false;
    dom.loginEmail.value = '';
    dom.loginPass.value = '';
    dom.loginErr.textContent = '';
}
// ─── 메일박스 로드 ────────────────────────────────────────────────────────────
async function loadMailboxes() {
    try {
        state.mailboxes = await api.getMailboxes(state.user.email);
    }
    catch {
        state.mailboxes = [];
    }
}
// ─── 사이드바 렌더 ────────────────────────────────────────────────────────────
function renderNav() {
    const ul = dom.navList;
    ul.innerHTML = '';
    FOLDERS.forEach(f => {
        const count = f.key === 'DRAFTS' ? state.drafts.length
            : f.key === 'STARRED' ? state.starredIds.size
                : 0;
        ul.appendChild(makeNavItem(f.key, f.ic, f.label, count));
    });
    // API에서 온 추가 메일박스 (기본 폴더에 없는 것)
    const knownKeys = new Set(FOLDERS.map(f => f.key.toUpperCase()));
    const extra = state.mailboxes.filter(b => !knownKeys.has(b.name.toUpperCase()));
    if (extra.length > 0) {
        const div = document.createElement('div');
        div.className = 'nav-divider';
        ul.appendChild(div);
        const lbl = document.createElement('div');
        lbl.className = 'nav-section-label';
        lbl.textContent = '레이블';
        ul.appendChild(lbl);
        extra.forEach(b => ul.appendChild(makeNavItem(b.name.toUpperCase(), '🏷️', b.name, 0)));
    }
}
function makeNavItem(key, ic, label, count) {
    const li = document.createElement('div');
    li.className = 'nav-item' + (state.activeFolder === key ? ' active' : '');
    li.dataset['key'] = key;
    li.innerHTML = `
    <span class="ic">${ic}</span>
    <span class="label">${label}</span>
    ${count > 0 ? `<span class="badge">${count}</span>` : ''}
  `;
    li.addEventListener('click', () => void openFolder(key));
    return li;
}
function updateNavActive() {
    document.querySelectorAll('.nav-item').forEach(el => {
        el.classList.toggle('active', el.dataset['key'] === state.activeFolder);
    });
}
// ─── 폴더 열기 ────────────────────────────────────────────────────────────────
async function openFolder(key) {
    state.activeFolder = key;
    state.activeMessage = null;
    updateNavActive();
    showListView();
    const folder = FOLDERS.find(f => f.key === key);
    dom.folderTitle.textContent = folder ? folder.label : key;
    if (key === 'DRAFTS') {
        state.messages = state.drafts.map(d => ({
            id: d.id,
            mailboxId: '',
            direction: 'outbound',
            fromAddr: state.user.email,
            toAddr: d.to,
            subject: d.subject,
            textBody: d.body,
            isDraft: true,
            receivedAt: d.savedAt,
        }));
        renderMessageList();
        return;
    }
    if (key === 'STARRED') {
        const all = lsGet('ucw_all_messages', []);
        state.messages = all.filter(m => state.starredIds.has(m.id));
        renderMessageList();
        return;
    }
    const box = state.mailboxes.find(b => b.name.toUpperCase() === key.toUpperCase());
    if (!box) {
        state.messages = [];
        renderMessageList();
        return;
    }
    setLoading(true);
    try {
        state.messages = await api.getMessages(box.id);
        lsSet('ucw_all_messages', state.messages);
    }
    catch (e) {
        toast('메시지를 불러오지 못했습니다: ' + (e instanceof Error ? e.message : String(e)));
        state.messages = [];
    }
    finally {
        setLoading(false);
    }
    renderMessageList();
}
function setLoading(on) {
    state.loading = on;
    dom.messageList.innerHTML = on ? '<div class="spinner"></div>' : '';
}
// ─── 메시지 목록 렌더 ─────────────────────────────────────────────────────────
function renderMessageList() {
    const list = dom.messageList;
    const q = dom.searchInput.value.toLowerCase().trim();
    const msgs = q
        ? state.messages.filter(m => (m.fromAddr ?? '').toLowerCase().includes(q) ||
            (m.toAddr ?? '').toLowerCase().includes(q) ||
            (m.subject ?? '').toLowerCase().includes(q) ||
            (m.textBody ?? '').toLowerCase().includes(q))
        : state.messages;
    if (!msgs.length) {
        list.innerHTML = '<div class="msg-empty">이 폴더에 메일이 없습니다.</div>';
        return;
    }
    list.innerHTML = '';
    msgs.forEach(msg => {
        const isRead = state.readIds.has(msg.id);
        const isStarred = state.starredIds.has(msg.id);
        const from = msg.isDraft ? `받는사람: ${msg.toAddr}` : formatAddr(msg.fromAddr ?? '');
        const date = formatDate(msg.receivedAt ?? msg.createdAt ?? '');
        const preview = (msg.textBody ?? '').replace(/\s+/g, ' ').slice(0, 80);
        const el = document.createElement('div');
        el.className = `msg-item ${isRead ? 'read' : 'unread'}`;
        el.innerHTML = `
      <span class="msg-star ${isStarred ? '' : 'empty'}" data-id="${msg.id}" title="${isStarred ? '별표 제거' : '별표 추가'}">
        ${isStarred ? '⭐' : '☆'}
      </span>
      <span class="msg-from">${escHtml(from)}</span>
      <span class="msg-body">
        <span class="msg-subject">${escHtml(msg.subject ?? '(제목 없음)')}</span>
        <span class="msg-preview">${escHtml(preview)}</span>
      </span>
      <span class="msg-date">${date}</span>
    `;
        el.querySelector('.msg-star').addEventListener('click', e => {
            e.stopPropagation();
            const starEl = el.querySelector('.msg-star');
            toggleStar(msg.id, starEl);
        });
        el.addEventListener('click', () => openMessage(msg));
        list.appendChild(el);
    });
}
// ─── 별표 토글 ────────────────────────────────────────────────────────────────
function toggleStar(id, el) {
    if (state.starredIds.has(id)) {
        state.starredIds.delete(id);
        el.textContent = '☆';
        el.classList.add('empty');
    }
    else {
        state.starredIds.add(id);
        el.textContent = '⭐';
        el.classList.remove('empty');
    }
    saveLocalState();
}
// ─── 메시지 상세 보기 ─────────────────────────────────────────────────────────
function openMessage(msg) {
    state.activeMessage = msg;
    state.readIds.add(msg.id);
    saveLocalState();
    showDetailView();
    const from = formatAddr(msg.fromAddr ?? '');
    const initial = (msg.fromAddr ?? '?')[0].toUpperCase();
    const date = formatDateFull(msg.receivedAt ?? msg.createdAt ?? '');
    dom.messageDetail.innerHTML = `
    <div class="detail-toolbar">
      <button class="back-btn" id="detail-back">← 뒤로</button>
      <div style="flex:1"></div>
    </div>
    <div class="detail-body">
      <div class="detail-subject">${escHtml(msg.subject ?? '(제목 없음)')}</div>
      <div class="detail-meta">
        <div class="detail-from-wrap">
          <div class="detail-avatar">${escHtml(initial)}</div>
          <div class="detail-from-info">
            <div class="from-name">${escHtml(from)}</div>
            <div class="from-addr">받는 사람: ${escHtml(msg.toAddr ?? '')}</div>
          </div>
        </div>
        <div class="detail-date">${date}</div>
      </div>
      <div class="detail-text">${escHtml(msg.textBody ?? msg.rawMime ?? '')}</div>
    </div>
  `;
    document.getElementById('detail-back').addEventListener('click', () => {
        state.activeMessage = null;
        showListView();
        renderMessageList();
    });
}
function showListView() {
    dom.messageList.classList.remove('hidden');
    dom.messageDetail.classList.remove('visible');
}
function showDetailView() {
    dom.messageList.classList.add('hidden');
    dom.messageDetail.classList.add('visible');
}
function openCompose(opts = {}) {
    dom.composeTo.value = opts.to ?? '';
    dom.composeSubj.value = opts.subj ?? '';
    dom.composeBody.value = opts.body ?? '';
    dom.composeWindow.classList.add('visible');
    dom.composeTo.focus();
}
function closeCompose() {
    dom.composeWindow.classList.remove('visible');
}
async function handleSend() {
    const to = dom.composeTo.value.trim();
    const subject = dom.composeSubj.value.trim();
    const body = dom.composeBody.value;
    if (!to) {
        toast('받는 사람을 입력하세요.');
        return;
    }
    dom.sendBtn.disabled = true;
    try {
        const sentBox = await api.findOrCreateMailbox(state.user.email, 'SENT');
        await api.createMessage({
            mailboxId: sentBox.id,
            direction: 'outbound',
            fromAddr: state.user.email,
            toAddr: to,
            subject,
            textBody: body,
        });
        toast('메일을 보냈습니다.');
        closeCompose();
        if (state.activeFolder === 'SENT') {
            await loadMailboxes();
            await openFolder('SENT');
        }
    }
    catch (e) {
        toast('전송 실패: ' + (e instanceof Error ? e.message : String(e)));
    }
    finally {
        dom.sendBtn.disabled = false;
    }
}
// ─── 새로고침 ─────────────────────────────────────────────────────────────────
async function refreshFolder() {
    await loadMailboxes();
    await openFolder(state.activeFolder);
    toast('새로고침 완료');
}
// ─── 검색 ────────────────────────────────────────────────────────────────────
function handleSearch(e) {
    if (e.key === 'Enter')
        renderMessageList();
}
// ─── 포맷 헬퍼 ───────────────────────────────────────────────────────────────
function formatAddr(addr) {
    if (!addr)
        return '';
    const m = addr.match(/^([^<]+)<([^>]+)>/);
    return m ? (m[1]?.trim() || m[2] || addr) : addr;
}
function formatDate(iso) {
    if (!iso)
        return '';
    const d = new Date(iso);
    const now = new Date();
    if (d.toDateString() === now.toDateString()) {
        return d.toLocaleTimeString('ko-KR', { hour: '2-digit', minute: '2-digit' });
    }
    if (d.getFullYear() === now.getFullYear()) {
        return d.toLocaleDateString('ko-KR', { month: 'short', day: 'numeric' });
    }
    return d.toLocaleDateString('ko-KR', { year: 'numeric', month: 'short', day: 'numeric' });
}
function formatDateFull(iso) {
    if (!iso)
        return '';
    return new Date(iso).toLocaleString('ko-KR', {
        year: 'numeric', month: 'long', day: 'numeric',
        hour: '2-digit', minute: '2-digit',
    });
}
function escHtml(s) {
    return s
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;');
}
// ─── 초기화 ───────────────────────────────────────────────────────────────────
document.addEventListener('DOMContentLoaded', () => {
    initDom();
    dom.loginBtn.addEventListener('click', () => void handleLogin());
    dom.loginPass.addEventListener('keydown', e => { if (e.key === 'Enter')
        void handleLogin(); });
    dom.loginEmail.addEventListener('keydown', e => { if (e.key === 'Enter')
        dom.loginPass.focus(); });
    dom.userAvatar.addEventListener('click', () => {
        if (confirm(`${state.user?.email} 로그아웃 하시겠습니까?`))
            void handleLogout();
    });
    dom.composeBtn.addEventListener('click', () => openCompose());
    dom.sendBtn.addEventListener('click', () => void handleSend());
    getEl('compose-close').addEventListener('click', closeCompose);
    document.addEventListener('keydown', e => {
        if (e.key === 'Escape')
            closeCompose();
    });
    getEl('refresh-btn').addEventListener('click', () => void refreshFolder());
    dom.searchInput.addEventListener('keydown', handleSearch);
    // 이미 로그인된 세션 확인
    const auth = Auth.get();
    if (auth?.email) {
        state.user = { email: auth.email, role: auth.role };
        void enterApp();
    }
});
