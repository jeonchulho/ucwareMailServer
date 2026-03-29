package test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	authtypes "github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/auth/types"
	"golang.org/x/crypto/bcrypt"
)

func TestHandleAdminsCreateAndList(t *testing.T) {
	f := newAuthFixture(t)

	createReq := withActor(newJSONRequest(t, http.MethodPost, "/v1/auth/admins", authtypes.CreateAdminRequest{Email: "operator@example.com", Password: "StrongPass!123", Role: "operator"}), "admin@example.com")
	createRR := httptest.NewRecorder()
	f.service.HandleAdmins(createRR, createReq)
	if createRR.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", createRR.Code, createRR.Body.String())
	}

	listReq := withActor(httptest.NewRequest(http.MethodGet, "/v1/auth/admins", nil), "admin@example.com")
	listRR := httptest.NewRecorder()
	f.service.HandleAdmins(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", listRR.Code, listRR.Body.String())
	}
	admins := decodeJSON[[]authtypes.AdminUserResponse](t, listRR)
	if len(admins) < 2 {
		t.Fatalf("expected at least 2 admins, got %+v", admins)
	}
}

func TestHandleAdminByEmailChangeRole(t *testing.T) {
	f := newAuthFixture(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("StrongPass!123"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("GenerateFromPassword: %v", err)
	}
	_, err = f.store.UpsertAdminUser(httptest.NewRequest(http.MethodGet, "/", nil).Context(), "viewer@example.com", string(hash), "viewer")
	if err != nil {
		t.Fatalf("UpsertAdminUser: %v", err)
	}
	req := withActor(newJSONRequest(t, http.MethodPatch, "/v1/auth/admins/viewer@example.com/role", authtypes.ChangeRoleRequest{Role: "operator"}), "admin@example.com")
	req.URL.Path = "/v1/auth/admins/viewer@example.com/role"
	rr := httptest.NewRecorder()
	f.service.HandleAdminByEmail(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	admin, err := f.store.GetAdminUserByEmail(req.Context(), "viewer@example.com")
	if err != nil {
		t.Fatalf("GetAdminUserByEmail: %v", err)
	}
	if admin.Role != "operator" {
		t.Fatalf("expected role operator, got %s", admin.Role)
	}
}
