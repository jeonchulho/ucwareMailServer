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

// requestIDKey 는 컨텍스트에 Request ID를 저장·조회할 때 쓰는 타입 안전 키입니다.
type requestIDKey struct{}

// responseRecorder 는 http.ResponseWriter를 래핑하여
// 핸들러가 기록한 HTTP 상태 코드와 응답 바이트 수를 캡처합니다.
type responseRecorder struct {
	http.ResponseWriter
	status int // 핸들러가 WriteHeader로 기록한 상태 코드
	bytes  int // Write 호출로 전송된 바이트 합계
}

// WriteHeader 는 상태 코드를 캡처한 뒤 원래 ResponseWriter로 위임합니다.
func (r *responseRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Write 는 응답 바이트를 누적하고 원래 ResponseWriter로 위임합니다.
// WriteHeader 호출 없이 Write가 먼저 오면 200으로 초기화합니다.
func (r *responseRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += n
	return n, err
}

// WriteJSON 은 Content-Type을 application/json으로 설정하고 v를 JSON 직렬화하여 응답합니다.
func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// RequestIDFromContext 는 현재 컨텍스트에서 Request ID를 꺼냅니다.
// 핸들러 내부에서 로그 추적용으로 사용합니다.
func RequestIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(requestIDKey{}).(string)
	return strings.TrimSpace(v)
}

// LogMiddleware 는 모든 HTTP 요청에 X-Request-ID를 부여하고
// 처리 완료 후 JSON 구조화 액세스 로그를 출력하는 미들웨어입니다.
// 인바운드 X-Request-ID 헤더가 있으면 그것을 사용하고, 없으면 새로 생성합니다.
func LogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 클라이언트 또는 상위 프록시가 보낸 Request ID를 우선 사용
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" {
			requestID = newRequestID()
		}
		// 응답 헤더에도 Request ID를 포함 (클라이언트에서 추적 가능)
		w.Header().Set("X-Request-ID", requestID)
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, requestID))

		// 상태 코드와 바이트 수를 캡처하기 위해 ResponseWriter를 래핑
		rw := &responseRecorder{ResponseWriter: w}
		started := time.Now()
		next.ServeHTTP(rw, r)
		if rw.status == 0 {
			rw.status = http.StatusOK
		}

		// JSON 구조화 액세스 로그 출력 (로그 집계 시스템 파싱 용이)
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
			// JSON 직렬화 실패 시 폴백으로 텍스트 로그 출력
			log.Printf("method=%s path=%s status=%d duration_ms=%d", r.Method, r.URL.Path, rw.status, time.Since(started).Milliseconds())
		}
	})
}

// newRequestID 는 암호학적으로 안전한 12바이트 난수를 hex 인코딩하여 Request ID를 생성합니다.
// crand 실패 시 타임스탬프 기반 폴백을 사용합니다.
func newRequestID() string {
	b := make([]byte, 12)
	if _, err := crand.Read(b); err != nil {
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(b)
}

// requestRemoteIP 는 프록시 환경에서도 실제 클라이언트 IP를 추출합니다.
// X-Forwarded-For → X-Real-IP → RemoteAddr 순서로 시도합니다.
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
