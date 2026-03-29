package test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	authtypes "github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/auth/types"
	"github.com/pquerna/otp/totp"
)

func TestHandleTOTPSetupConfirmAndChallenge(t *testing.T) {
	f := newAuthFixture(t)
	actorEmail := f.cfg.BootstrapAdminEmail

	setupReq := withActor(httptest.NewRequest(http.MethodPost, "/v1/auth/totp/setup", nil), actorEmail)
	setupReq.RemoteAddr = "127.0.0.1:56789"
	setupRR := httptest.NewRecorder()
	f.service.HandleTOTPSetup(setupRR, setupReq)
	if setupRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", setupRR.Code, setupRR.Body.String())
	}
	setupResp := decodeJSON[authtypes.TOTPSetupResponse](t, setupRR)
	code, err := totp.GenerateCode(setupResp.Secret, time.Now().UTC())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}

	confirmReq := withActor(newJSONRequest(t, http.MethodPost, "/v1/auth/totp/confirm", authtypes.TOTPVerifyRequest{Code: code}), actorEmail)
	confirmRR := httptest.NewRecorder()
	f.service.HandleTOTPConfirm(confirmRR, confirmReq)
	if confirmRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", confirmRR.Code, confirmRR.Body.String())
	}

	loginReq := newJSONRequest(t, http.MethodPost, "/v1/auth/login", authtypes.LoginRequest{Email: actorEmail, Password: f.cfg.BootstrapAdminPassword})
	loginRR := httptest.NewRecorder()
	f.service.HandleLogin(loginRR, loginReq)
	if loginRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", loginRR.Code, loginRR.Body.String())
	}
	challengeResp := decodeJSON[authtypes.TOTPChallengeResponse](t, loginRR)
	if challengeResp.Status != "totp_required" || challengeResp.ChallengeToken == "" {
		t.Fatalf("unexpected challenge response: %+v", challengeResp)
	}

	challengeCode, err := totp.GenerateCode(setupResp.Secret, time.Now().UTC())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	challengeReq := newJSONRequest(t, http.MethodPost, "/v1/auth/totp/challenge", authtypes.TOTPChallengeRequest{ChallengeToken: challengeResp.ChallengeToken, Code: challengeCode})
	challengeRR := httptest.NewRecorder()
	f.service.HandleTOTPChallenge(challengeRR, challengeReq)
	if challengeRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", challengeRR.Code, challengeRR.Body.String())
	}
	loginResp := decodeJSON[authtypes.LoginResponse](t, challengeRR)
	if loginResp.AccessToken == "" || loginResp.RefreshToken == "" {
		t.Fatalf("expected tokens, got %+v", loginResp)
	}
}
