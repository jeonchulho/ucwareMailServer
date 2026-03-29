package auth

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	authtypes "github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/auth/types"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/infra/httpx"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/infra/ratelimit"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/internal/store"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/security"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
)

type Config struct {
	JWTSecret               string
	JWTIssuer               string
	JWTExpiryMinutes        int
	RefreshTokenExpiryDays  int
	TOTPIssuer              string
	TOTPChallengeExpiryMins int
	BcryptCost              int
	BootstrapAdminEmail     string
	BootstrapAdminPassword  string
	BootstrapAdminRole      string
	LoginIPRateLimitPerMin  int
	LoginFailThreshold      int
	LoginLockMinutes        int
}

type Dependencies struct {
	Store              *store.SQLiteStore
	Config             Config
	GoogleOAuth2Cfg    *oauth2.Config
	MicrosoftOAuth2Cfg *oauth2.Config
	WriteAudit         func(ctx context.Context, action, actor, email, status, message string, r *http.Request)
	ActorFromContext   func(ctx context.Context) string
	SetAuthContext     func(ctx context.Context, email, role string) context.Context
	IsValidRole        func(role string) bool
}

type Service struct {
	store              *store.SQLiteStore
	cfg                Config
	googleOAuth2Cfg    *oauth2.Config
	microsoftOAuth2Cfg *oauth2.Config
	writeAuditFn       func(ctx context.Context, action, actor, email, status, message string, r *http.Request)
	actorFromContextFn func(ctx context.Context) string
	setAuthContextFn   func(ctx context.Context, email, role string) context.Context
	isValidRoleFn      func(role string) bool
	mu                 sync.RWMutex
	loginMu            sync.Mutex
	loginLimiter       *ratelimit.FixedWindow
	loginFailures      map[string]int
	loginLockedUntil   map[string]time.Time
}

type accessClaims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

func NewService(dep Dependencies) *Service {
	cfg := dep.Config
	if cfg.LoginIPRateLimitPerMin < 1 {
		cfg.LoginIPRateLimitPerMin = 30
	}
	if cfg.LoginFailThreshold < 1 {
		cfg.LoginFailThreshold = 5
	}
	if cfg.LoginLockMinutes < 1 {
		cfg.LoginLockMinutes = 15
	}

	return &Service{
		store:              dep.Store,
		cfg:                cfg,
		googleOAuth2Cfg:    dep.GoogleOAuth2Cfg,
		microsoftOAuth2Cfg: dep.MicrosoftOAuth2Cfg,
		writeAuditFn:       dep.WriteAudit,
		actorFromContextFn: dep.ActorFromContext,
		setAuthContextFn:   dep.SetAuthContext,
		isValidRoleFn:      dep.IsValidRole,
		loginLimiter:       ratelimit.NewFixedWindow(cfg.LoginIPRateLimitPerMin, time.Minute),
		loginFailures:      make(map[string]int),
		loginLockedUntil:   make(map[string]time.Time),
	}
}

func (s *Service) SetOAuth2Configs(google, microsoft *oauth2.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.googleOAuth2Cfg = google
	s.microsoftOAuth2Cfg = microsoft
}

