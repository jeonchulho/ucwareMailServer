package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	archivetypes "github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/handlers/types/archive"
	userstypes "github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/handlers/types/users"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/infra/httpx"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/internal/archive"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/internal/store"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/internal/syncer"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/security"
	"golang.org/x/crypto/bcrypt"
)

type Config struct {
	DovecotUsersFile        string
	PostfixMailboxMapsFile  string
	PostfixDomainsFile      string
	MailRoot                string
	MailUID                 int
	MailGID                 int
	BcryptCost              int
	ArchiveAutoRouteEnabled bool
	ArchiveInboundMailbox   string
	ArchiveOutboundMailbox  string
	SMTPRelayAddr           string
	SMTPUsername            string
	SMTPPassword            string
}

type SendMessageRequest struct {
	FromAddr    string           `json:"fromAddr"`
	ToAddr      string           `json:"toAddr"`
	Subject     string           `json:"subject"`
	TextBody    string           `json:"textBody"`
	RawMIME     string           `json:"rawMime"`
	MailboxID   string           `json:"mailboxId"`
	Attachments []SendAttachment `json:"attachments,omitempty"`
}

type SendAttachment struct {
	Filename      string `json:"filename"`
	ContentType   string `json:"contentType"`
	ContentBase64 string `json:"contentBase64"`
}

type SendMessageResponse struct {
	Status          string                        `json:"status"`
	Archived        bool                          `json:"archived"`
	AttachmentCount int                           `json:"attachmentCount,omitempty"`
	Attachments     []archivetypes.AttachmentMeta `json:"attachments,omitempty"`
	Warning         string                        `json:"warning,omitempty"`
}

const (
	sendMaxAttachments      = 20
	sendMaxAttachmentBytes  = 10 * 1024 * 1024
	sendMaxTotalAttachBytes = 50 * 1024 * 1024
	sendMultipartMemLimit   = 8 * 1024 * 1024
)

type sendFilePayload struct {
	Filename    string
	ContentType string
	Data        []byte
}

var errInvalidAutoRoute = errors.New("invalid auto route input")

type Dependencies struct {
	Store            *store.SQLiteStore
	Archive          *archive.SQLStore
	Config           Config
	WriteAudit       func(ctx context.Context, action, actor, email, status, message string, r *http.Request)
	ActorFromContext func(ctx context.Context) string
	RoleFromContext  func(ctx context.Context) string
}

type Service struct {
	store          *store.SQLiteStore
	archive        *archive.SQLStore
	cfg            Config
	writeAuditFn   func(ctx context.Context, action, actor, email, status, message string, r *http.Request)
	actorFromCtxFn func(ctx context.Context) string
	roleFromCtxFn  func(ctx context.Context) string
	mu             sync.Mutex
}

func NewService(dep Dependencies) *Service {
	return &Service{
		store:          dep.Store,
		archive:        dep.Archive,
		cfg:            dep.Config,
		writeAuditFn:   dep.WriteAudit,
		actorFromCtxFn: dep.ActorFromContext,
		roleFromCtxFn:  dep.RoleFromContext,
	}
}

func (s *Service) HandleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Service) HandleUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listUsers(w, r)
	case http.MethodPost:
		if s.roleFromContext(r.Context()) != security.RoleAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		s.createUser(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Service) HandleUserByEmail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	email := strings.TrimPrefix(r.URL.Path, "/v1/users/")
	if email == "" {
		http.Error(w, "email is required", http.StatusBadRequest)
		return
	}
	if !isValidEmail(email) {
		http.Error(w, "invalid email", http.StatusBadRequest)
		s.writeAudit(r.Context(), "delete_user", s.actorFromContext(r.Context()), email, "failed", "invalid email", r)
		return
	}

	if err := s.store.DeleteUser(r.Context(), strings.ToLower(email)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		s.writeAudit(r.Context(), "delete_user", s.actorFromContext(r.Context()), email, "failed", err.Error(), r)
		return
	}

	if err := s.syncNow(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		s.writeAudit(r.Context(), "delete_user", s.actorFromContext(r.Context()), email, "failed", err.Error(), r)
		return
	}

	s.writeAudit(r.Context(), "delete_user", s.actorFromContext(r.Context()), email, "ok", "deleted", r)
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"result": "deleted"})
}

