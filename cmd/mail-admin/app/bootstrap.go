package app

import (
	"context"
	"fmt"
	"log"
	"net/http"

	authsvc "github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/auth"
	handlersvc "github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/handlers"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/security"
)

func buildServices(s *server) (*authsvc.Service, *handlersvc.Service, error) {
	roleFromContext := func(ctx context.Context) string {
		v, ok := ctx.Value(authContextKey).(requestAuthInfo)
		if !ok {
			return ""
		}
		return v.Role
	}
	actorFromContext := func(ctx context.Context) string {
		v, ok := ctx.Value(authContextKey).(requestAuthInfo)
		if !ok {
			return ""
		}
		return security.ActorFromEmail(v.Email)
	}
	writeAudit := func(ctx context.Context, action, actor, email, status, message string, r *http.Request) {
		entry := security.BuildAuditLog(action, actor, email, status, message, r.RemoteAddr, r.UserAgent())
		if err := s.store.InsertAuditLog(ctx, entry); err != nil {
			log.Printf("audit log write failed: %v", err)
		}
	}

	authService := authsvc.NewService(authsvc.Dependencies{
		Store: s.store,
		Config: authsvc.Config{
			JWTSecret:               s.cfg.JWTSecret,
			JWTIssuer:               s.cfg.JWTIssuer,
			JWTExpiryMinutes:        s.cfg.JWTExpiryMinutes,
			RefreshTokenExpiryDays:  s.cfg.RefreshTokenExpiryDays,
			TOTPIssuer:              s.cfg.TOTPIssuer,
			TOTPChallengeExpiryMins: s.cfg.TOTPChallengeExpiryMins,
			BcryptCost:              s.cfg.BcryptCost,
			BootstrapAdminEmail:     s.cfg.BootstrapAdminEmail,
			BootstrapAdminPassword:  s.cfg.BootstrapAdminPassword,
			BootstrapAdminRole:      s.cfg.BootstrapAdminRole,
			LoginIPRateLimitPerMin:  s.cfg.LoginIPRateLimitPerMin,
			LoginFailThreshold:      s.cfg.LoginFailThreshold,
			LoginLockMinutes:        s.cfg.LoginLockMinutes,
		},
		GoogleOAuth2Cfg:    s.googleOAuth2Cfg,
		MicrosoftOAuth2Cfg: s.microsoftOAuth2Cfg,
		WriteAudit:         writeAudit,
		ActorFromContext:   actorFromContext,
		SetAuthContext: func(ctx context.Context, email, role string) context.Context {
			return context.WithValue(ctx, authContextKey, requestAuthInfo{Email: email, Role: role})
		},
		IsValidRole: security.IsValidRole,
	})

	handlerService := handlersvc.NewService(handlersvc.Dependencies{
		Store:   s.store,
		Archive: s.archive,
		Config: handlersvc.Config{
			DovecotUsersFile:        s.cfg.DovecotUsersFile,
			PostfixMailboxMapsFile:  s.cfg.PostfixMailboxMapsFile,
			PostfixDomainsFile:      s.cfg.PostfixDomainsFile,
			MailRoot:                s.cfg.MailRoot,
			MailUID:                 s.cfg.MailUID,
			MailGID:                 s.cfg.MailGID,
			BcryptCost:              s.cfg.BcryptCost,
			ArchiveAutoRouteEnabled: s.cfg.ArchiveAutoRouteEnabled,
			ArchiveInboundMailbox:   s.cfg.ArchiveInboundMailbox,
			ArchiveOutboundMailbox:  s.cfg.ArchiveOutboundMailbox,
			SMTPRelayAddr:           s.cfg.SMTPRelayAddr,
			SMTPUsername:            s.cfg.SMTPUsername,
			SMTPPassword:            s.cfg.SMTPPassword,
			SendRateLimitPerMin:     s.cfg.SendRateLimitPerMin,
		},
		WriteAudit:       writeAudit,
		ActorFromContext: actorFromContext,
		RoleFromContext:  roleFromContext,
	})

	if err := authService.EnsureBootstrapAdmin(context.Background()); err != nil {
		return nil, nil, fmt.Errorf("bootstrap admin error: %w", err)
	}
	if err := handlerService.EnsureInitialSync(context.Background()); err != nil {
		return nil, nil, err
	}

	return authService, handlerService, nil
}
