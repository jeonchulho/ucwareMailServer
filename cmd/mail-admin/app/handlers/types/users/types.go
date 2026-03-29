package users

import "time"

// CreateUserRequest 는 메일 사용자 생성 API 요청 본문입니다.
// Email 은 생성 대상 주소, Password 는 초기 로그인 비밀번호(서버에서 해시 저장)입니다.
type CreateUserRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// UserResponse 는 사용자 조회/생성 API에서 반환되는 기본 사용자 정보입니다.
// CreatedAt 은 계정이 시스템에 등록된 시각입니다.
type UserResponse struct {
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"createdAt"`
}

// AuditResponse 는 감사 로그 조회 API의 단일 이벤트 응답 모델입니다.
// Action: 수행된 작업, Actor: 작업 주체(관리자), Email: 대상 사용자,
// Status/Message: 결과 요약, RemoteIP/UserAgent: 요청 출처 정보입니다.
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
