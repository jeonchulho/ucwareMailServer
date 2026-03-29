/**
 * api.js — UCware Mail REST API wrapper
 *
 * 모든 API 호출은 이 모듈을 통합니다.
 * - Authorization 헤더 자동 첨부
 * - 401 응답 시 토큰 자동 갱신 후 1회 재시도
 * - 토큰은 localStorage에 보관
 */

const Auth = (() => {
  const K = {
    access:  'ucw_access',
    refresh: 'ucw_refresh',
    expires: 'ucw_expires',
    email:   'ucw_email',
    role:    'ucw_role',
  };

  function save(resp) {
    localStorage.setItem(K.access,  resp.accessToken);
    localStorage.setItem(K.refresh, resp.refreshToken);
    localStorage.setItem(K.expires, resp.expiresAt);
    localStorage.setItem(K.email,   resp.email);
    localStorage.setItem(K.role,    resp.role);
  }

  function clear() {
    Object.values(K).forEach(k => localStorage.removeItem(k));
  }

  function get() {
    const access = localStorage.getItem(K.access);
    if (!access) return null;
    return {
      accessToken:  access,
      refreshToken: localStorage.getItem(K.refresh),
      expiresAt:    localStorage.getItem(K.expires),
      email:        localStorage.getItem(K.email),
      role:         localStorage.getItem(K.role),
    };
  }

  function isExpired() {
    const expires = localStorage.getItem(K.expires);
    if (!expires) return true;
    // 60초 여유를 두고 갱신
    return Date.now() >= new Date(expires).getTime() - 60_000;
  }

  return { save, clear, get, isExpired };
})();

// ─── 내부 fetch 래퍼 ─────────────────────────────────────────────────────────
let _refreshing = null; // 중복 갱신 방지

async function _doFetch(path, options = {}) {
  const auth = Auth.get();
  const headers = { 'Content-Type': 'application/json', ...(options.headers || {}) };
  if (auth) headers['Authorization'] = `Bearer ${auth.accessToken}`;

  const res = await fetch(path, { ...options, headers });
  return res;
}

async function _refreshTokens() {
  if (_refreshing) return _refreshing;
  _refreshing = (async () => {
    try {
      const auth = Auth.get();
      if (!auth?.refreshToken) throw new Error('no refresh token');
      const res = await fetch('/v1/auth/refresh', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ refreshToken: auth.refreshToken }),
      });
      if (!res.ok) throw new Error('refresh failed');
      const data = await res.json();
      Auth.save(data);
      return true;
    } finally {
      _refreshing = null;
    }
  })();
  return _refreshing;
}

/**
 * 인증이 필요한 API 호출.
 * 401 시 토큰 갱신 후 1회 재시도.
 */
async function apiFetch(path, options = {}) {
  // 만료 임박 시 사전 갱신
  if (Auth.isExpired()) {
    try { await _refreshTokens(); } catch { throw new ApiError(401, 'session expired'); }
  }

  let res = await _doFetch(path, options);

  if (res.status === 401) {
    try {
      await _refreshTokens();
      res = await _doFetch(path, options);
    } catch {
      Auth.clear();
      throw new ApiError(401, 'session expired');
    }
  }

  if (!res.ok) {
    const text = await res.text().catch(() => res.statusText);
    throw new ApiError(res.status, text.trim());
  }

  const ct = res.headers.get('content-type') || '';
  if (ct.includes('application/json')) return res.json();
  return null;
}

class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

// ─── Public API ───────────────────────────────────────────────────────────────
const api = {
  // ── Auth ──────────────────────────────────────────────────────────────────
  async login(email, password) {
    const res = await fetch('/v1/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email, password }),
    });
    if (!res.ok) {
      const text = await res.text().catch(() => res.statusText);
      throw new ApiError(res.status, text.trim());
    }
    const data = await res.json();
    Auth.save(data);
    return data;
  },

  async logout() {
    try {
      await apiFetch('/v1/auth/logout', { method: 'POST' });
    } finally {
      Auth.clear();
    }
  },

  // ── Mailboxes ─────────────────────────────────────────────────────────────
  async getMailboxes(userEmail) {
    const qs = userEmail ? `?userEmail=${encodeURIComponent(userEmail)}` : '';
    return apiFetch(`/v1/mailboxes${qs}`);
  },

  async createMailbox(userEmail, name) {
    return apiFetch('/v1/mailboxes', {
      method: 'POST',
      body: JSON.stringify({ userEmail, name }),
    });
  },

  /** userEmail의 name 메일박스를 찾거나 없으면 생성해서 ID 반환 */
  async findOrCreateMailbox(userEmail, name) {
    const boxes = await api.getMailboxes(userEmail);
    const found = boxes.find(b => b.name.toUpperCase() === name.toUpperCase());
    if (found) return found;
    return api.createMailbox(userEmail, name);
  },

  // ── Messages ──────────────────────────────────────────────────────────────
  async getMessages(mailboxId, limit = 100) {
    return apiFetch(`/v1/messages?mailboxId=${encodeURIComponent(mailboxId)}&limit=${limit}`);
  },

  /**
   * 메시지 저장 (수신 ingest 또는 발신 보관)
   * direction: 'inbound' | 'outbound'
   */
  async createMessage({ mailboxId, direction, fromAddr, toAddr, subject, textBody, rawMime }) {
    return apiFetch('/v1/messages', {
      method: 'POST',
      body: JSON.stringify({
        mailboxId,
        direction,
        fromAddr,
        toAddr,
        subject:  subject  || '',
        textBody: textBody || '',
        rawMime:  rawMime  || buildRawMime({ fromAddr, toAddr, subject: subject || '', textBody: textBody || '' }),
      }),
    });
  },
};

/**
 * buildRawMime — 간단한 RFC 5322 텍스트 메일 생성
 */
function buildRawMime({ fromAddr, toAddr, subject, textBody }) {
  const date = new Date().toUTCString();
  return [
    `From: ${fromAddr}`,
    `To: ${toAddr}`,
    `Subject: ${subject}`,
    `Date: ${date}`,
    `MIME-Version: 1.0`,
    `Content-Type: text/plain; charset=utf-8`,
    `Content-Transfer-Encoding: 8bit`,
    ``,
    textBody,
  ].join('\r\n');
}
