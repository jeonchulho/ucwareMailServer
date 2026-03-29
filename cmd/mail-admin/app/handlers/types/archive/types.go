package archive

import "time"

// CreateMailboxRequest 는 아카이브 메일박스 생성 API 요청 본문입니다.
// UserEmail 은 메일박스 소유 사용자, Name 은 생성할 메일박스 이름(INBOX/Sent 등)입니다.
type CreateMailboxRequest struct {
	UserEmail string `json:"userEmail"`
	Name      string `json:"name"`
}

// MailboxResponse 는 메일박스 조회/생성 API에서 반환되는 메일박스 정보입니다.
// ID 는 내부 식별자이며, CreatedAt 은 UTC 기준 생성 시각입니다.
type MailboxResponse struct {
	ID        string    `json:"id"`
	UserEmail string    `json:"userEmail"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

// IngestMessageRequest 는 외부에서 메시지를 아카이브 저장소로 수집(ingest)할 때 사용하는 요청 본문입니다.
// Direction 은 inbound/outbound 를 사용하며, RawMIME/TextBody/첨부 메타를 기반으로 저장 레코드를 구성합니다.
// ReceivedAt 은 RFC3339 문자열 시각을 기대합니다.
type IngestMessageRequest struct {
	MailboxID  string `json:"mailboxId"`
	Direction  string `json:"direction"`
	FromAddr   string `json:"fromAddr"`
	ToAddr     string `json:"toAddr"`
	Subject    string `json:"subject"`
	RawMIME    string `json:"rawMime"`
	TextBody   string `json:"textBody"`
	SizeBytes  int64  `json:"sizeBytes"`
	ReceivedAt string `json:"receivedAt"`
}

// MessageResponse 는 아카이브 메시지 조회 API 응답 모델입니다.
// 첨부 파일이 있는 경우 Attachments 에 파일명/콘텐츠 타입/크기 메타데이터가 포함됩니다.
// ReceivedAt 은 원본 수신 시각, CreatedAt 은 아카이브 DB 저장 시각입니다.
type MessageResponse struct {
	ID          string           `json:"id"`
	MailboxID   string           `json:"mailboxId"`
	Direction   string           `json:"direction"`
	FromAddr    string           `json:"fromAddr"`
	ToAddr      string           `json:"toAddr"`
	Subject     string           `json:"subject"`
	RawMIME     string           `json:"rawMime"`
	TextBody    string           `json:"textBody"`
	SizeBytes   int64            `json:"sizeBytes"`
	Attachments []AttachmentMeta `json:"attachments,omitempty"`
	ReceivedAt  time.Time        `json:"receivedAt"`
	CreatedAt   time.Time        `json:"createdAt"`
}

// AttachmentMeta 는 메시지에 포함된 첨부 파일의 메타데이터입니다.
// Raw 바이너리는 포함하지 않고, 조회/응답에 필요한 최소 정보만 제공합니다.
type AttachmentMeta struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
}