func (s *Service) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	now := time.Now().UTC()
	clientIP := requestClientIP(r)
	if !s.loginLimiter.Allow(clientIP, now) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		s.writeAudit(r.Context(), "login", "", "", "failed", "rate limited", r)
		return
	}
	defer r.Body.Close()
	var req authtypes.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if s.isLoginLocked(req.Email, now) {
		http.Error(w, "account temporarily locked", http.StatusLocked)
		s.writeAudit(r.Context(), "login", req.Email, req.Email, "failed", "account locked", r)
		return
	}
	adminUser, err := s.store.GetAdminUserByEmail(r.Context(), req.Email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.registerLoginFailure(req.Email, now)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			s.writeAudit(r.Context(), "login", "", req.Email, "failed", "unknown account", r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(adminUser.PasswordHash), []byte(req.Password)); err != nil {
		s.registerLoginFailure(req.Email, now)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		s.writeAudit(r.Context(), "login", req.Email, req.Email, "failed", "wrong password", r)
		return
	}
	s.clearLoginFailures(req.Email)
	if adminUser.TOTPEnabled {
		challengeID, err := GenerateRefreshToken()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		expiry := time.Now().UTC().Add(time.Duration(s.cfg.TOTPChallengeExpiryMins) * time.Minute)
		if err := s.store.InsertTOTPChallenge(r.Context(), challengeID, adminUser.Email, expiry); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.writeAudit(r.Context(), "login", req.Email, req.Email, "totp_required", "awaiting TOTP", r)
		httpx.WriteJSON(w, http.StatusOK, authtypes.TOTPChallengeResponse{Status: "totp_required", ChallengeToken: challengeID})
		return
	}
	signed, expiresAt, err := s.issueAccessToken(adminUser, now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rawRT, rtHash, rtExpiry, err := s.IssueRefreshToken(now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.store.InsertRefreshToken(r.Context(), rtHash, adminUser.Email, rtExpiry); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeAudit(r.Context(), "login", req.Email, req.Email, "ok", "login success", r)
	httpx.WriteJSON(w, http.StatusOK, authtypes.LoginResponse{AccessToken: signed, RefreshToken: rawRT, TokenType: "Bearer", ExpiresAt: expiresAt, Role: adminUser.Role, Email: adminUser.Email})
}

func (s *Service) isLoginLocked(email string, now time.Time) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if !isValidEmail(email) {
		return false
	}
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	lockedUntil, ok := s.loginLockedUntil[email]
	if !ok {
		return false
	}
	if now.Before(lockedUntil) {
		return true
	}
	delete(s.loginLockedUntil, email)
	delete(s.loginFailures, email)
	return false
}

func (s *Service) registerLoginFailure(email string, now time.Time) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !isValidEmail(email) {
		return
	}
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	if until, ok := s.loginLockedUntil[email]; ok && now.Before(until) {
		return
	}
	s.loginFailures[email]++
	if s.loginFailures[email] >= s.cfg.LoginFailThreshold {
		s.loginLockedUntil[email] = now.Add(time.Duration(s.cfg.LoginLockMinutes) * time.Minute)
		s.loginFailures[email] = 0
	}
}

func (s *Service) clearLoginFailures(email string) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return
	}
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	delete(s.loginFailures, email)
	delete(s.loginLockedUntil, email)
}

func requestClientIP(r *http.Request) string {
	xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if xff != "" {
		first := strings.TrimSpace(strings.Split(xff, ",")[0])
		if first != "" {
			return first
		}
	}
	xri := strings.TrimSpace(r.Header.Get("X-Real-IP"))
	if xri != "" {
		return xri
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func (s *Service) HandleRefreshToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	var req authtypes.RefreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	req.RefreshToken = strings.TrimSpace(req.RefreshToken)
	if req.RefreshToken == "" {
		http.Error(w, "refreshToken is required", http.StatusBadRequest)
		return
	}
	rtHash := HashRefreshToken(req.RefreshToken)
	rt, err := s.store.GetRefreshToken(r.Context(), rtHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rt.Revoked || time.Now().UTC().After(rt.ExpiresAt) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	adminUser, err := s.store.GetAdminUserByEmail(r.Context(), rt.AdminEmail)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now().UTC()
	signed, expiresAt, err := s.issueAccessToken(adminUser, now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeAudit(r.Context(), "refresh_token", adminUser.Email, adminUser.Email, "ok", "token refreshed", r)
	httpx.WriteJSON(w, http.StatusOK, authtypes.LoginResponse{AccessToken: signed, TokenType: "Bearer", ExpiresAt: expiresAt, Role: adminUser.Role, Email: adminUser.Email})
}

func (s *Service) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	var req authtypes.RefreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	req.RefreshToken = strings.TrimSpace(req.RefreshToken)
	if req.RefreshToken != "" {
		rtHash := HashRefreshToken(req.RefreshToken)
		if err := s.store.RevokeRefreshToken(r.Context(), rtHash); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"result": "logged out"})
}

func (s *Service) HandleAdmins(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listAdmins(w, r)
	case http.MethodPost:
		s.createAdmin(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Service) HandleAdminByEmail(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, "/v1/auth/admins/")
	parts := strings.SplitN(tail, "/", 2)
	email := strings.ToLower(strings.TrimSpace(parts[0]))
	action := ""
	if len(parts) > 1 {
		action = strings.ToLower(strings.TrimSpace(parts[1]))
	}
	if email == "" {
		http.Error(w, "email is required", http.StatusBadRequest)
		return
	}
	switch {
	case r.Method == http.MethodDelete && action == "":
		s.deleteAdmin(w, r, email)
	case r.Method == http.MethodPatch && action == "role":
		s.changeAdminRole(w, r, email)
	case r.Method == http.MethodPost && action == "password":
		s.changeAdminPassword(w, r, email)
	default:
		http.NotFound(w, r)
	}
}

func (s *Service) HandleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	email := s.actorFromContext(r.Context())
	key, err := totp.Generate(totp.GenerateOpts{Issuer: s.cfg.TOTPIssuer, AccountName: email})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.store.SetTOTPSecret(r.Context(), email, key.Secret()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeAudit(r.Context(), "totp_setup", email, email, "ok", "secret generated", r)
	httpx.WriteJSON(w, http.StatusOK, authtypes.TOTPSetupResponse{Secret: key.Secret(), OTPAuth: key.URL()})
}

func (s *Service) HandleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	email := s.actorFromContext(r.Context())
	defer r.Body.Close()
	var req authtypes.TOTPVerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	adminUser, err := s.store.GetAdminUserByEmail(r.Context(), email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if adminUser.TOTPSecret == "" {
		http.Error(w, "call /v1/auth/totp/setup first", http.StatusBadRequest)
		return
	}
	if !totp.Validate(req.Code, adminUser.TOTPSecret) {
		http.Error(w, "invalid totp code", http.StatusUnauthorized)
		s.writeAudit(r.Context(), "totp_confirm", email, email, "failed", "wrong code", r)
		return
	}
	if err := s.store.EnableTOTP(r.Context(), email); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeAudit(r.Context(), "totp_confirm", email, email, "ok", "totp enabled", r)
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"result": "totp enabled"})
}

