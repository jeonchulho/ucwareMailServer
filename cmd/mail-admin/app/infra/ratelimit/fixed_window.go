package ratelimit

import (
	"strings"
	"sync"
	"time"
)

type bucket struct {
	windowStart time.Time
	count       int
}

type FixedWindow struct {
	mu        sync.Mutex
	buckets   map[string]bucket
	max       int
	windowDur time.Duration
}

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

func (l *FixedWindow) Allow(key string, now time.Time) bool {
	key = strings.TrimSpace(strings.ToLower(key))
	if key == "" {
		key = "global"
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok || now.Sub(b.windowStart) >= l.windowDur {
		l.buckets[key] = bucket{windowStart: now, count: 1}
		return true
	}

	if b.count >= l.max {
		return false
	}
	b.count++
	l.buckets[key] = b
	return true
}
