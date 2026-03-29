package auth

import "time"

// LoginRequest 는 로그인 API 요청 본문입니다.
// 이메일과 평문 비밀번호를 받아 인증을 시도합니다.
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// LoginResponse 는 로그인 성공 시 반환되는 토큰/계정 정보 응답입니다.
// AccessToken 은 API 인증에 사용되며, RefreshToken 은 AccessToken 재발급에 사용됩니다.
type LoginResponse struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	TokenType    string    `json:"tokenType"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Role         string    `json:"role"`
	Email        string    `json:"email"`
}

// RefreshRequest 는 액세스 토큰 재발급 요청 본문입니다.
type RefreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// AdminUserResponse 는 관리자 사용자 조회 API에서 반환되는 계정 요약 정보입니다.
type AdminUserResponse struct {
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"createdAt"`
}

// CreateAdminRequest 는 관리자 계정 생성 요청 본문입니다.
// role 은 서버 정책에 따라 admin/operator/viewer 등 허용된 값만 사용해야 합니다.
type CreateAdminRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// ChangeRoleRequest 는 관리자 계정의 권한(role) 변경 요청 본문입니다.
type ChangeRoleRequest struct {
	Role string `json:"role"`
}

// ChangePasswordRequest 는 관리자 계정 비밀번호 변경 요청 본문입니다.
type ChangePasswordRequest struct {
	Password string `json:"password"`
}

// TOTPSetupResponse 는 TOTP(2단계 인증) 초기 설정 시 반환되는 정보입니다.
// Secret 은 앱 등록용 시드이며, OTPAuth 는 인증 앱에서 스캔 가능한 otpauth URL 입니다.
type TOTPSetupResponse struct {
	Secret  string `json:"secret"`
	OTPAuth string `json:"otpAuthURL"`
}

// TOTPVerifyRequest 는 이미 등록된 TOTP 코드 검증 요청 본문입니다.
type TOTPVerifyRequest struct {
	Code string `json:"code"`
}

// TOTPChallengeRequest 는 로그인 2차 인증 단계에서 전달되는 요청 본문입니다.
// ChallengeToken 과 사용자가 입력한 1회용 코드를 함께 전달합니다.
type TOTPChallengeRequest struct {
	ChallengeToken string `json:"challengeToken"`
	Code           string `json:"code"`
}

// TOTPChallengeResponse 는 2차 인증 챌린지 처리 결과 응답입니다.
// status 가 challenge 이면 ChallengeToken 이 포함될 수 있고,
// 최종 인증 성공 시에는 토큰 발급 단계로 이어집니다.
type TOTPChallengeResponse struct {
	Status         string `json:"status"`
	ChallengeToken string `json:"challengeToken,omitempty"`
}
