package api

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Rate limits (spec 17). One token bucket per principal, held in memory.
// ponytail: per process, so N API instances allow N times the rate; move to the database or a shared store when there is more than one.

// RateLimit is requests per second and burst size for one principal.
type RateLimit struct {
	PerSecond float64
	Burst     int
}

// Limits holds the rate for signed-in users (operators and the console) and the
// tighter rate for external systems that submit orders and ASNs with a token.
// A zero PerSecond turns that limit off.
type Limits struct {
	User, System RateLimit
}

// DefaultLimits suit one warehouse: a scanner sends a request or two a second.
func DefaultLimits() Limits {
	return Limits{User: RateLimit{PerSecond: 50, Burst: 100}, System: RateLimit{PerSecond: 10, Burst: 20}}
}

// WithLimits sets the rate limits. Load tests turn them off.
func WithLimits(l Limits) Option { return func(s *Server) { s.limits = l } }

type bucket struct {
	tokens float64
	last   time.Time
}

type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	swept   time.Time
}

// allow takes one token for key. It returns false and how long to wait when empty.
func (l *limiter) allow(key string, r RateLimit, now time.Time) (bool, time.Duration) {
	if r.PerSecond <= 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = map[string]*bucket{}
	}
	if now.Sub(l.swept) > time.Minute { // forget principals that have been idle long enough to be full again
		for k, b := range l.buckets {
			if now.Sub(b.last) > time.Minute {
				delete(l.buckets, k)
			}
		}
		l.swept = now
	}
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: float64(r.Burst), last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(float64(r.Burst), b.tokens+now.Sub(b.last).Seconds()*r.PerSecond)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / r.PerSecond * float64(time.Second))
}

// tooMany writes the 429 and tells the caller when to retry.
func tooMany(w http.ResponseWriter, wait time.Duration) *httpErr {
	w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
	return &httpErr{http.StatusTooManyRequests, "too many requests", "slow down and retry after the time in Retry-After"}
}
