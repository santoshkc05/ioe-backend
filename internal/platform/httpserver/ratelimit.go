package httpserver

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

const (
	rateLimitSweepEvery = time.Minute
	rateLimitIdleTTL    = 10 * time.Minute
)

// RateLimiter is an in-memory, per-key token bucket. It is per process; multi-instance
// deployments need a shared limiter.
type RateLimiter struct {
	mu        sync.Mutex
	perMinute int
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

func NewRateLimiter(perMinute int) *RateLimiter {
	return &RateLimiter{perMinute: perMinute, buckets: map[string]*bucket{}, lastSweep: time.Now()}
}

// Allow reports whether one more request for key is permitted now.
func (l *RateLimiter) Allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	if now.Sub(l.lastSweep) > rateLimitSweepEvery {
		for k, b := range l.buckets {
			if now.Sub(b.seen) > rateLimitIdleTTL {
				delete(l.buckets, k)
			}
		}
		l.lastSweep = now
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(rate.Every(time.Minute/time.Duration(l.perMinute)), l.perMinute)}
		l.buckets[key] = b
	}
	b.seen = now
	l.mu.Unlock()
	return b.lim.AllowN(now, 1)
}

// Middleware limits requests per client IP.
func (l *RateLimiter) Middleware(ips IPResolver) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(ips.ClientIP(r)) {
				w.Header().Set("Retry-After", strconv.Itoa(60/l.perMinute+1))
				problem.Write(w, r, http.StatusTooManyRequests, problem.TypeRateLimited, "Too Many Requests", "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
