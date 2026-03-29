package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/infra/envx"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/security"
	"golang.org/x/crypto/bcrypt"
)

// loadConfig 는 환경변수에서 모든 설정을 읽어 config 구조체로 반환합니다.
// 필수 값이 없거나 하한을 위반하면 오류를 반환하여 애플리케이션 구동을 중단합니다.
func loadConfig() (config, error) {
	// vmail 파일 소유자 UID/GID — Dovecot과 동일한 값으로 맞춰야 권한 오류가 없음
	mailUID, err := envx.GetInt("MAIL_UID", 5000)
	if err != nil {
		return config{}, err
	}
	mailGID, err := envx.GetInt("MAIL_GID", 5000)
	if err != nil {
		return config{}, err
	}
	// bcrypt 비용 — DefaultCost(10)보다 높을수록 보안 향상 (12~14 권장, 성능과 트레이드오프)
	bcryptCost, err := envx.GetInt("BCRYPT_COST", bcrypt.DefaultCost)
	if err != nil {
		return config{}, err
	}
	// JWT 액세스 토큰 유효 시간(분) — 짧을수록 보안 향상 (기본 60분)
	jwtExpiry, err := envx.GetInt("JWT_EXPIRY_MINUTES", 60)
	if err != nil {
		return config{}, err
	}
	refreshExpiry, err := envx.GetInt("REFRESH_TOKEN_EXPIRY_DAYS", 30)
	if err != nil {
		return config{}, err
	}
	// IP 기준 분당 로그인 허용 횟수 — 1 미만이면 기동 거부
	loginIPRateLimitPerMin, err := envx.GetInt("LOGIN_IP_RATE_LIMIT_PER_MIN", 30)
	if err != nil {
		return config{}, err
	}
	if loginIPRateLimitPerMin < 1 {
		return config{}, fmt.Errorf("LOGIN_IP_RATE_LIMIT_PER_MIN must be >= 1")
	}
	// 연속 로그인 실패 임계값 — 이 횟수 초과 시 계정 잠금 발동
	loginFailThreshold, err := envx.GetInt("LOGIN_FAIL_THRESHOLD", 5)
	if err != nil {
		return config{}, err
	}
	if loginFailThreshold < 1 {
		return config{}, fmt.Errorf("LOGIN_FAIL_THRESHOLD must be >= 1")
	}
	// 계정 잠금 지속 시간(분) — 잠금 중 추가 시도는 모두 거부됨
	loginLockMinutes, err := envx.GetInt("LOGIN_LOCK_MINUTES", 15)
	if err != nil {
		return config{}, err
	}
	if loginLockMinutes < 1 {
		return config{}, fmt.Errorf("LOGIN_LOCK_MINUTES must be >= 1")
	}
	// 메일 발송 API 분당 허용 횟수 (actor 또는 IP 기준)
	sendRateLimitPerMin, err := envx.GetInt("SEND_RATE_LIMIT_PER_MIN", 60)
	if err != nil {
		return config{}, err
	}
	if sendRateLimitPerMin < 1 {
		return config{}, fmt.Errorf("SEND_RATE_LIMIT_PER_MIN must be >= 1")
	}
	// JWT_SECRET 이 없으면 기동 거부 (토큰 서명 불가)
	jwtSecret := envx.Get("JWT_SECRET", "")
	totpChallengeExpiry, err := envx.GetInt("TOTP_CHALLENGE_EXPIRY_MINUTES", 5)
	if err != nil {
		return config{}, err
	}
	archiveEnabled, err := envx.GetBool("ARCHIVE_DB_ENABLED", false)
	if err != nil {
		return config{}, err
	}
	autoRouteEnabled, err := envx.GetBool("ARCHIVE_AUTO_ROUTE_ENABLED", false)
	if err != nil {
		return config{}, err
	}
	lmtpEnabled, err := envx.GetBool("LMTP_ENABLED", false)
	if err != nil {
		return config{}, err
	}
	pop3Enabled, err := envx.GetBool("POP3_ENABLED", false)
	if err != nil {
		return config{}, err
	}
	lmtpMaxMessageBytes, err := envx.GetInt("LMTP_MAX_MESSAGE_BYTES", 200*1024*1024)
	if err != nil {
		return config{}, err
	}
	if lmtpMaxMessageBytes < 1024*1024 {
		return config{}, fmt.Errorf("LMTP_MAX_MESSAGE_BYTES must be >= 1048576")
	}
	inboundMailbox := strings.TrimSpace(envx.Get("ARCHIVE_INBOUND_MAILBOX", "INBOX"))
	if inboundMailbox == "" {
		return config{}, fmt.Errorf("ARCHIVE_INBOUND_MAILBOX cannot be empty")
	}
	outboundMailbox := strings.TrimSpace(envx.Get("ARCHIVE_OUTBOUND_MAILBOX", "SENT"))
	if outboundMailbox == "" {
		return config{}, fmt.Errorf("ARCHIVE_OUTBOUND_MAILBOX cannot be empty")
	}
	if jwtSecret == "" {
		return config{}, fmt.Errorf("JWT_SECRET is required")
	}
	// 부트스트랩 관리자 역할은 security.IsValidRole 로 사전 검증
	bootstrapRole := strings.ToLower(envx.Get("BOOTSTRAP_ADMIN_ROLE", security.RoleAdmin))
	if !security.IsValidRole(bootstrapRole) {
		return config{}, fmt.Errorf("invalid BOOTSTRAP_ADMIN_ROLE: %s", bootstrapRole)
	}

	return config{
		Addr:                        envx.Get("ADDR", ":8080"),
		JWTSecret:                   jwtSecret,
		JWTIssuer:                   envx.Get("JWT_ISSUER", "ucware-mail-admin"),
		JWTExpiryMinutes:            jwtExpiry,
		RefreshTokenExpiryDays:      refreshExpiry,
		BootstrapAdminEmail:         strings.ToLower(envx.Get("BOOTSTRAP_ADMIN_EMAIL", "admin@example.com")),
		BootstrapAdminPassword:      envx.Get("BOOTSTRAP_ADMIN_PASSWORD", "ChangeMeAdmin!123"),
		BootstrapAdminRole:          bootstrapRole,
		DBPath:                      envx.Get("DB_PATH", "./data/mailadmin.db"),
		DovecotUsersFile:            envx.Get("DOVECOT_USERS_FILE", "./generated/dovecot/users.passwd"),
		PostfixMailboxMapsFile:      envx.Get("POSTFIX_MAILBOX_MAPS_FILE", "./generated/postfix/virtual_mailbox_maps"),
		PostfixDomainsFile:          envx.Get("POSTFIX_DOMAINS_FILE", "./generated/postfix/virtual_mailbox_domains"),
		MailRoot:                    envx.Get("MAIL_ROOT", "/var/mail/vhosts"),
		MailUID:                     mailUID,
		MailGID:                     mailGID,
		BcryptCost:                  bcryptCost,
		OAuth2CallbackBase:          envx.Get("OAUTH2_CALLBACK_BASE", "http://localhost:8080"),
		GoogleOAuth2ClientID:        envx.Get("GOOGLE_OAUTH2_CLIENT_ID", ""),
		GoogleOAuth2ClientSecret:    envx.Get("GOOGLE_OAUTH2_CLIENT_SECRET", ""),
		MicrosoftOAuth2ClientID:     envx.Get("MICROSOFT_OAUTH2_CLIENT_ID", ""),
		MicrosoftOAuth2ClientSecret: envx.Get("MICROSOFT_OAUTH2_CLIENT_SECRET", ""),
		MicrosoftTenant:             envx.Get("MICROSOFT_TENANT", "common"),
		TOTPIssuer:                  envx.Get("TOTP_ISSUER", "ucware-mail-admin"),
		TOTPChallengeExpiryMins:     totpChallengeExpiry,
		ArchiveDBEnabled:            archiveEnabled,
		ArchiveDBDriver:             strings.ToLower(envx.Get("ARCHIVE_DB_DRIVER", "postgres")),
		ArchiveDSN:                  envx.Get("ARCHIVE_DSN", ""),
		ArchiveAutoRouteEnabled:     autoRouteEnabled,
		ArchiveInboundMailbox:       inboundMailbox,
		ArchiveOutboundMailbox:      outboundMailbox,
		SMTPRelayAddr:               envx.Get("SMTP_RELAY_ADDR", "postfix:587"),
		SMTPUsername:                envx.Get("SMTP_USERNAME", ""),
		SMTPPassword:                envx.Get("SMTP_PASSWORD", ""),
		LoginIPRateLimitPerMin:      loginIPRateLimitPerMin,
		LoginFailThreshold:          loginFailThreshold,
		LoginLockMinutes:            loginLockMinutes,
		SendRateLimitPerMin:         sendRateLimitPerMin,
		LMTPEnabled:                 lmtpEnabled,
		LMTPAddr:                    envx.Get("LMTP_ADDR", ":2525"),
		LMTPDomain:                  envx.Get("LMTP_DOMAIN", ""),
		LMTPMaxMessageBytes:         lmtpMaxMessageBytes,
		POP3Enabled:                 pop3Enabled,
		POP3Addr:                    envx.Get("POP3_ADDR", ":110"),
	}, nil
}

// ensurePaths 는 설정에서 참조하는 파일들의 상위 디렉터리가 존재하도록 보장합니다.
// DB 파일, Dovecot·Postfix 생성 파일 경로에 대해 os.MkdirAll을 수행합니다.
func ensurePaths(cfg config) error {
	for _, p := range []string{cfg.DBPath, cfg.DovecotUsersFile, cfg.PostfixMailboxMapsFile, cfg.PostfixDomainsFile} {
		dir := filepath.Dir(p)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}
