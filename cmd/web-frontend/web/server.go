package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

//go:embed templates/* static/*
var embedFS embed.FS

type Config struct {
	Addr         string
	APIBaseURL   string
	SecureCookie bool
}

type Server struct {
	cfg    Config
	client *http.Client
}

type folderDef struct {
	Key   string
	Label string
	Icon  string
}

var folders = []folderDef{
	{Key: "INBOX", Label: "받은편지함", Icon: "📥"},
	{Key: "SENT", Label: "보낸편지함", Icon: "📤"},
	{Key: "IMPORTANT", Label: "중요편지함", Icon: "🏷️"},
	{Key: "SPAM", Label: "스팸함", Icon: "🚫"},
	{Key: "TRASH", Label: "휴지통", Icon: "🗑️"},
}

type loginResponse struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Role         string    `json:"role"`
	Email        string    `json:"email"`
}

type mailbox struct {
	ID        string    `json:"id"`
	UserEmail string    `json:"userEmail"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

type message struct {
	ID         string    `json:"id"`
	MailboxID  string    `json:"mailboxId"`
	Direction  string    `json:"direction"`
	FromAddr   string    `json:"fromAddr"`
	ToAddr     string    `json:"toAddr"`
	Subject    string    `json:"subject"`
	RawMIME    string    `json:"rawMime"`
	TextBody   string    `json:"textBody"`
	SizeBytes  int64     `json:"sizeBytes"`
	ReceivedAt time.Time `json:"receivedAt"`
	CreatedAt  time.Time `json:"createdAt"`
}

type mailPageData struct {
	User struct {
		Email string
		Role  string
	}
	Folders        []folderDef
	ExtraMailboxes []mailbox
	ActiveFolder   string
	Messages       []message
	ActiveMessage  *message
	Flash          string
	Error          string
	Query          string
}

const (
	cookieAccess  = "ucw_web_access"
	cookieRefresh = "ucw_web_refresh"
	cookieEmail   = "ucw_web_email"
	cookieRole    = "ucw_web_role"
)

func Run(cfg Config) error {
	s := &Server{
		cfg:    cfg,
		client: &http.Client{Timeout: 15 * time.Second},
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

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
			return t.Format("2006-01-02 15:04")
		},
		"initial": func(addr string) string {
			if addr == "" {
				return "?"
			}
			return strings.ToUpper(string([]rune(addr)[0]))
		},
		"shortAddr": func(addr string) string {
			if parsed, err := mail.ParseAddress(addr); err == nil {
				if parsed.Name != "" {
					return parsed.Name
				}
				return parsed.Address
			}
			return addr
		},
		"preview": func(body string) string {
			normalized := strings.Join(strings.Fields(strings.ReplaceAll(body, "\n", " ")), " ")
			r := []rune(normalized)
			if len(r) > 80 {
				return string(r[:80]) + "…"
			}
			return normalized
		},
	}

	tpl, err := template.New("").Funcs(funcMap).ParseFS(embedFS, "templates/*.html")
	if err != nil {
		return err
	}
	r.SetHTMLTemplate(tpl)

	staticFS, err := fs.Sub(embedFS, "static")
	if err != nil {
		return err
	}
	r.StaticFS("/static", http.FS(staticFS))

	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.GET("/login", s.handleLoginPage)
	r.POST("/login", s.handleLoginSubmit)
	r.POST("/logout", s.requireAuth(), s.handleLogout)
	r.GET("/", s.requireAuth(), func(c *gin.Context) { c.Redirect(http.StatusSeeOther, "/mail/INBOX") })
	r.GET("/mail/:folder", s.requireAuth(), s.handleMail)
	r.GET("/mail/:folder/:id", s.requireAuth(), s.handleMail)
	r.POST("/compose", s.requireAuth(), s.handleCompose)

	return r.Run(cfg.Addr)
}

func (s *Server) requireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, err := c.Cookie(cookieAccess); err != nil {
			c.Redirect(http.StatusSeeOther, "/login")
			c.Abort()
			return
		}
		c.Next()
	}
}

func (s *Server) handleLoginPage(c *gin.Context) {
	if _, err := c.Cookie(cookieAccess); err == nil {
		c.Redirect(http.StatusSeeOther, "/mail/INBOX")
		return
	}
	c.HTML(http.StatusOK, "login.html", gin.H{"Error": ""})
}

func (s *Server) handleLoginSubmit(c *gin.Context) {
	email := strings.TrimSpace(c.PostForm("email"))
	password := c.PostForm("password")
	if email == "" || password == "" {
		c.HTML(http.StatusBadRequest, "login.html", gin.H{"Error": "이메일과 비밀번호를 입력하세요."})
		return
	}

	var resp loginResponse
	if err := s.apiJSON(c, http.MethodPost, "/v1/auth/login", gin.H{"email": email, "password": password}, &resp, false); err != nil {
		c.HTML(http.StatusUnauthorized, "login.html", gin.H{"Error": "이메일 또는 비밀번호가 올바르지 않습니다."})
		return
	}

	s.setAuthCookies(c, resp)
	c.Redirect(http.StatusSeeOther, "/mail/INBOX")
}

func (s *Server) handleLogout(c *gin.Context) {
	_ = s.apiJSON(c, http.MethodPost, "/v1/auth/logout", nil, nil, true)
	s.clearAuthCookies(c)
	c.Redirect(http.StatusSeeOther, "/login")
}

func (s *Server) handleMail(c *gin.Context) {
	email, _ := c.Cookie(cookieEmail)
	role, _ := c.Cookie(cookieRole)
	folder := strings.ToUpper(strings.TrimSpace(c.Param("folder")))
	if folder == "" {
		folder = "INBOX"
	}
	msgID := strings.TrimSpace(c.Param("id"))
	query := strings.TrimSpace(c.Query("q"))

	var boxes []mailbox
	if err := s.apiJSON(c, http.MethodGet, "/v1/mailboxes?userEmail="+url.QueryEscape(email), nil, &boxes, true); err != nil {
		c.HTML(http.StatusOK, "mail.html", gin.H{"Error": "메일박스를 불러오지 못했습니다.", "Folders": folders, "ActiveFolder": folder})
		return
	}

	activeMailboxID := ""
	known := map[string]bool{}
	for _, f := range folders {
		known[strings.ToUpper(f.Key)] = true
	}
	extra := make([]mailbox, 0)
	for _, b := range boxes {
		name := strings.ToUpper(b.Name)
		if name == folder {
			activeMailboxID = b.ID
		}
		if !known[name] {
			extra = append(extra, b)
		}
	}

	msgs := make([]message, 0)
	if activeMailboxID != "" {
		path := "/v1/messages?mailboxId=" + url.QueryEscape(activeMailboxID) + "&limit=100"
		if err := s.apiJSON(c, http.MethodGet, path, nil, &msgs, true); err != nil {
			c.HTML(http.StatusOK, "mail.html", gin.H{"Error": "메시지를 불러오지 못했습니다.", "Folders": folders, "ActiveFolder": folder})
			return
		}
	}

	if query != "" {
		q := strings.ToLower(query)
		filtered := make([]message, 0, len(msgs))
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

	var active *message
	if msgID != "" {
		for i := range msgs {
			if msgs[i].ID == msgID {
				active = &msgs[i]
				break
			}
		}
	}

	pd := mailPageData{
		Folders:        folders,
		ExtraMailboxes: extra,
		ActiveFolder:   folder,
		Messages:       msgs,
		ActiveMessage:  active,
		Flash:          strings.TrimSpace(c.Query("flash")),
		Query:          query,
	}
	pd.User.Email = email
	pd.User.Role = role
	if pd.Flash == "sent" {
		pd.Flash = "메일을 보냈습니다."
	} else if pd.Flash == "error" {
		pd.Flash = "작업에 실패했습니다."
	}

	c.HTML(http.StatusOK, "mail.html", pd)
}

func (s *Server) handleCompose(c *gin.Context) {
	email, _ := c.Cookie(cookieEmail)
	to := strings.TrimSpace(c.PostForm("to"))
	subject := strings.TrimSpace(c.PostForm("subject"))
	body := c.PostForm("body")
	if to == "" {
		c.Redirect(http.StatusSeeOther, "/mail/SENT?flash=error")
		return
	}

	var boxes []mailbox
	if err := s.apiJSON(c, http.MethodGet, "/v1/mailboxes?userEmail="+url.QueryEscape(email), nil, &boxes, true); err != nil {
		c.Redirect(http.StatusSeeOther, "/mail/SENT?flash=error")
		return
	}

	sentID := ""
	for _, b := range boxes {
		if strings.EqualFold(b.Name, "SENT") {
			sentID = b.ID
			break
		}
	}
	if sentID == "" {
		var mb mailbox
		if err := s.apiJSON(c, http.MethodPost, "/v1/mailboxes", gin.H{"userEmail": email, "name": "SENT"}, &mb, true); err != nil {
			c.Redirect(http.StatusSeeOther, "/mail/SENT?flash=error")
			return
		}
		sentID = mb.ID
	}

	raw := buildRawMIME(email, to, subject, body)
	err := s.apiJSON(c, http.MethodPost, "/v1/messages", gin.H{
		"mailboxId": sentID,
		"direction": "outbound",
		"fromAddr":  email,
		"toAddr":    to,
		"subject":   subject,
		"textBody":  body,
		"rawMime":   raw,
	}, nil, true)
	if err != nil {
		c.Redirect(http.StatusSeeOther, "/mail/SENT?flash=error")
		return
	}

	c.Redirect(http.StatusSeeOther, "/mail/SENT?flash=sent")
}

func (s *Server) apiJSON(c *gin.Context, method, path string, payload any, out any, auth bool) error {
	doReq := func(accessToken string) (*http.Response, error) {
		var body io.Reader
		if payload != nil {
			b, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			body = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(c.Request.Context(), method, strings.TrimRight(s.cfg.APIBaseURL, "/")+path, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if auth && accessToken != "" {
			req.Header.Set("Authorization", "Bearer "+accessToken)
		}
		return s.client.Do(req)
	}

	access, _ := c.Cookie(cookieAccess)
	res, err := doReq(access)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if auth && res.StatusCode == http.StatusUnauthorized {
		newAccess, refreshErr := s.refreshTokens(c)
		if refreshErr == nil {
			res, err = doReq(newAccess)
			if err != nil {
				return err
			}
			defer res.Body.Close()
		}
	}

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		b, _ := io.ReadAll(res.Body)
		return fmt.Errorf("api status=%d body=%s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}

func (s *Server) refreshTokens(c *gin.Context) (string, error) {
	refresh, err := c.Cookie(cookieRefresh)
	if err != nil || refresh == "" {
		return "", fmt.Errorf("no refresh token")
	}
	var resp loginResponse
	if err := s.apiJSON(c, http.MethodPost, "/v1/auth/refresh", gin.H{"refreshToken": refresh}, &resp, false); err != nil {
		return "", err
	}
	s.setAuthCookies(c, resp)
	return resp.AccessToken, nil
}

func (s *Server) setAuthCookies(c *gin.Context, token loginResponse) {
	maxAge := int(time.Until(token.ExpiresAt).Seconds())
	if maxAge <= 0 {
		maxAge = 3600
	}
	c.SetCookie(cookieAccess, token.AccessToken, maxAge, "/", "", s.cfg.SecureCookie, true)
	c.SetCookie(cookieRefresh, token.RefreshToken, 60*60*24*30, "/", "", s.cfg.SecureCookie, true)
	c.SetCookie(cookieEmail, token.Email, 60*60*24*30, "/", "", s.cfg.SecureCookie, true)
	c.SetCookie(cookieRole, token.Role, 60*60*24*30, "/", "", s.cfg.SecureCookie, true)
}

func (s *Server) clearAuthCookies(c *gin.Context) {
	for _, k := range []string{cookieAccess, cookieRefresh, cookieEmail, cookieRole} {
		c.SetCookie(k, "", -1, "/", "", s.cfg.SecureCookie, true)
	}
}

func buildRawMIME(from, to, subject, body string) string {
	date := time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 +0000")
	return strings.Join([]string{
		"From: " + from,
		"To: " + to,
		"Subject: " + subject,
		"Date: " + date,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: 8bit",
		"",
		body,
	}, "\r\n")
}
