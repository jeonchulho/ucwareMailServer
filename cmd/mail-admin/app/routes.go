package app

import (
	"net/http"

	authsvc "github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/auth"
	handlersvc "github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/handlers"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/security"
)

func buildMux(authService *authsvc.Service, handlerService *handlersvc.Service) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handlerService.HandleHealthz)
	mux.HandleFunc("/v1/auth/login", authService.HandleLogin)
	mux.HandleFunc("/v1/auth/refresh", authService.HandleRefreshToken)
	mux.HandleFunc("/v1/auth/logout", authService.HandleLogout)
	mux.HandleFunc("/v1/auth/oauth2/google", authService.HandleOAuth2Start("google"))
	mux.HandleFunc("/v1/auth/oauth2/google/callback", authService.HandleOAuth2Callback("google"))
	mux.HandleFunc("/v1/auth/oauth2/microsoft", authService.HandleOAuth2Start("microsoft"))
	mux.HandleFunc("/v1/auth/oauth2/microsoft/callback", authService.HandleOAuth2Callback("microsoft"))
	mux.HandleFunc("/v1/auth/totp/challenge", authService.HandleTOTPChallenge)
	mux.Handle("/v1/auth/totp/setup", authService.WithAuth(http.HandlerFunc(authService.HandleTOTPSetup), security.RoleAdmin, security.RoleOperator, security.RoleViewer))
	mux.Handle("/v1/auth/totp/confirm", authService.WithAuth(http.HandlerFunc(authService.HandleTOTPConfirm), security.RoleAdmin, security.RoleOperator, security.RoleViewer))
	mux.Handle("/v1/auth/totp/disable", authService.WithAuth(http.HandlerFunc(authService.HandleTOTPDisable), security.RoleAdmin, security.RoleOperator, security.RoleViewer))
	mux.Handle("/v1/auth/admins", authService.WithAuth(http.HandlerFunc(authService.HandleAdmins), security.RoleAdmin))
	mux.Handle("/v1/auth/admins/", authService.WithAuth(http.HandlerFunc(authService.HandleAdminByEmail), security.RoleAdmin))
	mux.Handle("/v1/users", authService.WithAuth(http.HandlerFunc(handlerService.HandleUsers), security.RoleViewer, security.RoleOperator, security.RoleAdmin))
	mux.Handle("/v1/users/", authService.WithAuth(http.HandlerFunc(handlerService.HandleUserByEmail), security.RoleAdmin))
	mux.Handle("/v1/sync", authService.WithAuth(http.HandlerFunc(handlerService.HandleSync), security.RoleOperator, security.RoleAdmin))
	mux.Handle("/v1/audits", authService.WithAuth(http.HandlerFunc(handlerService.HandleAudits), security.RoleOperator, security.RoleAdmin))
	mux.Handle("/v1/mailboxes", authService.WithAuth(http.HandlerFunc(handlerService.HandleMailboxes), security.RoleViewer, security.RoleOperator, security.RoleAdmin))
	mux.Handle("/v1/messages", authService.WithAuth(http.HandlerFunc(handlerService.HandleMessages), security.RoleViewer, security.RoleOperator, security.RoleAdmin))
	mux.Handle("/v1/send", authService.WithAuth(http.HandlerFunc(handlerService.HandleSend), security.RoleOperator, security.RoleAdmin))
	return mux
}
