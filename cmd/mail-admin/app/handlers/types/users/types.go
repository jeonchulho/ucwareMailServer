package users

import "time"

type CreateUserRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type UserResponse struct {
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"createdAt"`
}

type AuditResponse struct {
	Action    string    `json:"action"`
	Actor     string    `json:"actorEmail,omitempty"`
	Email     string    `json:"targetEmail,omitempty"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	RemoteIP  string    `json:"remoteIp"`
	UserAgent string    `json:"userAgent"`
	CreatedAt time.Time `json:"createdAt"`
}
