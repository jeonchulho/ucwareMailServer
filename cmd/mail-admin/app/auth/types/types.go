package auth

import "time"

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginResponse struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	TokenType    string    `json:"tokenType"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Role         string    `json:"role"`
	Email        string    `json:"email"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

type AdminUserResponse struct {
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"createdAt"`
}

type CreateAdminRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

type ChangeRoleRequest struct {
	Role string `json:"role"`
}

type ChangePasswordRequest struct {
	Password string `json:"password"`
}

type TOTPSetupResponse struct {
	Secret  string `json:"secret"`
	OTPAuth string `json:"otpAuthURL"`
}

type TOTPVerifyRequest struct {
	Code string `json:"code"`
}

type TOTPChallengeRequest struct {
	ChallengeToken string `json:"challengeToken"`
	Code           string `json:"code"`
}

type TOTPChallengeResponse struct {
	Status         string `json:"status"`
	ChallengeToken string `json:"challengeToken,omitempty"`
}