func (s *Service) HandleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	email := s.actorFromContext(r.Context())
	defer r.Body.Close()
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	adminUser, err := s.store.GetAdminUserByEmail(r.Context(), email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if adminUser.PasswordHash != "" {
		if err := bcrypt.CompareHashAndPassword([]byte(adminUser.PasswordHash), []byte(req.Password)); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			s.writeAudit(r.Context(), "totp_disable", email, email, "failed", "wrong password", r)
			return
		}
	}
	if err := s.store.DisableTOTP(r.Context(), email); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeAudit(r.Context(), "totp_disable", email, email, "ok", "totp disabled", r)
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"result": "totp disabled"})
}

func (s *Service) HandleTOTPChallenge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	var req authtypes.TOTPChallengeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	req.ChallengeToken = strings.TrimSpace(req.ChallengeToken)
	req.Code = strings.TrimSpace(req.Code)
	if req.ChallengeToken == "" || req.Code == "" {
		http.Error(w, "challengeToken and code are required", http.StatusBadRequest)
		return
	}
	challenge, err := s.store.UseTOTPChallenge(r.Context(), req.ChallengeToken)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if challenge.Used || time.Now().UTC().After(challenge.ExpiresAt) {
		http.Error(w, "challenge token expired or already used", http.StatusUnauthorized)
		s.writeAudit(r.Context(), "totp_challenge", challenge.AdminEmail, challenge.AdminEmail, "failed", "expired/used", r)
		return
	}
	adminUser, err := s.store.GetAdminUserByEmail(r.Context(), challenge.AdminEmail)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !totp.Validate(req.Code, adminUser.TOTPSecret) {
		http.Error(w, "invalid totp code", http.StatusUnauthorized)
		s.writeAudit(r.Context(), "totp_challenge", challenge.AdminEmail, challenge.AdminEmail, "failed", "wrong code", r)
		return
	}
	now := time.Now().UTC()
	accessToken, expiresAt, err := s.issueAccessToken(adminUser, now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rawRT, rtHash, rtExpiry, err := s.IssueRefreshToken(now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.store.InsertRefreshToken(r.Context(), rtHash, adminUser.Email, rtExpiry); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = s.store.PurgeExpiredTOTPChallenges(r.Context())
	s.writeAudit(r.Context(), "totp_challenge", adminUser.Email, adminUser.Email, "ok", "login success", r)
	httpx.WriteJSON(w, http.StatusOK, authtypes.LoginResponse{AccessToken: accessToken, RefreshToken: rawRT, TokenType: "Bearer", ExpiresAt: expiresAt, Role: adminUser.Role, Email: adminUser.Email})
}

func (s *Service) HandleOAuth2Start(provider string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		cfg := s.oauth2ConfigFor(provider)
		if cfg == nil {
			http.Error(w, provider+" OAuth2 is not configured", http.StatusNotImplemented)
			return
		}
		stateToken, err := GenerateRefreshToken()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		expiry := time.Now().UTC().Add(10 * time.Minute)
		if err := s.store.InsertOAuth2State(r.Context(), stateToken, provider, expiry); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		url := cfg.AuthCodeURL(stateToken, oauth2.AccessTypeOnline)
		http.Redirect(w, r, url, http.StatusFound)
	}
}

