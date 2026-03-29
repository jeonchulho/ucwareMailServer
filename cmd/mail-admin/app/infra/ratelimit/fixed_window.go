package ratelimit

import (
	"strings"
	"sync"
	"time"
)

// bucket 은 특정 키(IP·이메일 등)의 현재 윈도우 시작 시각과 누적 요청 횟수를 저장합니다.
type bucket struct {
	windowStart time.Time // 현재 고정 윈도우가 시작된 시각
	count       int       // 해당 윈도우 내에서 누적된 요청 횟수
}

// FixedWindow 는 고정 슬라이딩 윈도우 방식의 레이트리밋 구현체입니다.
// max 횟수를 초과하면 windowDur 이 지날 때까지 추가 요청을 거부합니다.
type FixedWindow struct {
	mu        sync.Mutex        // 동시 접근을 막기 위한 뮤텍스
	buckets   map[string]bucket // 키별 버킷 맵 (동적 생성)
	max       int               // 윈도우 당 허용 최대 요청 횟수
	windowDur time.Duration     // 윈도우 크기 (예: 1분)
}

// NewFixedWindow 는 새로운 고정 윈도우 제한기를 생성합니다.
// max < 1 이거나 windowDur <= 0 이면 안전한 기본값(max=1, dur=1분)으로 보정합니다.
func NewFixedWindow(max int, windowDur time.Duration) *FixedWindow {
	if max < 1 {
		max = 1
	}
	if windowDur <= 0 {
		windowDur = time.Minute
	}
	return &FixedWindow{
		buckets:   make(map[string]bucket),
		max:       max,
		windowDur: windowDur,
	}
}

// Allow 는 주어진 키에 대해 현재 요청을 허용할지 결정합니다.
// 윈도우가 만료됐으면 새 윈도우를 시작하고 허용합니다.
// 윈도우가 살아있고 max 이하면 카운터를 증가시키고 허용합니다.
// max를 초과하면 false를 반환하여 요청을 거부합니다.
func (l *FixedWindow) Allow(key string, now time.Time) bool {
	key = strings.TrimSpace(strings.ToLower(key))
	if key == "" {
		key = "global" // 빈 키는 전역 버킷으로 귀결
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok || now.Sub(b.windowStart) >= l.windowDur {
		// 버킷 없음 또는 윈도우 만료 → 새 윈도우로 초기화
		l.buckets[key] = bucket{windowStart: now, count: 1}
		return true
	}

	if b.count >= l.max {
		return false // 현재 윈도우 내 허용 횟수 초과
	}
	b.count++
	l.buckets[key] = b
	return true
}
