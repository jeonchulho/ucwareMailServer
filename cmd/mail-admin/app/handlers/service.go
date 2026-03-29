package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
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
			resp = append(resp, archivetypes.MessageResponse{ID: row.ID, MailboxID: row.MailboxID, Direction: row.Direction, FromAddr: row.FromAddr, ToAddr: row.ToAddr, Subject: row.Subject, RawMIME: row.RawMIME, TextBody: row.TextBody, SizeBytes: row.SizeBytes, ReceivedAt: row.ReceivedAt, CreatedAt: row.CreatedAt})
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
		httpx.WriteJSON(w, http.StatusCreated, archivetypes.MessageResponse{ID: msg.ID, MailboxID: msg.MailboxID, Direction: msg.Direction, FromAddr: msg.FromAddr, ToAddr: msg.ToAddr, Subject: msg.Subject, RawMIME: msg.RawMIME, TextBody: msg.TextBody, SizeBytes: msg.SizeBytes, ReceivedAt: msg.ReceivedAt, CreatedAt: msg.CreatedAt})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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
