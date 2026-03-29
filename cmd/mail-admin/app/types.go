package app

import (
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/internal/archive"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/internal/store"
	"golang.org/x/oauth2"
)

type contextKey string

// authContextKey 는 요청 컨텍스트에 인증 정보를 저장/조회할 때 사용하는 키입니다.
// 문자열 충돌을 피하기 위해 전용 타입(contextKey)을 사용합니다.
const authContextKey contextKey = "auth"

// requestAuthInfo 는 인증 미들웨어가 컨텍스트에 넣어두는 사용자 식별 정보입니다.
// 이후 핸들러에서 현재 요청자의 이메일/역할 확인에 사용됩니다.
type requestAuthInfo struct {
	Email string
	Role  string
}

// config 는 mail-admin 애플리케이션의 런타임 설정 집합입니다.
// 인증/JWT, 스토리지 경로, 메일 서버 연동, OAuth2, TOTP, 아카이브, 레이트리밋,
// LMTP/POP3 기능 토글 등 운영에 필요한 모든 값을 포함합니다.
type config struct {
	Addr                        string // HTTP 관리 API 바인드 주소(예: :8080)
	JWTSecret                   string // JWT 서명 키(HMAC)
	JWTIssuer                   string // JWT iss 클레임 값
	JWTExpiryMinutes            int    // 액세스 토큰 만료 시간(분)
	RefreshTokenExpiryDays      int    // 리프레시 토큰 만료 시간(일)
	BootstrapAdminEmail         string // 초기 부트스트랩 관리자 이메일
	BootstrapAdminPassword      string // 초기 부트스트랩 관리자 비밀번호
	BootstrapAdminRole          string // 초기 관리자 권한(admin/operator 등)
	DBPath                      string // SQLite 사용자/감사 로그 DB 경로
	DovecotUsersFile            string // Dovecot 사용자 파일 출력 경로
	PostfixMailboxMapsFile      string // Postfix mailbox_maps 파일 경로
	PostfixDomainsFile          string // Postfix 가상 도메인 목록 파일 경로
	MailRoot                    string // 메일박스 루트 디렉터리
	MailUID                     int    // 메일 파일 소유 UID
	MailGID                     int    // 메일 파일 소유 GID
	BcryptCost                  int    // 비밀번호 해시 bcrypt cost
	OAuth2CallbackBase          string // OAuth2 콜백 기본 URL(외부 접근 가능한 베이스)
	GoogleOAuth2ClientID        string // Google OAuth2 클라이언트 ID
	GoogleOAuth2ClientSecret    string // Google OAuth2 클라이언트 시크릿
	MicrosoftOAuth2ClientID     string // Microsoft OAuth2 클라이언트 ID
	MicrosoftOAuth2ClientSecret string // Microsoft OAuth2 클라이언트 시크릿
	MicrosoftTenant             string // Microsoft Entra 테넌트 ID/도메인
	TOTPIssuer                  string // TOTP 발급자(Authenticator 앱 표시명)
	TOTPChallengeExpiryMins     int    // 로그인 TOTP 챌린지 토큰 만료 시간(분)
	ArchiveDBEnabled            bool   // 아카이브 DB 기능 활성화 여부
	ArchiveDBDriver             string // 아카이브 DB 드라이버(예: sqlite/postgres)
	ArchiveDSN                  string // 아카이브 DB 연결 문자열
	ArchiveAutoRouteEnabled     bool   // 방향(inbound/outbound) 기반 메일박스 자동 라우팅 사용 여부
	ArchiveInboundMailbox       string // inbound 메시지 기본 적재 메일박스 이름
	ArchiveOutboundMailbox      string // outbound 메시지 기본 적재 메일박스 이름
	SMTPRelayAddr               string // SMTP 릴레이 주소(host:port)
	SMTPUsername                string // SMTP 인증 사용자명(선택)
	SMTPPassword                string // SMTP 인증 비밀번호(선택)
	LoginIPRateLimitPerMin      int    // 로그인 API IP 기준 분당 요청 제한
	LoginFailThreshold          int    // 계정 잠금 전 허용 실패 횟수
	LoginLockMinutes            int    // 임시 잠금 지속 시간(분)
	SendRateLimitPerMin         int    // 메일 발송 API actor/IP 기준 분당 제한
	LMTPEnabled                 bool   // 내장 LMTP 수신 서버 활성화 여부
	LMTPAddr                    string // LMTP 리슨 주소(host:port)
	LMTPDomain                  string // LMTP 수신 허용 도메인
	LMTPMaxMessageBytes         int    // LMTP 단일 메시지 최대 바이트
	POP3Enabled                 bool   // 내장 POP3 서버 활성화 여부
	POP3Addr                    string // POP3 리슨 주소(host:port)
}

// server 는 앱 실행에 필요한 핵심 서비스 핸들을 묶는 런타임 컨테이너입니다.
// 스토리지, 설정, OAuth2 클라이언트 구성을 보관하고 라우터/핸들러에서 재사용합니다.
type server struct {
	store              *store.SQLiteStore // 사용자/감사 로그 저장소
	archive            *archive.SQLStore  // 메일 아카이브 저장소(nil이면 비활성)
	cfg                config             // 정규화된 런타임 설정
	googleOAuth2Cfg    *oauth2.Config     // Google OAuth2 로그인 설정
	microsoftOAuth2Cfg *oauth2.Config     // Microsoft OAuth2 로그인 설정
}
