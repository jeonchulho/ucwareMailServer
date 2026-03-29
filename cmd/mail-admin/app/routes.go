package app

import (
	"net/http"

	authsvc "github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/auth"
	handlersvc "github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/handlers"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/security"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/web"
)

func buildMux(authService *authsvc.Service, handlerService *handlersvc.Service, ssrHandler *web.Handler) *http.ServeMux {
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

	// ── SSR 라우트 ──────────────────────────────────────────────────────────
	if ssrHandler != nil {
		// 정적 애셋 (CSS 등)
		mux.Handle("/static/", ssrHandler.StaticHandler())

		// 인증 불필요
		mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				ssrHandler.HandleLoginPage(w, r)
			case http.MethodPost:
				ssrHandler.HandleLoginSubmit(w, r)
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
		})
		mux.HandleFunc("/logout", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			ssrHandler.HandleLogout(w, r)
		})

		// 세션 필요
		protected := ssrHandler.RequireSession
		mux.Handle("/", protected(http.HandlerFunc(ssrHandler.HandleRoot)))
		mux.Handle("/mail/", protected(http.HandlerFunc(ssrHandler.HandleMail)))
		mux.Handle("/compose", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Redirect(w, r, "/mail/INBOX", http.StatusSeeOther)
				return
			}
			ssrHandler.HandleCompose(w, r)
		})))
	}
	return mux
}
