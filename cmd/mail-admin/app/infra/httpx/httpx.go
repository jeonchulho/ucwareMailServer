package httpx

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

type requestIDKey struct{}

type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *responseRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += n
	return n, err
}

func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func RequestIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(requestIDKey{}).(string)
	return strings.TrimSpace(v)
}

func LogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" {
			requestID = newRequestID()
		}
		w.Header().Set("X-Request-ID", requestID)
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, requestID))

		rw := &responseRecorder{ResponseWriter: w}
		started := time.Now()
		next.ServeHTTP(rw, r)
		if rw.status == 0 {
			rw.status = http.StatusOK
		}

		entry := map[string]any{
			"ts":          time.Now().UTC().Format(time.RFC3339Nano),
			"request_id":  requestID,
			"method":      r.Method,
			"path":        r.URL.Path,
			"status":      rw.status,
			"bytes":       rw.bytes,
			"duration_ms": time.Since(started).Milliseconds(),
			"remote_ip":   requestRemoteIP(r),
			"user_agent":  r.UserAgent(),
		}
		if raw := strings.TrimSpace(r.URL.RawQuery); raw != "" {
			entry["query"] = raw
		}
		if b, err := json.Marshal(entry); err == nil {
			log.Print(string(b))
		} else {
			log.Printf("method=%s path=%s status=%d duration_ms=%d", r.Method, r.URL.Path, rw.status, time.Since(started).Milliseconds())
		}
	})
}

func newRequestID() string {
	b := make([]byte, 12)
	if _, err := crand.Read(b); err != nil {
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(b)
}

func requestRemoteIP(r *http.Request) string {
	xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if xff != "" {
		first := strings.TrimSpace(strings.Split(xff, ",")[0])
		if first != "" {
			return first
		}
	}
	xri := strings.TrimSpace(r.Header.Get("X-Real-IP"))
	if xri != "" {
		return xri
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}
