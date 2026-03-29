package web

import (
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/internal/archive"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/internal/store"
	"golang.org/x/crypto/bcrypt"
)

//go:embed templates/* static/*
var embedFS embed.FS

const (
	cookieName   = "ucw_session"
	cookieMaxAge = 8 * 60 * 60 // 8시간
	jwtCookieTTL = 8 * time.Hour
)

// sessionClaims — access token 과 동일한 구조 (기존 JWTSecret 재사용 가능)
type sessionClaims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

type ctxKey int

const (
	ctxEmail ctxKey = iota
	ctxRole
)

// Config — SSR 핸들러 초기화 설정
type Config struct {
	JWTSecret    string
	JWTIssuer    string
	SecureCookie bool // HTTPS 배포 시 true
}

// Handler — Go html/template 기반 서버사이드 렌더링 핸들러
type Handler struct {
	store     *store.SQLiteStore
	archive   *archive.SQLStore
	tpl       *template.Template
	jwtSecret []byte
	jwtIssuer string
	secure    bool
}

// folderDef — 사이드바 폴더 목록
type folderDef struct {
	Key   string
	Label string
	Icon  string
}

var folders = []folderDef{
	{"INBOX", "받은편지함", "📥"},
	{"SENT", "보낸편지함", "📤"},
	{"IMPORTANT", "중요편지함", "🏷️"},
	{"SPAM", "스팸함", "🚫"},
	{"TRASH", "휴지통", "🗑️"},
}

// pageData — 템플릿에 전달하는 데이터
type pageData struct {
	User          *userInfo
	Folders       []folderDef
	ActiveFolder  string
	Mailboxes     []archive.Mailbox
	Messages      []archive.Message
	ActiveMessage *archive.Message
	Error         string
	Flash         string
	Query         string
}

type userInfo struct {
	Email string
	Role  string
}

// NewHandler — Handler 생성자. 템플릿을 embed FS에서 파싱합니다.
func NewHandler(st *store.SQLiteStore, ar *archive.SQLStore, cfg Config) (*Handler, error) {
	funcMap := template.FuncMap{
		"formatDate": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			now := time.Now()
			if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
				return t.Format("15:04")
			}
			if t.Year() == now.Year() {
				return t.Format("1월 2일")
			}
			return t.Format("2006. 1. 2.")
		},
		"formatDateFull": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.Format("2006년 1월 2일 (월) 오후 3:04")
		},
		"shortAddr": func(addr string) string {
			if addr == "" {
				return ""
			}
			if i := strings.Index(addr, "<"); i >= 0 {
				name := strings.TrimSpace(addr[:i])
				if name != "" {
					return name
				}
			}
			return addr
		},
		"preview": func(body string) string {
			s := strings.ReplaceAll(body, "\n", " ")
			s = strings.Join(strings.Fields(s), " ")
			if len([]rune(s)) > 80 {
				return string([]rune(s)[:80]) + "…"
			}
			return s
		},
		"initial": func(addr string) string {
			if addr == "" {
				return "?"
			}
			for _, r := range addr {
				return strings.ToUpper(string(r))
			}
			return "?"
		},
		"add": func(a, b int) int { return a + b },
	}

	tpl, err := template.New("").Funcs(funcMap).ParseFS(embedFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	return &Handler{
		store:     st,
		archive:   ar,
		tpl:       tpl,
		jwtSecret: []byte(cfg.JWTSecret),
		jwtIssuer: cfg.JWTIssuer,
		secure:    cfg.SecureCookie,
	}, nil
}

// StaticHandler — 임베드된 정적 파일 서빙 (/static/ 경로)
func (h *Handler) StaticHandler() http.Handler {
	return http.StripPrefix("/static/", http.FileServer(http.FS(embedFS)))
}

// ─── 세션 ─────────────────────────────────────────────────────────────────────

