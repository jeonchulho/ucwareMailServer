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

	authsvc "github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/auth"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/internal/store"
	"golang.org/x/crypto/bcrypt"
)

type actorKey struct{}

type authFixture struct {
	service *authsvc.Service
	store   *store.SQLiteStore
	cfg     authsvc.Config
}

func newAuthFixture(t *testing.T) *authFixture {
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

	cfg := authsvc.Config{
		JWTSecret:               "test-secret-key",
		JWTIssuer:               "test-issuer",
		JWTExpiryMinutes:        60,
		RefreshTokenExpiryDays:  30,
		TOTPIssuer:              "ucware-mail-admin-test",
		TOTPChallengeExpiryMins: 5,
		BcryptCost:              bcrypt.MinCost,
		BootstrapAdminEmail:     "admin@example.com",
		BootstrapAdminPassword:  "ChangeMeAdmin!123",
		BootstrapAdminRole:      "admin",
	}

	svc := authsvc.NewService(authsvc.Dependencies{
		Store:  st,
		Config: cfg,
		WriteAudit: func(context.Context, string, string, string, string, string, *http.Request) {
		},
		ActorFromContext: func(ctx context.Context) string {
			v, _ := ctx.Value(actorKey{}).(string)
			return strings.ToLower(strings.TrimSpace(v))
		},
		SetAuthContext: func(ctx context.Context, email, role string) context.Context {
			return context.WithValue(ctx, actorKey{}, email)
		},
		IsValidRole: func(role string) bool {
			switch strings.ToLower(strings.TrimSpace(role)) {
			case "admin", "operator", "viewer":
				return true
			default:
				return false
			}
		},
	})

	if err := svc.EnsureBootstrapAdmin(context.Background()); err != nil {
		t.Fatalf("EnsureBootstrapAdmin: %v", err)
	}

	return &authFixture{service: svc, store: st, cfg: cfg}
}

func withActor(r *http.Request, email string) *http.Request {
	ctx := context.WithValue(r.Context(), actorKey{}, email)
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