func (s *Service) HandleOAuth2Callback(provider string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		oauthCfg := s.oauth2ConfigFor(provider)
		if oauthCfg == nil {
			http.Error(w, provider+" OAuth2 is not configured", http.StatusNotImplemented)
			return
		}
		stateParam := r.URL.Query().Get("state")
		if stateParam == "" {
			http.Error(w, "missing state", http.StatusBadRequest)
			return
		}
		storedState, err := s.store.PopOAuth2State(r.Context(), stateParam)
		if err != nil {
			http.Error(w, "invalid or expired state", http.StatusUnauthorized)
			return
		}
		if storedState.Provider != provider || time.Now().UTC().After(storedState.ExpiresAt) {
			http.Error(w, "state mismatch or expired", http.StatusUnauthorized)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing code: "+r.URL.Query().Get("error"), http.StatusBadRequest)
			return
		}
		oauthToken, err := oauthCfg.Exchange(r.Context(), code)
		if err != nil {
			http.Error(w, "token exchange failed: "+err.Error(), http.StatusUnauthorized)
			s.writeAudit(r.Context(), "oauth2_callback", "", "", "failed", err.Error(), r)
			return
		}
		email, sub, err := s.fetchOAuth2UserInfo(r.Context(), provider, oauthCfg, oauthToken)
		if err != nil {
			http.Error(w, "userinfo failed: "+err.Error(), http.StatusUnauthorized)
			s.writeAudit(r.Context(), "oauth2_callback", "", "", "failed", err.Error(), r)
			return
		}
		adminUser, err := s.store.GetAdminUserByEmail(r.Context(), email)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				adminUser, err = s.store.UpsertAdminUserByOAuth2(r.Context(), email, provider, sub, security.RoleViewer)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		now := time.Now().UTC()
		accessToken, expiresAt, err := s.issueAccessToken(adminUser, now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rawRT, rtHash, rtExpiry, err := s.IssueRefreshToken(now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := s.store.InsertRefreshToken(r.Context(), rtHash, adminUser.Email, rtExpiry); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.writeAudit(r.Context(), "oauth2_login", email, email, "ok", provider+" login", r)
		httpx.WriteJSON(w, http.StatusOK, authtypes.LoginResponse{AccessToken: accessToken, RefreshToken: rawRT, TokenType: "Bearer", ExpiresAt: expiresAt, Role: adminUser.Role, Email: adminUser.Email})
	}
}

func (s *Service) WithAuth(next http.Handler, allowedRoles ...string) http.Handler {
	allow := make(map[string]struct{}, len(allowedRoles))
	for _, role := range allowedRoles {
		allow[role] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
		if authHeader == "" || !strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			s.writeAudit(r.Context(), "auth", "", "", "failed", "missing bearer token", r)
			return
		}
		tokenStr := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer"))
		tokenStr = strings.TrimSpace(strings.TrimPrefix(tokenStr, "bearer"))
		if tokenStr == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			s.writeAudit(r.Context(), "auth", "", "", "failed", "empty bearer token", r)
			return
		}
		claims := &accessClaims{}
		token, err := jwt.ParseWithClaims(tokenStr, claims, func(token *jwt.Token) (any, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method")
			}
			return []byte(s.cfg.JWTSecret), nil
		})
		if err != nil || !token.Valid {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			s.writeAudit(r.Context(), "auth", "", "", "failed", "invalid token", r)
			return
		}
		if claims.Issuer != s.cfg.JWTIssuer || claims.Subject == "" || !s.isValidRole(claims.Role) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			s.writeAudit(r.Context(), "auth", "", "", "failed", "invalid claims", r)
			return
		}
		if _, ok := allow[claims.Role]; !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			s.writeAudit(r.Context(), "auth", claims.Subject, "", "failed", "rbac denied", r)
			return
		}
		ctx := r.Context()
		if s.setAuthContextFn != nil {
			ctx = s.setAuthContextFn(ctx, claims.Subject, claims.Role)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Service) EnsureBootstrapAdmin(ctx context.Context) error {
	email := strings.TrimSpace(strings.ToLower(s.cfg.BootstrapAdminEmail))
	if !isValidEmail(email) {
		return fmt.Errorf("invalid BOOTSTRAP_ADMIN_EMAIL")
	}
	if len(s.cfg.BootstrapAdminPassword) < 10 {
		return fmt.Errorf("BOOTSTRAP_ADMIN_PASSWORD must be at least 10 chars")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(s.cfg.BootstrapAdminPassword), s.cfg.BcryptCost)
	if err != nil {
		return err
	}
	_, err = s.store.UpsertAdminUser(ctx, email, string(hash), s.cfg.BootstrapAdminRole)
	return err
}

