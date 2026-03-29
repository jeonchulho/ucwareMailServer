package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLogMiddlewareGeneratesRequestID(t *testing.T) {
	h := LogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := RequestIDFromContext(r.Context())
		if reqID == "" {
			t.Fatal("expected request id in context")
		}
		w.Header().Set("X-Handler-Request-ID", reqID)
		w.WriteHeader(http.StatusNoContent)
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
	respReqID := rr.Header().Get("X-Request-ID")
	if respReqID == "" {
		t.Fatal("expected X-Request-ID response header")
	}
	if rr.Header().Get("X-Handler-Request-ID") != respReqID {
		t.Fatalf("handler context request id mismatch: handler=%q response=%q", rr.Header().Get("X-Handler-Request-ID"), respReqID)
	}
}

func TestLogMiddlewarePreservesInboundRequestID(t *testing.T) {
	const inbound = "req-from-client-123"

	h := LogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := RequestIDFromContext(r.Context()); got != inbound {
			t.Fatalf("expected context request id %q, got %q", inbound, got)
		}
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("X-Request-ID", inbound)
	req.RemoteAddr = "127.0.0.1:54321"
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if got := rr.Header().Get("X-Request-ID"); got != inbound {
		t.Fatalf("expected response request id %q, got %q", inbound, got)
	}
}