func (s *Service) HandleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.syncNow(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		s.writeAudit(r.Context(), "sync", s.actorFromContext(r.Context()), "", "failed", err.Error(), r)
		return
	}
	s.writeAudit(r.Context(), "sync", s.actorFromContext(r.Context()), "", "ok", "synced", r)
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"result": "synced"})
}

func (s *Service) HandleAudits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 1000 {
			http.Error(w, "limit must be between 1 and 1000", http.StatusBadRequest)
			return
		}
		limit = n
	}

	rows, err := s.store.ListAuditLogs(r.Context(), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := make([]userstypes.AuditResponse, 0, len(rows))
	for _, row := range rows {
		resp = append(resp, userstypes.AuditResponse{Action: row.Action, Actor: row.Actor, Email: row.Email, Status: row.Status, Message: row.Message, RemoteIP: row.RemoteIP, UserAgent: row.UserAgent, CreatedAt: row.CreatedAt})
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (s *Service) HandleMailboxes(w http.ResponseWriter, r *http.Request) {
	if s.archive == nil {
		http.Error(w, "archive db is disabled", http.StatusNotImplemented)
		return
	}

	switch r.Method {
	case http.MethodGet:
		userEmail := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("userEmail")))
		rows, err := s.archive.ListMailboxes(r.Context(), userEmail)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp := make([]archivetypes.MailboxResponse, 0, len(rows))
		for _, row := range rows {
			resp = append(resp, archivetypes.MailboxResponse{ID: row.ID, UserEmail: row.UserEmail, Name: row.Name, CreatedAt: row.CreatedAt})
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
	case http.MethodPost:
		if role := s.roleFromContext(r.Context()); role != security.RoleAdmin && role != security.RoleOperator {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		defer r.Body.Close()
		var req archivetypes.CreateMailboxRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		req.UserEmail = strings.ToLower(strings.TrimSpace(req.UserEmail))
		req.Name = strings.TrimSpace(req.Name)
		if !isValidEmail(req.UserEmail) {
			http.Error(w, "invalid userEmail", http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		mb, err := s.archive.CreateMailbox(r.Context(), req.UserEmail, req.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.writeAudit(r.Context(), "create_mailbox", s.actorFromContext(r.Context()), req.UserEmail, "ok", req.Name, r)
		httpx.WriteJSON(w, http.StatusCreated, archivetypes.MailboxResponse{ID: mb.ID, UserEmail: mb.UserEmail, Name: mb.Name, CreatedAt: mb.CreatedAt})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Service) HandleMessages(w http.ResponseWriter, r *http.Request) {
	if s.archive == nil {
		http.Error(w, "archive db is disabled", http.StatusNotImplemented)
		return
	}

	switch r.Method {
	case http.MethodGet:
		mailboxID := strings.TrimSpace(r.URL.Query().Get("mailboxId"))
		if mailboxID == "" {
			http.Error(w, "mailboxId is required", http.StatusBadRequest)
			return
		}
		limit := 100
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 1000 {
				http.Error(w, "limit must be between 1 and 1000", http.StatusBadRequest)
				return
			}
			limit = n
		}
		rows, err := s.archive.ListMessages(r.Context(), mailboxID, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp := make([]archivetypes.MessageResponse, 0, len(rows))
		for _, row := range rows {
			resp = append(resp, archivetypes.MessageResponse{
				ID:          row.ID,
				MailboxID:   row.MailboxID,
				Direction:   row.Direction,
				FromAddr:    row.FromAddr,
				ToAddr:      row.ToAddr,
				Subject:     row.Subject,
				RawMIME:     row.RawMIME,
				TextBody:    row.TextBody,
				SizeBytes:   row.SizeBytes,
				Attachments: extractAttachmentMeta(row.RawMIME),
				ReceivedAt:  row.ReceivedAt,
				CreatedAt:   row.CreatedAt,
			})
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
	case http.MethodPost:
		if role := s.roleFromContext(r.Context()); role != security.RoleAdmin && role != security.RoleOperator {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		defer r.Body.Close()
		var req archivetypes.IngestMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		req.MailboxID = strings.TrimSpace(req.MailboxID)
		req.Direction = strings.ToLower(strings.TrimSpace(req.Direction))
		req.FromAddr = strings.TrimSpace(req.FromAddr)
		req.ToAddr = strings.TrimSpace(req.ToAddr)
		if req.FromAddr == "" || req.ToAddr == "" {
			http.Error(w, "fromAddr/toAddr are required", http.StatusBadRequest)
			return
		}
		if req.Direction != "inbound" && req.Direction != "outbound" {
			http.Error(w, "direction must be inbound or outbound", http.StatusBadRequest)
			return
		}
		if req.MailboxID == "" {
			if !s.cfg.ArchiveAutoRouteEnabled {
				http.Error(w, "mailboxId is required (or enable ARCHIVE_AUTO_ROUTE_ENABLED)", http.StatusBadRequest)
				return
			}
			mailboxID, err := s.resolveMailboxIDForAutoRoute(r.Context(), req.Direction, req.FromAddr, req.ToAddr)
			if err != nil {
				if errors.Is(err, errInvalidAutoRoute) {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			req.MailboxID = mailboxID
		}
		receivedAt := time.Now().UTC()
		if strings.TrimSpace(req.ReceivedAt) != "" {
			t, err := time.Parse(time.RFC3339, req.ReceivedAt)
			if err != nil {
				http.Error(w, "receivedAt must be RFC3339", http.StatusBadRequest)
				return
			}
			receivedAt = t.UTC()
		}
		sizeBytes := req.SizeBytes
		if sizeBytes < 0 {
			http.Error(w, "sizeBytes must be >= 0", http.StatusBadRequest)
			return
		}
		if sizeBytes == 0 && req.RawMIME != "" {
			sizeBytes = int64(len(req.RawMIME))
		}
		msg, err := s.archive.CreateMessage(r.Context(), archive.CreateMessageInput{MailboxID: req.MailboxID, Direction: req.Direction, FromAddr: req.FromAddr, ToAddr: req.ToAddr, Subject: req.Subject, RawMIME: req.RawMIME, TextBody: req.TextBody, SizeBytes: sizeBytes, ReceivedAt: receivedAt})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.writeAudit(r.Context(), "ingest_message", s.actorFromContext(r.Context()), "", "ok", msg.Direction, r)
		httpx.WriteJSON(w, http.StatusCreated, archivetypes.MessageResponse{
			ID:          msg.ID,
			MailboxID:   msg.MailboxID,
			Direction:   msg.Direction,
			FromAddr:    msg.FromAddr,
			ToAddr:      msg.ToAddr,
			Subject:     msg.Subject,
			RawMIME:     msg.RawMIME,
			TextBody:    msg.TextBody,
			SizeBytes:   msg.SizeBytes,
			Attachments: extractAttachmentMeta(msg.RawMIME),
			ReceivedAt:  msg.ReceivedAt,
			CreatedAt:   msg.CreatedAt,
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Service) HandleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.TrimSpace(s.cfg.SMTPRelayAddr) == "" {
		http.Error(w, "SMTP_RELAY_ADDR is not configured", http.StatusServiceUnavailable)
		return
	}

	var req SendMessageRequest
	files, err := parseSendRequest(r, &req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.FromAddr = strings.ToLower(strings.TrimSpace(req.FromAddr))
	req.ToAddr = strings.ToLower(strings.TrimSpace(req.ToAddr))
	req.Subject = strings.TrimSpace(req.Subject)
	if !isValidEmail(req.FromAddr) || !isValidEmail(req.ToAddr) {
		http.Error(w, "fromAddr/toAddr are required and must be valid email", http.StatusBadRequest)
		return
	}

	raw := strings.TrimSpace(req.RawMIME)
	if raw == "" {
		if len(files) > 0 {
			raw, err = buildMultipartMIME(req.FromAddr, req.ToAddr, req.Subject, req.TextBody, files)
			if err != nil {
				http.Error(w, "build multipart mime failed: "+err.Error(), http.StatusBadRequest)
				return
			}
		} else if len(req.Attachments) > 0 {
			files, err = decodeJSONAttachments(req.Attachments)
			if err != nil {
				http.Error(w, "invalid attachments: "+err.Error(), http.StatusBadRequest)
				return
			}
			raw, err = buildMultipartMIME(req.FromAddr, req.ToAddr, req.Subject, req.TextBody, files)
			if err != nil {
				http.Error(w, "build multipart mime failed: "+err.Error(), http.StatusBadRequest)
				return
			}
		} else {
			raw = buildRawMIME(req.FromAddr, req.ToAddr, req.Subject, req.TextBody)
		}
	}

	var auth smtp.Auth
	if strings.TrimSpace(s.cfg.SMTPUsername) != "" {
		host := strings.TrimSpace(s.cfg.SMTPRelayAddr)
		if i := strings.Index(host, ":"); i >= 0 {
			host = host[:i]
		}
		auth = smtp.PlainAuth("", s.cfg.SMTPUsername, s.cfg.SMTPPassword, host)
	}

	if err := smtp.SendMail(s.cfg.SMTPRelayAddr, auth, req.FromAddr, []string{req.ToAddr}, []byte(raw)); err != nil {
		s.writeAudit(r.Context(), "send_message", s.actorFromContext(r.Context()), req.ToAddr, "failed", err.Error(), r)
		http.Error(w, "smtp send failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	meta := extractAttachmentMeta(raw)
	resp := SendMessageResponse{Status: "sent", Archived: false, AttachmentCount: len(meta), Attachments: meta}
	archiveErr := s.archiveOutboundCopy(r.Context(), req, raw)
	if archiveErr != nil {
		resp.Warning = archiveErr.Error()
	} else {
		resp.Archived = true
	}

	s.writeAudit(r.Context(), "send_message", s.actorFromContext(r.Context()), req.ToAddr, "ok", "sent", r)
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (s *Service) archiveOutboundCopy(ctx context.Context, req SendMessageRequest, raw string) error {
	if s.archive == nil {
		return fmt.Errorf("archive db is disabled")
	}

	mailboxID := strings.TrimSpace(req.MailboxID)
	if mailboxID == "" {
		if s.cfg.ArchiveAutoRouteEnabled {
			id, err := s.resolveMailboxIDForAutoRoute(ctx, "outbound", req.FromAddr, req.ToAddr)
			if err != nil {
				return fmt.Errorf("archive route failed: %w", err)
			}
			mailboxID = id
		} else {
			boxes, err := s.archive.ListMailboxes(ctx, req.FromAddr)
			if err != nil {
				return fmt.Errorf("archive list mailboxes failed: %w", err)
			}
			mailboxName := strings.TrimSpace(s.cfg.ArchiveOutboundMailbox)
			if mailboxName == "" {
				mailboxName = "SENT"
			}
			for _, b := range boxes {
				if strings.EqualFold(strings.TrimSpace(b.Name), mailboxName) {
					mailboxID = b.ID
					break
				}
			}
			if mailboxID == "" {
				mb, err := s.archive.CreateMailbox(ctx, req.FromAddr, mailboxName)
				if err != nil {
					return fmt.Errorf("archive create mailbox failed: %w", err)
				}
				mailboxID = mb.ID
			}
		}
	}

	_, err := s.archive.CreateMessage(ctx, archive.CreateMessageInput{
		MailboxID:  mailboxID,
		Direction:  "outbound",
		FromAddr:   req.FromAddr,
		ToAddr:     req.ToAddr,
		Subject:    req.Subject,
		RawMIME:    raw,
		TextBody:   req.TextBody,
		SizeBytes:  int64(len(raw)),
		ReceivedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("archive create message failed: %w", err)
	}
	return nil
}

func (s *Service) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := make([]userstypes.UserResponse, 0, len(users))
	for _, u := range users {
		resp = append(resp, userstypes.UserResponse{Email: u.Email, CreatedAt: u.CreatedAt})
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (s *Service) createUser(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req userstypes.CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if !isValidEmail(req.Email) {
		http.Error(w, "invalid email", http.StatusBadRequest)
		s.writeAudit(r.Context(), "create_user", s.actorFromContext(r.Context()), req.Email, "failed", "invalid email", r)
		return
	}
	if len(req.Password) < 10 {
		http.Error(w, "password must be at least 10 characters", http.StatusBadRequest)
		s.writeAudit(r.Context(), "create_user", s.actorFromContext(r.Context()), req.Email, "failed", "password too short", r)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), s.cfg.BcryptCost)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		s.writeAudit(r.Context(), "create_user", s.actorFromContext(r.Context()), req.Email, "failed", err.Error(), r)
		return
	}

	u, err := s.store.UpsertUser(r.Context(), req.Email, string(hash))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		s.writeAudit(r.Context(), "create_user", s.actorFromContext(r.Context()), req.Email, "failed", err.Error(), r)
		return
	}

	if err := s.syncNow(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		s.writeAudit(r.Context(), "create_user", s.actorFromContext(r.Context()), req.Email, "failed", err.Error(), r)
		return
	}

	s.writeAudit(r.Context(), "create_user", s.actorFromContext(r.Context()), req.Email, "ok", "created", r)
	httpx.WriteJSON(w, http.StatusCreated, userstypes.UserResponse{Email: u.Email, CreatedAt: u.CreatedAt})
}

func (s *Service) syncNow(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return err
	}
	return syncer.Export(users, syncer.ExportConfig{DovecotUsersFile: s.cfg.DovecotUsersFile, PostfixMailboxMapsFile: s.cfg.PostfixMailboxMapsFile, PostfixDomainsFile: s.cfg.PostfixDomainsFile, MailRoot: s.cfg.MailRoot, MailUID: s.cfg.MailUID, MailGID: s.cfg.MailGID})
}

func (s *Service) writeAudit(ctx context.Context, action, actor, email, status, message string, r *http.Request) {
	if s.writeAuditFn != nil {
		s.writeAuditFn(ctx, action, actor, email, status, message, r)
	}
}

func (s *Service) actorFromContext(ctx context.Context) string {
	if s.actorFromCtxFn == nil {
		return ""
	}
	return s.actorFromCtxFn(ctx)
}

func (s *Service) roleFromContext(ctx context.Context) string {
	if s.roleFromCtxFn == nil {
		return ""
	}
	return s.roleFromCtxFn(ctx)
}

func isValidEmail(value string) bool {
	addr, err := mail.ParseAddress(value)
	if err != nil {
		return false
	}
	return strings.EqualFold(addr.Address, value)
}

func buildRawMIME(from, to, subject, body string) string {
	date := time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 +0000")
	return strings.Join([]string{
		"From: " + from,
		"To: " + to,
		"Subject: " + subject,
		"Date: " + date,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: 8bit",
		"",
		body,
	}, "\r\n")
}

func parseSendRequest(r *http.Request, req *SendMessageRequest) ([]sendFilePayload, error) {
	ct := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(sendMultipartMemLimit); err != nil {
			return nil, fmt.Errorf("invalid multipart form")
		}
		req.FromAddr = r.FormValue("fromAddr")
		req.ToAddr = r.FormValue("toAddr")
		req.Subject = r.FormValue("subject")
		req.TextBody = r.FormValue("textBody")
		req.RawMIME = r.FormValue("rawMime")
		req.MailboxID = r.FormValue("mailboxId")
		return collectUploadedAttachments(r.MultipartForm)
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		return nil, fmt.Errorf("invalid json")
	}
	return nil, nil
}

func collectUploadedAttachments(form *multipart.Form) ([]sendFilePayload, error) {
	if form == nil || form.File == nil {
		return nil, nil
	}
	headers := form.File["attachments"]
	if len(headers) == 0 {
		return nil, nil
	}
	if len(headers) > sendMaxAttachments {
		return nil, fmt.Errorf("attachments must be <= %d", sendMaxAttachments)
	}

	out := make([]sendFilePayload, 0, len(headers))
	var total int64
	for _, fh := range headers {
		if fh.Size > sendMaxAttachmentBytes {
			return nil, fmt.Errorf("attachment %s exceeds %d bytes", fh.Filename, sendMaxAttachmentBytes)
		}
		f, err := fh.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(f, sendMaxAttachmentBytes+1))
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > sendMaxAttachmentBytes {
			return nil, fmt.Errorf("attachment %s exceeds %d bytes", fh.Filename, sendMaxAttachmentBytes)
		}
		total += int64(len(data))
		if total > sendMaxTotalAttachBytes {
			return nil, fmt.Errorf("total attachment size exceeds %d bytes", sendMaxTotalAttachBytes)
		}
		ctype := fh.Header.Get("Content-Type")
		if strings.TrimSpace(ctype) == "" {
			ctype = "application/octet-stream"
		}
		out = append(out, sendFilePayload{
			Filename:    filepath.Base(strings.TrimSpace(fh.Filename)),
			ContentType: ctype,
			Data:        data,
		})
	}
	return out, nil
}

func decodeJSONAttachments(items []SendAttachment) ([]sendFilePayload, error) {
	if len(items) > sendMaxAttachments {
		return nil, fmt.Errorf("attachments must be <= %d", sendMaxAttachments)
	}
	out := make([]sendFilePayload, 0, len(items))
	var total int64
	for _, it := range items {
		name := filepath.Base(strings.TrimSpace(it.Filename))
		if name == "" || name == "." {
			return nil, fmt.Errorf("filename is required")
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(it.ContentBase64))
		if err != nil {
			return nil, fmt.Errorf("invalid base64 for %s", name)
		}
		if int64(len(decoded)) > sendMaxAttachmentBytes {
			return nil, fmt.Errorf("attachment %s exceeds %d bytes", name, sendMaxAttachmentBytes)
		}
		total += int64(len(decoded))
		if total > sendMaxTotalAttachBytes {
			return nil, fmt.Errorf("total attachment size exceeds %d bytes", sendMaxTotalAttachBytes)
		}
		ctype := strings.TrimSpace(it.ContentType)
		if ctype == "" {
			ctype = "application/octet-stream"
		}
		out = append(out, sendFilePayload{Filename: name, ContentType: ctype, Data: decoded})
	}
	return out, nil
}

func buildMultipartMIME(from, to, subject, body string, files []sendFilePayload) (string, error) {
	var mimeBody bytes.Buffer
	mw := multipart.NewWriter(&mimeBody)

	textHeader := textproto.MIMEHeader{}
	textHeader.Set("Content-Type", "text/plain; charset=utf-8")
	textHeader.Set("Content-Transfer-Encoding", "8bit")
	textPart, err := mw.CreatePart(textHeader)
	if err != nil {
		return "", err
	}
	if _, err := textPart.Write([]byte(body)); err != nil {
		return "", err
	}

	for _, f := range files {
		if f.Filename == "" {
			continue
		}
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", f.ContentType+`; name="`+f.Filename+`"`)
		h.Set("Content-Disposition", `attachment; filename="`+f.Filename+`"`)
		h.Set("Content-Transfer-Encoding", "base64")
		part, err := mw.CreatePart(h)
		if err != nil {
			return "", err
		}
		enc := base64.NewEncoder(base64.StdEncoding, newBase64LineWriter(part))
		if _, err := enc.Write(f.Data); err != nil {
			_ = enc.Close()
			return "", err
		}
		if err := enc.Close(); err != nil {
			return "", err
		}
	}

	if err := mw.Close(); err != nil {
		return "", err
	}

	date := time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 +0000")
	var raw bytes.Buffer
	fmt.Fprintf(&raw, "From: %s\r\n", from)
	fmt.Fprintf(&raw, "To: %s\r\n", to)
	fmt.Fprintf(&raw, "Subject: %s\r\n", subject)
	fmt.Fprintf(&raw, "Date: %s\r\n", date)
	raw.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&raw, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", mw.Boundary())
	raw.Write(mimeBody.Bytes())
	return raw.String(), nil
}

func extractAttachmentMeta(raw string) []archivetypes.AttachmentMeta {
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		return nil
	}
	ct := msg.Header.Get("Content-Type")
	if ct == "" {
		return nil
	}
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil || !strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		return nil
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil
	}
	out := make([]archivetypes.AttachmentMeta, 0)
	walkMultipartParts(multipart.NewReader(msg.Body, boundary), &out)
	return out
}

func walkMultipartParts(mr *multipart.Reader, out *[]archivetypes.AttachmentMeta) {
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		cd := part.Header.Get("Content-Disposition")
		disp, dispParams, _ := mime.ParseMediaType(cd)
		filename := strings.TrimSpace(dispParams["filename"])
		if filename == "" {
			filename = strings.TrimSpace(part.FileName())
		}
		ct := part.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/octet-stream"
		}
		mediaType, ctParams, _ := mime.ParseMediaType(ct)

		if strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
			if boundary := ctParams["boundary"]; boundary != "" {
				b, _ := io.ReadAll(part)
				walkMultipartParts(multipart.NewReader(bytes.NewReader(b), boundary), out)
			}
			continue
		}

		isAttachment := strings.EqualFold(disp, "attachment") || filename != ""
		n, _ := io.Copy(io.Discard, part)
		if isAttachment {
			if filename == "" {
				filename = "attachment.bin"
			}
			*out = append(*out, archivetypes.AttachmentMeta{
				Filename:    filename,
				ContentType: mediaType,
				SizeBytes:   n,
			})
		}
	}
}

type base64LineWriter struct {
	w io.Writer
	n int
}

func newBase64LineWriter(w io.Writer) *base64LineWriter { return &base64LineWriter{w: w} }

func (bw *base64LineWriter) Write(p []byte) (int, error) {
	written := 0
	for _, b := range p {
		if bw.n == 76 {
			if _, err := bw.w.Write([]byte("\r\n")); err != nil {
				return written, err
			}
			bw.n = 0
		}
		if _, err := bw.w.Write([]byte{b}); err != nil {
			return written, err
		}
		bw.n++
		written++
	}
	return written, nil
}

func (s *Service) EnsureInitialSync(ctx context.Context) error {
	if err := s.syncNow(ctx); err != nil {
		return fmt.Errorf("initial sync error: %w", err)
	}
	return nil
}

func (s *Service) resolveMailboxIDForAutoRoute(ctx context.Context, direction, fromAddr, toAddr string) (string, error) {
	var userEmail string
	var mailboxName string

	switch direction {
	case "inbound":
		userEmail = strings.ToLower(strings.TrimSpace(toAddr))
		mailboxName = strings.TrimSpace(s.cfg.ArchiveInboundMailbox)
	case "outbound":
		userEmail = strings.ToLower(strings.TrimSpace(fromAddr))
		mailboxName = strings.TrimSpace(s.cfg.ArchiveOutboundMailbox)
	default:
		return "", fmt.Errorf("%w: unsupported direction", errInvalidAutoRoute)
	}

	if !isValidEmail(userEmail) {
		return "", fmt.Errorf("%w: invalid target email for auto route", errInvalidAutoRoute)
	}
	if mailboxName == "" {
		return "", fmt.Errorf("%w: mailbox name is empty", errInvalidAutoRoute)
	}

	rows, err := s.archive.ListMailboxes(ctx, userEmail)
	if err != nil {
		return "", fmt.Errorf("list mailboxes failed: %w", err)
	}
	for _, row := range rows {
		if strings.EqualFold(strings.TrimSpace(row.Name), mailboxName) {
			return row.ID, nil
		}
	}

	created, err := s.archive.CreateMailbox(ctx, userEmail, mailboxName)
	if err == nil {
		return created.ID, nil
	}

	// 동시 생성 경쟁이 있으면 다시 조회해 기존 박스를 재사용한다.
	rows, listErr := s.archive.ListMailboxes(ctx, userEmail)
	if listErr == nil {
		for _, row := range rows {
			if strings.EqualFold(strings.TrimSpace(row.Name), mailboxName) {
				return row.ID, nil
			}
		}
	}

	return "", fmt.Errorf("create mailbox failed: %w", err)
}