func GenerateRefreshToken() (string, error) {
	b := make([]byte, 32)
	if _, err := crand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func HashRefreshToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

func (s *Service) IssueRefreshToken(now time.Time) (raw, hash string, expiry time.Time, err error) {
	raw, err = GenerateRefreshToken()
	if err != nil {
		return
	}
	hash = HashRefreshToken(raw)
	expiry = now.Add(time.Duration(s.cfg.RefreshTokenExpiryDays) * 24 * time.Hour)
	return
}

func (s *Service) issueAccessToken(adminUser store.AdminUser, now time.Time) (signed string, expiresAt time.Time, err error) {
	expiresAt = now.Add(time.Duration(s.cfg.JWTExpiryMinutes) * time.Minute)
	claims := accessClaims{Role: adminUser.Role, RegisteredClaims: jwt.RegisteredClaims{Issuer: s.cfg.JWTIssuer, Subject: adminUser.Email, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(expiresAt)}}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err = token.SignedString([]byte(s.cfg.JWTSecret))
	return
}

func (s *Service) listAdmins(w http.ResponseWriter, r *http.Request) {
	admins, err := s.store.ListAdminUsers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := make([]authtypes.AdminUserResponse, 0, len(admins))
	for _, a := range admins {
		resp = append(resp, authtypes.AdminUserResponse{Email: a.Email, Role: a.Role, CreatedAt: a.CreatedAt})
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (s *Service) createAdmin(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req authtypes.CreateAdminRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.Role = strings.ToLower(strings.TrimSpace(req.Role))
	if !isValidEmail(req.Email) {
		http.Error(w, "invalid email", http.StatusBadRequest)
		s.writeAudit(r.Context(), "create_admin", s.actorFromContext(r.Context()), req.Email, "failed", "invalid email", r)
		return
	}
	if !s.isValidRole(req.Role) {
		http.Error(w, "invalid role", http.StatusBadRequest)
		s.writeAudit(r.Context(), "create_admin", s.actorFromContext(r.Context()), req.Email, "failed", "invalid role", r)
		return
	}
	if len(req.Password) < 10 {
		http.Error(w, "password must be at least 10 characters", http.StatusBadRequest)
		s.writeAudit(r.Context(), "create_admin", s.actorFromContext(r.Context()), req.Email, "failed", "password too short", r)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), s.cfg.BcryptCost)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a, err := s.store.UpsertAdminUser(r.Context(), req.Email, string(hash), req.Role)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		s.writeAudit(r.Context(), "create_admin", s.actorFromContext(r.Context()), req.Email, "failed", err.Error(), r)
		return
	}
	s.writeAudit(r.Context(), "create_admin", s.actorFromContext(r.Context()), req.Email, "ok", "created", r)
	httpx.WriteJSON(w, http.StatusCreated, authtypes.AdminUserResponse{Email: a.Email, Role: a.Role, CreatedAt: a.CreatedAt})
}

func (s *Service) deleteAdmin(w http.ResponseWriter, r *http.Request, email string) {
	actor := s.actorFromContext(r.Context())
	if email == actor {
		http.Error(w, "cannot delete your own account", http.StatusBadRequest)
		s.writeAudit(r.Context(), "delete_admin", actor, email, "failed", "self-delete blocked", r)
		return
	}
	if !isValidEmail(email) {
		http.Error(w, "invalid email", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteAdminUser(r.Context(), email); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		s.writeAudit(r.Context(), "delete_admin", actor, email, "failed", err.Error(), r)
		return
	}
	s.writeAudit(r.Context(), "delete_admin", actor, email, "ok", "deleted", r)
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"result": "deleted"})
}

