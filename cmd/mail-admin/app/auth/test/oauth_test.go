package test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/oauth2"
)

func TestHandleOAuth2StartReturnsNotImplementedWhenProviderMissing(t *testing.T) {
	f := newAuthFixture(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/oauth2/google", nil)
	f.service.HandleOAuth2Start("google")(rr, req)
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleOAuth2CallbackRequiresState(t *testing.T) {
	f := newAuthFixture(t)
	f.service.SetOAuth2Configs(&oauth2.Config{ClientID: "id", ClientSecret: "secret", RedirectURL: "http://localhost/callback", Endpoint: oauth2.Endpoint{AuthURL: "https://example.com/auth", TokenURL: "https://example.com/token"}}, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/oauth2/google/callback", nil)
	f.service.HandleOAuth2Callback("google")(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}
