package test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	userstypes "github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/handlers/types/users"
)

func TestHandleHealthz(t *testing.T) {
	f := newFixture(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	f.service.HandleHealthz(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestHandleUsersCreateListAndSync(t *testing.T) {
	f := newFixture(t)

	createReq := withActor(newJSONRequest(t, http.MethodPost, "/v1/users", userstypes.CreateUserRequest{Email: "user@example.com", Password: "StrongPass!123"}), "admin@example.com", "admin")
	createRR := httptest.NewRecorder()
	f.service.HandleUsers(createRR, createReq)
	if createRR.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", createRR.Code, createRR.Body.String())
	}

	listReq := withActor(httptest.NewRequest(http.MethodGet, "/v1/users", nil), "viewer@example.com", "viewer")
	listRR := httptest.NewRecorder()
	f.service.HandleUsers(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", listRR.Code)
	}
	users := decodeJSON[[]userstypes.UserResponse](t, listRR)
	if len(users) != 1 || users[0].Email != "user@example.com" {
		t.Fatalf("unexpected users payload: %+v", users)
	}

	syncReq := withActor(httptest.NewRequest(http.MethodPost, "/v1/sync", nil), "admin@example.com", "admin")
	syncReq.RemoteAddr = "127.0.0.1:23456"
	syncRR := httptest.NewRecorder()
	f.service.HandleSync(syncRR, syncReq)
	if syncRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", syncRR.Code, syncRR.Body.String())
	}
}

func TestHandleAuditsReturnsRows(t *testing.T) {
	f := newFixture(t)
	createReq := withActor(newJSONRequest(t, http.MethodPost, "/v1/users", userstypes.CreateUserRequest{Email: "audit@example.com", Password: "StrongPass!123"}), "admin@example.com", "admin")
	createRR := httptest.NewRecorder()
	f.service.HandleUsers(createRR, createReq)
	if createRR.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", createRR.Code)
	}

	auditReq := withActor(httptest.NewRequest(http.MethodGet, "/v1/audits?limit=10", nil), "operator@example.com", "operator")
	auditReq.RemoteAddr = "127.0.0.1:34567"
	auditRR := httptest.NewRecorder()
	f.service.HandleAudits(auditRR, auditReq)
	if auditRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", auditRR.Code, auditRR.Body.String())
	}
	rows := decodeJSON[[]userstypes.AuditResponse](t, auditRR)
	if len(rows) == 0 {
		t.Fatal("expected at least one audit row")
	}
}
