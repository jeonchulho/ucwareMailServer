package test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	handlersvc "github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/handlers"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/internal/store"
	"golang.org/x/crypto/bcrypt"
)

type roleKey struct{}
type actorKey struct{}

type fixture struct {
	service *handlersvc.Service
	store   *store.SQLiteStore
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data", "mailadmin.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	st, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	bootstrapHash, err := bcrypt.GenerateFromPassword([]byte("ChangeMeAdmin!123"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("GenerateFromPassword: %v", err)
	}
	if _, err := st.UpsertAdminUser(context.Background(), "admin@example.com", string(bootstrapHash), "admin"); err != nil {
		t.Fatalf("UpsertAdminUser: %v", err)
	}

	svc := handlersvc.NewService(handlersvc.Dependencies{
		Store: st,
		Config: handlersvc.Config{
			DovecotUsersFile:       filepath.Join(dir, "generated", "dovecot", "users.passwd"),
			PostfixMailboxMapsFile: filepath.Join(dir, "generated", "postfix", "virtual_mailbox_maps"),
			PostfixDomainsFile:     filepath.Join(dir, "generated", "postfix", "virtual_mailbox_domains"),
			MailRoot:               filepath.Join(dir, "mailroot"),
			MailUID:                5000,
			MailGID:                5000,
			BcryptCost:             bcrypt.MinCost,
		},
		WriteAudit: func(ctx context.Context, action, actor, email, status, message string, r *http.Request) {
			_ = st.InsertAuditLog(ctx, store.AuditLog{Action: action, Actor: strings.ToLower(strings.TrimSpace(actor)), Email: strings.ToLower(strings.TrimSpace(email)), Status: status, Message: message, RemoteIP: r.RemoteAddr, UserAgent: r.UserAgent()})
		},
		ActorFromContext: func(ctx context.Context) string {
			v, _ := ctx.Value(actorKey{}).(string)
			return strings.ToLower(strings.TrimSpace(v))
		},
		RoleFromContext: func(ctx context.Context) string {
			v, _ := ctx.Value(roleKey{}).(string)
			return strings.ToLower(strings.TrimSpace(v))
		},
	})

	if err := svc.EnsureInitialSync(context.Background()); err != nil {
		t.Fatalf("EnsureInitialSync: %v", err)
	}

	return &fixture{service: svc, store: st}
}

func withActor(r *http.Request, email, role string) *http.Request {
	ctx := context.WithValue(r.Context(), actorKey{}, email)
	ctx = context.WithValue(ctx, roleKey{}, role)
	return r.WithContext(ctx)
}

func newJSONRequest(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"
	return req
}

func decodeJSON[T any](t *testing.T, rr *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("json.Unmarshal: %v body=%s", err, rr.Body.String())
	}
	return out
}
