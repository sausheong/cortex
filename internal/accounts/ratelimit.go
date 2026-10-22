package accounts

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Limiter is an in-memory token-bucket limiter keyed by string.
type Limiter struct {
	mu      sync.Mutex
	perMin  float64
	burst   float64
	now     func() time.Time
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewLimiter allows perMin requests per minute per key with the given burst.
func NewLimiter(perMin, burst int, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{perMin: float64(perMin), burst: float64(burst), now: now, buckets: map[string]*bucket{}}
}

// Allow consumes one token for key and reports whether it was available.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.now()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: t}
		l.buckets[key] = b
		if len(l.buckets) > 10000 { // bound memory: drop idle full buckets
			for k, v := range l.buckets {
				if k != key && t.Sub(v.last) > time.Hour {
					delete(l.buckets, k)
				}
			}
		}
	}
	b.tokens += t.Sub(b.last).Seconds() * l.perMin / 60
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = t
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// ClientIP returns RemoteAddr's host, except when the peer is loopback
// (the reverse proxy), where it is the last X-Forwarded-For entry.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
				return last
			}
		}
	}
	return host
}
