package app

import (
	"golang.org/x/oauth2"
	goauth2google "golang.org/x/oauth2/google"
)

func configureOAuth2(s *server) {
	// Google OAuth2 설정 (Client ID가 있을 때만)
	if s.cfg.GoogleOAuth2ClientID != "" {
		s.googleOAuth2Cfg = &oauth2.Config{
			ClientID:     s.cfg.GoogleOAuth2ClientID,
			ClientSecret: s.cfg.GoogleOAuth2ClientSecret,
			RedirectURL:  s.cfg.OAuth2CallbackBase + "/v1/auth/oauth2/google/callback",
			Scopes:       []string{"openid", "email", "profile"},
			Endpoint:     goauth2google.Endpoint,
		}
	}

	// Microsoft OAuth2 설정
	if s.cfg.MicrosoftOAuth2ClientID != "" {
		s.microsoftOAuth2Cfg = &oauth2.Config{
			ClientID:     s.cfg.MicrosoftOAuth2ClientID,
			ClientSecret: s.cfg.MicrosoftOAuth2ClientSecret,
			RedirectURL:  s.cfg.OAuth2CallbackBase + "/v1/auth/oauth2/microsoft/callback",
			Scopes:       []string{"openid", "email", "profile"},
			Endpoint: oauth2.Endpoint{
				AuthURL:  "https://login.microsoftonline.com/" + s.cfg.MicrosoftTenant + "/oauth2/v2.0/authorize",
				TokenURL: "https://login.microsoftonline.com/" + s.cfg.MicrosoftTenant + "/oauth2/v2.0/token",
			},
		}
	}
}
