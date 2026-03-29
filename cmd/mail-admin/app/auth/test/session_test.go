package test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	authsvc "github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/auth"
	authtypes "github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/auth/types"
)

func TestHandleLoginRefreshLogoutFlow(t *testing.T) {
	f := newAuthFixture(t)

	loginReq := newJSONRequest(t, http.MethodPost, "/v1/auth/login", authtypes.LoginRequest{Email: f.cfg.BootstrapAdminEmail, Password: f.cfg.BootstrapAdminPassword})
	loginRR := httptest.NewRecorder()
	f.service.HandleLogin(loginRR, loginReq)
	if loginRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", loginRR.Code, loginRR.Body.String())
	}
	loginResp := decodeJSON[authtypes.LoginResponse](t, loginRR)
	if loginResp.AccessToken == "" || loginResp.RefreshToken == "" {
		t.Fatalf("expected tokens, got %+v", loginResp)
	}

	refreshReq := newJSONRequest(t, http.MethodPost, "/v1/auth/refresh", authtypes.RefreshRequest{RefreshToken: loginResp.RefreshToken})
	refreshRR := httptest.NewRecorder()
	f.service.HandleRefreshToken(refreshRR, refreshReq)
	if refreshRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", refreshRR.Code, refreshRR.Body.String())
	}

	logoutReq := newJSONRequest(t, http.MethodPost, "/v1/auth/logout", authtypes.RefreshRequest{RefreshToken: loginResp.RefreshToken})
	logoutRR := httptest.NewRecorder()
	f.service.HandleLogout(logoutRR, logoutReq)
	if logoutRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", logoutRR.Code, logoutRR.Body.String())
	}

	rt, err := f.store.GetRefreshToken(loginReq.Context(), authsvc.HashRefreshToken(loginResp.RefreshToken))
	if err != nil {
		t.Fatalf("GetRefreshToken: %v", err)
	}
	if !rt.Revoked {
		t.Fatal("expected refresh token to be revoked")
	}
}

func TestWithAuthRejectsMissingBearer(t *testing.T) {
	f := newAuthFixture(t)
	h := f.service.WithAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), "admin")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.RemoteAddr = "127.0.0.1:45678"
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestIssueRefreshTokenSetsFutureExpiry(t *testing.T) {
	f := newAuthFixture(t)
	_, hash, expiry, err := f.service.IssueRefreshToken(time.Now().UTC())
	if err != nil {
		t.Fatalf("IssueRefreshToken: %v", err)
	}
	if hash == "" || !expiry.After(time.Now().UTC()) {
		t.Fatalf("unexpected refresh token result: hash=%q expiry=%v", hash, expiry)
	}
}

func TestHandleLoginLocksAccountAfterFailures(t *testing.T) {
	f := newAuthFixtureWithConfig(t, func(cfg *authsvc.Config) {
		cfg.LoginFailThreshold = 2
		cfg.LoginLockMinutes = 10
	})

	wrongReq1 := newJSONRequest(t, http.MethodPost, "/v1/auth/login", authtypes.LoginRequest{Email: f.cfg.BootstrapAdminEmail, Password: "wrong-password"})
	wrongRR1 := httptest.NewRecorder()
	f.service.HandleLogin(wrongRR1, wrongReq1)
	if wrongRR1.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for first failed login, got %d", wrongRR1.Code)
	}

	wrongReq2 := newJSONRequest(t, http.MethodPost, "/v1/auth/login", authtypes.LoginRequest{Email: f.cfg.BootstrapAdminEmail, Password: "wrong-password"})
	wrongRR2 := httptest.NewRecorder()
	f.service.HandleLogin(wrongRR2, wrongReq2)
	if wrongRR2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for second failed login, got %d", wrongRR2.Code)
	}

	goodReq := newJSONRequest(t, http.MethodPost, "/v1/auth/login", authtypes.LoginRequest{Email: f.cfg.BootstrapAdminEmail, Password: f.cfg.BootstrapAdminPassword})
	goodRR := httptest.NewRecorder()
	f.service.HandleLogin(goodRR, goodReq)
	if goodRR.Code != http.StatusLocked {
		t.Fatalf("expected 423 for locked account, got %d body=%s", goodRR.Code, goodRR.Body.String())
	}
}

func TestHandleLoginRateLimitedByIP(t *testing.T) {
	f := newAuthFixtureWithConfig(t, func(cfg *authsvc.Config) {
		cfg.LoginIPRateLimitPerMin = 1
	})

	firstReq := newJSONRequest(t, http.MethodPost, "/v1/auth/login", authtypes.LoginRequest{Email: f.cfg.BootstrapAdminEmail, Password: "wrong-password"})
	firstRR := httptest.NewRecorder()
	f.service.HandleLogin(firstRR, firstReq)
	if firstRR.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for first request, got %d", firstRR.Code)
	}

	secondReq := newJSONRequest(t, http.MethodPost, "/v1/auth/login", authtypes.LoginRequest{Email: f.cfg.BootstrapAdminEmail, Password: "wrong-password"})
	secondRR := httptest.NewRecorder()
	f.service.HandleLogin(secondRR, secondReq)
	if secondRR.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for rate limited request, got %d body=%s", secondRR.Code, secondRR.Body.String())
	}
}
