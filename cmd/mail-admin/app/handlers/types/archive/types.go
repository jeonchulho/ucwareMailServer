package archive

import "time"

type CreateMailboxRequest struct {
	UserEmail string `json:"userEmail"`
	Name      string `json:"name"`
}

type MailboxResponse struct {
	ID        string    `json:"id"`
	UserEmail string    `json:"userEmail"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

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

type AttachmentMeta struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
}
