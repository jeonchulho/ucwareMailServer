/**
 * api.ts — UCware Mail REST API wrapper
 *
 * 모든 API 호출은 이 모듈을 통합니다.
 * - Authorization 헤더 자동 첨부
 * - 401 응답 시 토큰 자동 갱신 후 1회 재시도
 * - 토큰은 localStorage에 보관
 */

// ─── 타입 정의 ────────────────────────────────────────────────────────────────

export interface AuthTokens {
  accessToken: string;
  refreshToken: string;
  expiresAt: string;
  email: string;
  role: string;
}

export interface Mailbox {
  id: string;
  userEmail: string;
  name: string;
  createdAt?: string;
}

export interface Message {
  id: string;
  mailboxId: string;
  direction: 'inbound' | 'outbound';
  fromAddr: string;
  toAddr: string;
  subject: string;
  textBody: string;
  rawMime?: string;
  receivedAt?: string;
  createdAt?: string;
  // 클라이언트 전용 필드
  isDraft?: boolean;
}

export interface CreateMessageParams {
  mailboxId: string;
  direction: 'inbound' | 'outbound';
  fromAddr: string;
  toAddr: string;
  subject?: string;
  textBody?: string;
  rawMime?: string;
}

export interface BuildMimeParams {
  fromAddr: string;
  toAddr: string;
  subject: string;
  textBody: string;
}

// ─── ApiError ────────────────────────────────────────────────────────────────

export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

// ─── Auth (localStorage 기반 토큰 관리) ──────────────────────────────────────

const TOKEN_KEYS = {
  access:  'ucw_access',
  refresh: 'ucw_refresh',
  expires: 'ucw_expires',
  email:   'ucw_email',
  role:    'ucw_role',
} as const;

export const Auth = {
  save(resp: AuthTokens): void {
    localStorage.setItem(TOKEN_KEYS.access,  resp.accessToken);
    localStorage.setItem(TOKEN_KEYS.refresh, resp.refreshToken);
    localStorage.setItem(TOKEN_KEYS.expires, resp.expiresAt);
    localStorage.setItem(TOKEN_KEYS.email,   resp.email);
    localStorage.setItem(TOKEN_KEYS.role,    resp.role);
  },

  clear(): void {
    Object.values(TOKEN_KEYS).forEach(k => localStorage.removeItem(k));
  },

  get(): AuthTokens | null {
    const access = localStorage.getItem(TOKEN_KEYS.access);
    if (!access) return null;
    return {
      accessToken:  access,
      refreshToken: localStorage.getItem(TOKEN_KEYS.refresh) ?? '',
      expiresAt:    localStorage.getItem(TOKEN_KEYS.expires) ?? '',
      email:        localStorage.getItem(TOKEN_KEYS.email)   ?? '',
      role:         localStorage.getItem(TOKEN_KEYS.role)    ?? '',
    };
  },

  /** 60초 여유를 두고 만료 여부 확인 */
  isExpired(): boolean {
    const expires = localStorage.getItem(TOKEN_KEYS.expires);
    if (!expires) return true;
    return Date.now() >= new Date(expires).getTime() - 60_000;
  },
};

// ─── 내부 fetch 래퍼 ─────────────────────────────────────────────────────────

let _refreshingPromise: Promise<boolean> | null = null; // 중복 갱신 방지

async function _doFetch(path: string, options: RequestInit = {}): Promise<Response> {
  const auth = Auth.get();
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(options.headers as Record<string, string> ?? {}),
  };
  if (auth) headers['Authorization'] = `Bearer ${auth.accessToken}`;

  return fetch(path, { ...options, headers });
}

async function _refreshTokens(): Promise<boolean> {
  if (_refreshingPromise) return _refreshingPromise;

  _refreshingPromise = (async (): Promise<boolean> => {
    try {
      const auth = Auth.get();
      if (!auth?.refreshToken) throw new Error('no refresh token');

      const res = await fetch('/v1/auth/refresh', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ refreshToken: auth.refreshToken }),
      });
      if (!res.ok) throw new Error('refresh failed');

      const data = (await res.json()) as AuthTokens;
      Auth.save(data);
      return true;
    } finally {
      _refreshingPromise = null;
    }
  })();

  return _refreshingPromise;
}

/**
 * 인증이 필요한 API 호출.
 * 401 시 토큰 갱신 후 1회 재시도.
 */
export async function apiFetch<T = unknown>(path: string, options: RequestInit = {}): Promise<T> {
  // 만료 임박 시 사전 갱신
  if (Auth.isExpired()) {
    try {
      await _refreshTokens();
    } catch {
      throw new ApiError(401, 'session expired');
    }
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

  const ct = res.headers.get('content-type') ?? '';
  if (ct.includes('application/json')) return res.json() as Promise<T>;
  return null as unknown as T;
}

// ─── Public API ───────────────────────────────────────────────────────────────

export const api = {
  // ── Auth ──────────────────────────────────────────────────────────────────
  async login(email: string, password: string): Promise<AuthTokens> {
    const res = await fetch('/v1/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email, password }),
    });
    if (!res.ok) {
      const text = await res.text().catch(() => res.statusText);
      throw new ApiError(res.status, text.trim());
    }
    const data = (await res.json()) as AuthTokens;
    Auth.save(data);
    return data;
  },

  async logout(): Promise<void> {
    try {
      await apiFetch('/v1/auth/logout', { method: 'POST' });
    } finally {
      Auth.clear();
    }
  },

  // ── Mailboxes ─────────────────────────────────────────────────────────────
  async getMailboxes(userEmail?: string): Promise<Mailbox[]> {
    const qs = userEmail ? `?userEmail=${encodeURIComponent(userEmail)}` : '';
    return apiFetch<Mailbox[]>(`/v1/mailboxes${qs}`);
  },

  async createMailbox(userEmail: string, name: string): Promise<Mailbox> {
    return apiFetch<Mailbox>('/v1/mailboxes', {
      method: 'POST',
      body: JSON.stringify({ userEmail, name }),
    });
  },

  /** userEmail의 name 메일박스를 찾거나 없으면 생성해서 반환 */
  async findOrCreateMailbox(userEmail: string, name: string): Promise<Mailbox> {
    const boxes = await api.getMailboxes(userEmail);
    const found = boxes.find(b => b.name.toUpperCase() === name.toUpperCase());
    if (found) return found;
    return api.createMailbox(userEmail, name);
  },

  // ── Messages ──────────────────────────────────────────────────────────────
  async getMessages(mailboxId: string, limit = 100): Promise<Message[]> {
    return apiFetch<Message[]>(
      `/v1/messages?mailboxId=${encodeURIComponent(mailboxId)}&limit=${limit}`
    );
  },

  /**
   * 메시지 저장 (수신 ingest 또는 발신 보관)
   */
  async createMessage(params: CreateMessageParams): Promise<Message> {
    const { mailboxId, direction, fromAddr, toAddr, subject = '', textBody = '', rawMime } = params;
    return apiFetch<Message>('/v1/messages', {
      method: 'POST',
      body: JSON.stringify({
        mailboxId,
        direction,
        fromAddr,
        toAddr,
        subject,
        textBody,
        rawMime: rawMime ?? buildRawMime({ fromAddr, toAddr, subject, textBody }),
      }),
    });
  },
};

// ─── 헬퍼 ────────────────────────────────────────────────────────────────────

/**
 * buildRawMime — 간단한 RFC 5322 텍스트 메일 생성
 */
export function buildRawMime({ fromAddr, toAddr, subject, textBody }: BuildMimeParams): string {
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
