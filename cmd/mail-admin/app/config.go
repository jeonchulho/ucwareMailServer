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

func loadConfig() (config, error) {
	mailUID, err := envx.GetInt("MAIL_UID", 5000)
	if err != nil {
		return config{}, err
	}
	mailGID, err := envx.GetInt("MAIL_GID", 5000)
	if err != nil {
		return config{}, err
	}
	bcryptCost, err := envx.GetInt("BCRYPT_COST", bcrypt.DefaultCost)
	if err != nil {
		return config{}, err
	}
	jwtExpiry, err := envx.GetInt("JWT_EXPIRY_MINUTES", 60)
	if err != nil {
		return config{}, err
	}
	refreshExpiry, err := envx.GetInt("REFRESH_TOKEN_EXPIRY_DAYS", 30)
	if err != nil {
		return config{}, err
	}
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
		LMTPEnabled:                 lmtpEnabled,
		LMTPAddr:                    envx.Get("LMTP_ADDR", ":2525"),
		LMTPDomain:                  envx.Get("LMTP_DOMAIN", ""),
		POP3Enabled:                 pop3Enabled,
		POP3Addr:                    envx.Get("POP3_ADDR", ":110"),
		StaticDir:                   envx.Get("STATIC_DIR", "./front"),
	}, nil
}

func ensurePaths(cfg config) error {
	for _, p := range []string{cfg.DBPath, cfg.DovecotUsersFile, cfg.PostfixMailboxMapsFile, cfg.PostfixDomainsFile} {
		dir := filepath.Dir(p)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}
