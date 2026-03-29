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

// Config 는 인증 서비스에 필요한 전체 설정을 담습니다.
type Config struct {
	JWTSecret               string // JWT 서명에 사용하는 HS256 비밀키 (노출 금지)
	JWTIssuer               string // JWT iss 클레임 값 (예: "ucware-mail")
	JWTExpiryMinutes        int    // 액세스 토큰 유효 시간(분), 기본 15분 권장
	RefreshTokenExpiryDays  int    // 리프레시 토큰 유효 기간(일)
	TOTPIssuer              string // OTP 앱에 표시될 발급자 이름
	TOTPChallengeExpiryMins int    // TOTP 챌린지 토큰이 만료되기까지의 시간(분)
	BcryptCost              int    // bcrypt 해시 비용 (보안↑ = 느림, 최소 12 권장)
	BootstrapAdminEmail     string // 최초 실행 시 자동 생성할 관리자 이메일
	BootstrapAdminPassword  string // 최초 실행 시 자동 생성할 관리자 초기 비밀번호
	BootstrapAdminRole      string // 최초 관리자에게 부여할 역할 (기본: superadmin)
	LoginIPRateLimitPerMin  int    // IP 당 분당 허용 로그인 시도 횟수 (레이트리밋)
	LoginFailThreshold      int    // 연속 실패 N회 이후 계정 잠금 발동
	LoginLockMinutes        int    // 계정 잠금 지속 시간(분)
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

// Service 는 관리자 인증 전반(로그인, TOTP, OAuth2, JWT 발급/검증, RBAC)을 담당합니다.
type Service struct {
	store              *store.SQLiteStore // 관리자 계정·토큰·TOTP 상태를 저장하는 SQLite 저장소
	cfg                Config
	googleOAuth2Cfg    *oauth2.Config                                                                           // Google OAuth2 설정 (런타임 갱신 가능)
	microsoftOAuth2Cfg *oauth2.Config                                                                           // Microsoft OAuth2 설정 (런타임 갱신 가능)
	writeAuditFn       func(ctx context.Context, action, actor, email, status, message string, r *http.Request) // 감사 로그 기록 콜백
	actorFromContextFn func(ctx context.Context) string                                                         // 현재 요청의 행위자(이메일)를 컨텍스트에서 추출
	setAuthContextFn   func(ctx context.Context, email, role string) context.Context                            // 인증 정보를 컨텍스트에 삽입
	isValidRoleFn      func(role string) bool                                                                   // 역할값 유효성 검사 (superadmin/admin/viewer 등)
	mu                 sync.RWMutex                                                                             // OAuth2 설정 런타임 갱신 시 경쟁 방지용 RW 뮤텍스
	loginMu            sync.Mutex                                                                               // 로그인 실패 횟수·잠금 맵에 대한 단독 접근 보장
	loginLimiter       *ratelimit.FixedWindow                                                                   // IP 기준 분당 로그인 시도 횟수 제한기
	loginFailures      map[string]int                                                                           // 이메일 → 아직 누적된 연속 실패 횟수
	loginLockedUntil   map[string]time.Time                                                                     // 이메일 → 잠금 해제 시각 (잠금 중인 계정)
}

type accessClaims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

// NewService 는 인증 서비스를 초기화합니다.
// 설정값이 0 이하인 경우 안전한 기본값(레이트리밋 30rpm, 실패 5회, 잠금 15분)으로 보정합니다.
func NewService(dep Dependencies) *Service {
	cfg := dep.Config
	if cfg.LoginIPRateLimitPerMin < 1 {
		// 환경변수 미설정 시 분당 30회 허용
		cfg.LoginIPRateLimitPerMin = 30
	}
	if cfg.LoginFailThreshold < 1 {
		// 연속 실패 5회부터 잠금
		cfg.LoginFailThreshold = 5
	}
	if cfg.LoginLockMinutes < 1 {
		// 기본 잠금 지속 시간 15분
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

// HandleLogin 은 이메일/비밀번호 로그인 요청을 처리합니다.
// 처리 순서: IP 레이트리밋 → 계정 잠금 확인 → 비밀번호 검증 → TOTP 여부 분기 → JWT 발급
func (s *Service) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	now := time.Now().UTC()
	clientIP := requestClientIP(r)
	// IP 기준 분당 허용 횟수 초과 시 즉시 429 반환 (브루트포스 방어)
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
	// 연속 실패로 잠긴 계정인지 확인 (HTTP 423 Locked 반환)
	if s.isLoginLocked(req.Email, now) {
		http.Error(w, "account temporarily locked", http.StatusLocked)
		s.writeAudit(r.Context(), "login", req.Email, req.Email, "failed", "account locked", r)
		return
	}
	adminUser, err := s.store.GetAdminUserByEmail(r.Context(), req.Email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// 존재하지 않는 이메일도 실패 횟수에 포함 (계정 존재 여부 노출 방지)
			s.registerLoginFailure(req.Email, now)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			s.writeAudit(r.Context(), "login", "", req.Email, "failed", "unknown account", r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// bcrypt 타이밍 안전 비교 — 일치하지 않으면 실패 횟수 누적
	if err := bcrypt.CompareHashAndPassword([]byte(adminUser.PasswordHash), []byte(req.Password)); err != nil {
		s.registerLoginFailure(req.Email, now)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		s.writeAudit(r.Context(), "login", req.Email, req.Email, "failed", "wrong password", r)
		return
	}
	// 로그인 성공 시 실패 카운터 초기화
	s.clearLoginFailures(req.Email)
	// TOTP 활성화된 계정은 2단계 인증 챌린지 토큰을 발급하고 즉시 반환
	// 클라이언트는 challengeToken + OTP 코드로 /v1/auth/totp/challenge 를 추가 호출해야 함
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

// isLoginLocked 는 해당 이메일 계정이 현재 잠금 상태인지 확인합니다.
// 잠금 만료 시각이 지난 경우 맵에서 자동 제거(지연 소멸)합니다.
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
		return true // 아직 잠금 기간 중
	}
	// 잠금 만료 — 맵에서 제거하여 메모리 누수 방지
	delete(s.loginLockedUntil, email)
	delete(s.loginFailures, email)
	return false
}

// registerLoginFailure 는 로그인 실패 횟수를 1 증가시키고,
// 임계값(LoginFailThreshold) 도달 시 LoginLockMinutes 분 동안 계정을 잠급니다.
func (s *Service) registerLoginFailure(email string, now time.Time) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !isValidEmail(email) {
		return
	}
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	// 이미 잠금 중이면 카운터 증가 불필요 (잠금 연장도 하지 않음)
	if until, ok := s.loginLockedUntil[email]; ok && now.Before(until) {
		return
	}
	s.loginFailures[email]++
	if s.loginFailures[email] >= s.cfg.LoginFailThreshold {
		// 임계값 초과 → 잠금 기간 설정 후 카운터 초기화
		s.loginLockedUntil[email] = now.Add(time.Duration(s.cfg.LoginLockMinutes) * time.Minute)
		s.loginFailures[email] = 0
	}
}

// clearLoginFailures 는 로그인 성공 시 해당 이메일의 실패 기록과 잠금 상태를 모두 삭제합니다.
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

// requestClientIP 는 프록시 체인을 고려하여 실제 클라이언트 IP를 추출합니다.
// 우선순위: X-Forwarded-For 첫 번째 IP → X-Real-IP → RemoteAddr
func requestClientIP(r *http.Request) string {
	// 리버스 프록시(nginx, ALB 등)가 추가하는 헤더에서 원본 IP 추출
	xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if xff != "" {
		// 다중 프록시 체인의 경우 가장 왼쪽(첫 번째)이 클라이언트 IP
		first := strings.TrimSpace(strings.Split(xff, ",")[0])
		if first != "" {
			return first
		}
	}
	xri := strings.TrimSpace(r.Header.Get("X-Real-IP"))
	if xri != "" {
		return xri
	}
	// 프록시 없이 직접 연결된 경우 RemoteAddr 파싱 (host:port 형식)
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

// HandleRefreshToken 은 유효한 리프레시 토큰을 받아 새 액세스 토큰을 발급합니다.
// 리프레시 토큰은 DB에 해시로 저장되며, 만료·철회 여부를 검사합니다.
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

// HandleLogout 은 리프레시 토큰을 DB에서 철회하여 세션을 무효화합니다.
// 토큰 없이 호출해도 정상 응답(멱등성 보장)합니다.
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

// HandleTOTPSetup 은 로그인한 관리자에게 TOTP 시크릿을 생성하고 QR 코드 URL을 반환합니다.
// 이후 HandleTOTPConfirm 으로 OTP를 한 번 검증해야 TOTP가 활성화됩니다.
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

// HandleTOTPConfirm 은 관리자가 OTP 앱에 등록 후 첫 코드를 검증하여 TOTP를 활성화합니다.
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

// HandleTOTPDisable 은 현재 비밀번호 재확인 후 TOTP를 비활성화합니다.
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

// HandleTOTPChallenge 는 비밀번호 검증 후 발급된 챌린지 토큰과 OTP 코드를 검증하고
// 최종 JWT 액세스 토큰과 리프레시 토큰을 발급합니다 (2단계 인증 완료).
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
	// 로그인 성공 시 만료된 챌린지 레코드를 정리 (에러는 무시)
	_ = s.store.PurgeExpiredTOTPChallenges(r.Context())
	s.writeAudit(r.Context(), "totp_challenge", adminUser.Email, adminUser.Email, "ok", "login success", r)
	httpx.WriteJSON(w, http.StatusOK, authtypes.LoginResponse{AccessToken: accessToken, RefreshToken: rawRT, TokenType: "Bearer", ExpiresAt: expiresAt, Role: adminUser.Role, Email: adminUser.Email})
}

// HandleOAuth2Start 는 OAuth2 인증 흐름을 시작합니다.
// CSRF 방어를 위해 랜덤 state 토큰을 생성·DB 저장 후 provider 인증 페이지로 리다이렉트합니다.
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

// HandleOAuth2Callback 은 provider가 리다이렉트한 요청을 처리합니다.
// state 검증 → 인증 코드 교환 → userinfo 조회 → 관리자 upsert → JWT 발급 순으로 진행합니다.
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

// WithAuth 는 JWT 검증 및 RBAC 역할 확인을 수행하는 HTTP 미들웨어를 반환합니다.
// allowedRoles에 포함된 역할만 통과하며, 검증 실패 시 감사 로그를 기록합니다.
func (s *Service) WithAuth(next http.Handler, allowedRoles ...string) http.Handler {
	// 역할 목록을 O(1) 조회용 맵으로 변환
	allow := make(map[string]struct{}, len(allowedRoles))
	for _, role := range allowedRoles {
		allow[role] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
		// Authorization: Bearer <token> 형식 필수
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
		// HS256 서명 방식만 허용 (알고리즘 혼용 공격 방어)
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
		// iss·sub 클레임 및 역할 유효성을 추가 검증
		if claims.Issuer != s.cfg.JWTIssuer || claims.Subject == "" || !s.isValidRole(claims.Role) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			s.writeAudit(r.Context(), "auth", "", "", "failed", "invalid claims", r)
			return
		}
		// RBAC: 허용 역할 목록에 없으면 403 Forbidden
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

// EnsureBootstrapAdmin 은 애플리케이션 최초 구동 시 초기 관리자 계정을 생성합니다.
// 이미 계정이 존재하면 Upsert로 비밀번호만 갱신합니다.
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

// GenerateRefreshToken 은 암호학적으로 안전한 32바이트 난수를 hex 인코딩하여 반환합니다.
// DB에는 이 값의 SHA-256 해시만 저장하여 토큰 탈취 시 재사용을 방지합니다.
func GenerateRefreshToken() (string, error) {
	b := make([]byte, 32)
	if _, err := crand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// HashRefreshToken 은 원본 리프레시 토큰의 SHA-256 해시를 반환합니다.
// DB 저장용으로, 원본 토큰은 클라이언트에만 존재합니다.
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

// issueAccessToken 은 관리자 정보를 담은 HS256 서명 JWT 액세스 토큰을 생성합니다.
// 토큰에는 Role, Issuer, Subject(이메일), 발급시각, 만료시각이 포함됩니다.
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

// isValidEmail 은 RFC 5322 파서를 사용하여 이메일 주소 유효성을 검사합니다.
// "Display <addr>" 형식의 입력은 거부하고 순수 이메일 주소만 허용합니다.
func isValidEmail(value string) bool {
	addr, err := mail.ParseAddress(value)
	if err != nil {
		return false
	}
	return strings.EqualFold(addr.Address, value)
}