func (h *Handler) issueSessionCookie(w http.ResponseWriter, email, role string) error {
	now := time.Now()
	claims := sessionClaims{
		Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   email,
			Issuer:    h.jwtIssuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(jwtCookieTTL)),
		},
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(h.jwtSecret)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    tok,
		Path:     "/",
		MaxAge:   cookieMaxAge,
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// validateSession — 쿠키에서 JWT 검증 후 (email, role) 반환
func (h *Handler) validateSession(r *http.Request) (email, role string, err error) {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return "", "", err
	}
	tok, err := jwt.ParseWithClaims(cookie.Value, &sessionClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return h.jwtSecret, nil
	})
	if err != nil || !tok.Valid {
		return "", "", fmt.Errorf("invalid session token")
	}
	claims, ok := tok.Claims.(*sessionClaims)
	if !ok {
		return "", "", fmt.Errorf("invalid claims type")
	}
	return claims.Subject, claims.Role, nil
}

// RequireSession — 세션이 없으면 /login으로 리다이렉트하는 미들웨어
func (h *Handler) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		email, role, err := h.validateSession(r)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		ctx := r.Context()
		ctx = setCtxEmail(ctx, email)
		ctx = setCtxRole(ctx, role)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ─── 페이지 핸들러 ─────────────────────────────────────────────────────────────

// HandleRoot — / 를 /mail/INBOX로 리다이렉트
func (h *Handler) HandleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/mail/INBOX", http.StatusSeeOther)
}

// HandleLoginPage — GET /login
func (h *Handler) HandleLoginPage(w http.ResponseWriter, r *http.Request) {
	// 이미 로그인 중이면 리다이렉트
	if _, _, err := h.validateSession(r); err == nil {
		http.Redirect(w, r, "/mail/INBOX", http.StatusSeeOther)
		return
	}
	h.renderLogin(w, "")
}

// HandleLoginSubmit — POST /login
func (h *Handler) HandleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderLogin(w, "잘못된 요청입니다.")
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	pass := r.FormValue("password")

	if email == "" || pass == "" {
		h.renderLogin(w, "이메일과 비밀번호를 입력하세요.")
		return
	}

	admin, err := h.store.GetAdminUserByEmail(r.Context(), email)
	if err != nil {
		h.renderLogin(w, "이메일 또는 비밀번호가 올바르지 않습니다.")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(pass)) != nil {
		h.renderLogin(w, "이메일 또는 비밀번호가 올바르지 않습니다.")
		return
	}

	if err := h.issueSessionCookie(w, admin.Email, admin.Role); err != nil {
		log.Printf("web: issue session cookie: %v", err)
		h.renderLogin(w, "서버 오류가 발생했습니다.")
		return
	}
	http.Redirect(w, r, "/mail/INBOX", http.StatusSeeOther)
}

