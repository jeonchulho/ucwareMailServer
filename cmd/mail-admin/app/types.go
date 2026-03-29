package app

import (
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/internal/archive"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/internal/store"
	"golang.org/x/oauth2"
)

type contextKey string

const authContextKey contextKey = "auth"

type requestAuthInfo struct {
	Email string
	Role  string
}

type config struct {
	Addr                        string
	JWTSecret                   string
	JWTIssuer                   string
	JWTExpiryMinutes            int
	RefreshTokenExpiryDays      int
	BootstrapAdminEmail         string
	BootstrapAdminPassword      string
	BootstrapAdminRole          string
	DBPath                      string
	DovecotUsersFile            string
	PostfixMailboxMapsFile      string
	PostfixDomainsFile          string
	MailRoot                    string
	MailUID                     int
	MailGID                     int
	BcryptCost                  int
	OAuth2CallbackBase          string
	GoogleOAuth2ClientID        string
	GoogleOAuth2ClientSecret    string
	MicrosoftOAuth2ClientID     string
	MicrosoftOAuth2ClientSecret string
	MicrosoftTenant             string
	TOTPIssuer                  string
	TOTPChallengeExpiryMins     int
	ArchiveDBEnabled            bool
	ArchiveDBDriver             string
	ArchiveDSN                  string
	ArchiveAutoRouteEnabled     bool
	ArchiveInboundMailbox       string
	ArchiveOutboundMailbox      string
	SMTPRelayAddr               string
	SMTPUsername                string
	SMTPPassword                string
	LMTPEnabled                 bool
	LMTPAddr                    string
	LMTPDomain                  string
	POP3Enabled                 bool
	POP3Addr                    string
}

type server struct {
	store              *store.SQLiteStore
	archive            *archive.SQLStore
	cfg                config
	googleOAuth2Cfg    *oauth2.Config
	microsoftOAuth2Cfg *oauth2.Config
}