func (s *Service) changeAdminRole(w http.ResponseWriter, r *http.Request, email string) {
	actor := s.actorFromContext(r.Context())
	if !isValidEmail(email) {
		http.Error(w, "invalid email", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	var req authtypes.ChangeRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	req.Role = strings.ToLower(strings.TrimSpace(req.Role))
	if !s.isValidRole(req.Role) {
		http.Error(w, "invalid role", http.StatusBadRequest)
		s.writeAudit(r.Context(), "change_admin_role", actor, email, "failed", "invalid role", r)
		return
	}
	if err := s.store.UpdateAdminUserRole(r.Context(), email, req.Role); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		s.writeAudit(r.Context(), "change_admin_role", actor, email, "failed", err.Error(), r)
		return
	}
	s.writeAudit(r.Context(), "change_admin_role", actor, email, "ok", "role="+req.Role, r)
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"result": "role updated", "role": req.Role})
}

func (s *Service) changeAdminPassword(w http.ResponseWriter, r *http.Request, email string) {
	actor := s.actorFromContext(r.Context())
	if !isValidEmail(email) {
		http.Error(w, "invalid email", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	var req authtypes.ChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if len(req.Password) < 10 {
		http.Error(w, "password must be at least 10 characters", http.StatusBadRequest)
		s.writeAudit(r.Context(), "change_admin_password", actor, email, "failed", "password too short", r)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), s.cfg.BcryptCost)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.store.ChangeAdminPassword(r.Context(), email, string(hash)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		s.writeAudit(r.Context(), "change_admin_password", actor, email, "failed", err.Error(), r)
		return
	}
	_, _ = s.store.PurgeExpiredRefreshTokens(r.Context())
	s.writeAudit(r.Context(), "change_admin_password", actor, email, "ok", "password changed", r)
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"result": "password changed"})
}

func (s *Service) oauth2ConfigFor(provider string) *oauth2.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	switch provider {
	case "google":
		return s.googleOAuth2Cfg
	case "microsoft":
		return s.microsoftOAuth2Cfg
	default:
		return nil
	}
}

func (s *Service) fetchOAuth2UserInfo(ctx context.Context, provider string, cfg *oauth2.Config, token *oauth2.Token) (email, sub string, err error) {
	client := cfg.Client(ctx, token)
	var infoURL string
	switch provider {
	case "google":
		infoURL = "https://openidconnect.googleapis.com/v1/userinfo"
	case "microsoft":
		infoURL = "https://graph.microsoft.com/oidc/userinfo"
	default:
		return "", "", fmt.Errorf("unsupported provider: %s", provider)
	}
	resp, err := client.Get(infoURL)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("userinfo HTTP %d", resp.StatusCode)
	}
	var info struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", "", err
	}
	if info.Email == "" {
		return "", "", fmt.Errorf("email not returned by %s userinfo", provider)
	}
	return strings.ToLower(strings.TrimSpace(info.Email)), info.Sub, nil
}

func (s *Service) actorFromContext(ctx context.Context) string {
	if s.actorFromContextFn == nil {
		return ""
	}
	return s.actorFromContextFn(ctx)
}

func (s *Service) isValidRole(role string) bool {
	if s.isValidRoleFn == nil {
		return false
	}
	return s.isValidRoleFn(role)
}

func (s *Service) writeAudit(ctx context.Context, action, actor, email, status, message string, r *http.Request) {
	if s.writeAuditFn != nil {
		s.writeAuditFn(ctx, action, actor, email, status, message, r)
	}
}

func isValidEmail(value string) bool {
	addr, err := mail.ParseAddress(value)
	if err != nil {
		return false
	}
	return strings.EqualFold(addr.Address, value)
}