// HandleLogout — POST /logout
func (h *Handler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	h.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// HandleMail — GET /mail/{folder} 또는 GET /mail/{folder}/{msgID}
func (h *Handler) HandleMail(w http.ResponseWriter, r *http.Request) {
	email := ctxEmailFrom(r)
	role := ctxRoleFrom(r)

	// 경로 파싱: /mail/{folder}[/{msgID}]
	path := strings.TrimPrefix(r.URL.Path, "/mail/")
	parts := strings.SplitN(path, "/", 2)
	folder := strings.ToUpper(parts[0])
	if folder == "" {
		folder = "INBOX"
	}
	msgID := ""
	if len(parts) == 2 {
		msgID = parts[1]
	}

	pd := &pageData{
		User:         &userInfo{Email: email, Role: role},
		Folders:      folders,
		ActiveFolder: folder,
		Flash:        r.URL.Query().Get("flash"),
		Query:        r.URL.Query().Get("q"),
	}

	if h.archive == nil {
		pd.Error = "아카이브 데이터베이스가 설정되지 않았습니다."
		h.renderMail(w, pd)
		return
	}

	// 메일박스 목록
	boxes, err := h.archive.ListMailboxes(r.Context(), email)
	if err != nil {
		log.Printf("web: list mailboxes: %v", err)
	}
	pd.Mailboxes = boxes

	// 현재 폴더 메일박스 찾기
	var activeBox *archive.Mailbox
	for i := range boxes {
		if strings.EqualFold(boxes[i].Name, folder) {
			activeBox = &boxes[i]
			break
		}
	}

	// 메시지 상세 보기
	if msgID != "" && activeBox != nil {
		msg, err := h.archive.GetMessage(r.Context(), msgID)
		if err != nil {
			pd.Error = "메시지를 찾을 수 없습니다."
		} else {
			pd.ActiveMessage = &msg
		}
		h.renderMail(w, pd)
		return
	}

	// 메시지 목록
	if activeBox != nil {
		limit := 100
		msgs, err := h.archive.ListMessages(r.Context(), activeBox.ID, limit)
		if err != nil {
			log.Printf("web: list messages: %v", err)
			pd.Error = "메시지 목록을 불러오지 못했습니다."
		} else {
			// 검색 필터
			if q := pd.Query; q != "" {
				q = strings.ToLower(q)
				var filtered []archive.Message
				for _, m := range msgs {
					if strings.Contains(strings.ToLower(m.FromAddr), q) ||
						strings.Contains(strings.ToLower(m.ToAddr), q) ||
						strings.Contains(strings.ToLower(m.Subject), q) ||
						strings.Contains(strings.ToLower(m.TextBody), q) {
						filtered = append(filtered, m)
					}
				}
				msgs = filtered
			}
			pd.Messages = msgs
		}
	}

	h.renderMail(w, pd)
}

// HandleCompose — POST /compose
func (h *Handler) HandleCompose(w http.ResponseWriter, r *http.Request) {
	email := ctxEmailFrom(r)

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/mail/SENT?flash=error", http.StatusSeeOther)
		return
	}

	to := strings.TrimSpace(r.FormValue("to"))
	subject := strings.TrimSpace(r.FormValue("subject"))
	body := r.FormValue("body")

	if to == "" || h.archive == nil {
		http.Redirect(w, r, "/mail/SENT?flash=error", http.StatusSeeOther)
		return
	}

	// SENT 메일박스 찾기 또는 생성
	boxes, _ := h.archive.ListMailboxes(r.Context(), email)
	var sentBox *archive.Mailbox
	for i := range boxes {
		if strings.EqualFold(boxes[i].Name, "SENT") {
			sentBox = &boxes[i]
			break
		}
	}
	if sentBox == nil {
		mb, err := h.archive.CreateMailbox(r.Context(), email, "SENT")
		if err != nil {
			log.Printf("web: create SENT mailbox: %v", err)
			http.Redirect(w, r, "/mail/INBOX?flash=error", http.StatusSeeOther)
			return
		}
		sentBox = &mb
	}

	rawMime := buildRawMime(email, to, subject, body)
	_, err := h.archive.CreateMessage(r.Context(), archive.CreateMessageInput{
		MailboxID: sentBox.ID,
		Direction: "outbound",
		FromAddr:  email,
		ToAddr:    to,
		Subject:   subject,
		TextBody:  body,
		RawMIME:   rawMime,
		SizeBytes: int64(len(rawMime)),
	})
	if err != nil {
		log.Printf("web: create message: %v", err)
		http.Redirect(w, r, "/mail/SENT?flash=error", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/mail/SENT?flash=sent", http.StatusSeeOther)
}

// ─── 렌더 헬퍼 ────────────────────────────────────────────────────────────────

func (h *Handler) renderLogin(w http.ResponseWriter, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if errMsg != "" {
		w.WriteHeader(http.StatusUnauthorized)
	}
	if err := h.tpl.ExecuteTemplate(w, "login.html", map[string]string{"Error": errMsg}); err != nil {
		log.Printf("web: render login: %v", err)
	}
}

func (h *Handler) renderMail(w http.ResponseWriter, pd *pageData) {
	// Flash 메시지
	switch pd.Flash {
	case "sent":
		pd.Flash = "메일을 보냈습니다."
	case "error":
		pd.Flash = "작업에 실패했습니다."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tpl.ExecuteTemplate(w, "mail.html", pd); err != nil {
		log.Printf("web: render mail: %v", err)
	}
}

// ─── 내부 유틸 ────────────────────────────────────────────────────────────────

func buildRawMime(from, to, subject, body string) string {
	date := time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 +0000")
	lines := []string{
		"From: " + from,
		"To: " + to,
		"Subject: " + subject,
		"Date: " + date,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: 8bit",
		"",
		body,
	}
	return strings.Join(lines, "\r\n")
}
