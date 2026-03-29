package test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleMailboxesDisabledArchive(t *testing.T) {
	f := newFixture(t)
	rr := httptest.NewRecorder()
	req := withActor(httptest.NewRequest(http.MethodGet, "/v1/mailboxes", nil), "viewer@example.com", "viewer")
	f.service.HandleMailboxes(rr, req)
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleMessagesDisabledArchive(t *testing.T) {
	f := newFixture(t)
	rr := httptest.NewRecorder()
	req := withActor(httptest.NewRequest(http.MethodGet, "/v1/messages?mailboxId=x", nil), "viewer@example.com", "viewer")
	f.service.HandleMessages(rr, req)
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d body=%s", rr.Code, rr.Body.String())
	}
}
