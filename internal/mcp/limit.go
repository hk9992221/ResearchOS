package mcp

import (
	"sync"
	"time"

	"researchos/internal/foundation/clock"
)

// Payload limits of SPEC-11 §5.7.
const (
	// MaxRequestBytes is the per-request payload cap.
	MaxRequestBytes = 1 << 20
	// MaxBodyBytes is the cap for note_save_body/paper_note_save_body bodies.
	MaxBodyBytes = 5 << 20
	// MaxBodyToolOverhead is the slack a body-bearing request may use beyond
	// MaxBodyBytes for the surrounding JSON envelope.
	MaxBodyToolOverhead = 64 << 10
)

// bodyArgumentNames are the argument names subject to MaxBodyBytes.
var bodyArgumentNames = map[string]bool{
	"body_md":    true,
	"content_md": true,
}

// Limiter is the per-connection token bucket of §5.7: 60 calls per minute with a
// burst of 10. Buckets refill one token per second.
type Limiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	clock   clock.Clock
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewLimiter builds the default limiter (60/min, burst 10). perMinute and burst
// below one fall back to the defaults.
func NewLimiter(perMinute, burst int, c clock.Clock) *Limiter {
	if perMinute <= 0 {
		perMinute = 60
	}
	if burst <= 0 {
		burst = 10
	}
	if c == nil {
		c = clock.System{}
	}
	return &Limiter{
		rate:    float64(perMinute) / 60,
		burst:   float64(burst),
		clock:   c,
		buckets: make(map[string]*bucket),
	}
}

// Allow reports whether key may make another call. When it may not, the second
// result is the retry delay.
func (l *Limiter) Allow(key string) (time.Duration, bool) {
	if l == nil {
		return 0, true
	}
	if key == "" {
		key = "anonymous"
	}
	now := l.clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return 0, true
	}
	needed := (1 - b.tokens) / l.rate
	return time.Duration(needed * float64(time.Second)), false
}

// Forget drops the bucket of a disconnected key.
func (l *Limiter) Forget(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	delete(l.buckets, key)
	l.mu.Unlock()
}

// PayloadTooLarge applies the §5.7 size rules to one arguments payload.
func PayloadTooLarge(arguments map[string]any, total int) bool {
	if total > MaxRequestBytes {
		bodyBearer := false
		for name := range arguments {
			if bodyArgumentNames[name] {
				bodyBearer = true
				break
			}
		}
		if !bodyBearer || total > MaxBodyBytes+MaxBodyToolOverhead {
			return true
		}
	}
	for name, value := range bodyArgumentNames {
		if !value {
			continue
		}
		if s, ok := arguments[name].(string); ok && len(s) > MaxBodyBytes {
			return true
		}
	}
	return false
}
