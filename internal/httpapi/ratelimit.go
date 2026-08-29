package httpapi

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

type rateBucket struct {
	resetAt time.Time
	count   int
}
type rateLimiter struct {
	mu             sync.Mutex
	buckets        map[string]rateBucket
	trustedProxies []netip.Prefix
	nextSweep      time.Time
}

const maxRateLimitBuckets = 10000

func newRateLimiter(trustedProxyCIDRs ...string) *rateLimiter {
	limiter := &rateLimiter{buckets: map[string]rateBucket{}}
	for _, raw := range trustedProxyCIDRs {
		if prefix, err := netip.ParsePrefix(strings.TrimSpace(raw)); err == nil {
			limiter.trustedProxies = append(limiter.trustedProxies, prefix)
		}
	}
	return limiter
}
func (l *rateLimiter) allow(key string, limit int, window time.Duration) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.nextSweep.IsZero() || !now.Before(l.nextSweep) {
		for candidate, bucket := range l.buckets {
			if !now.Before(bucket.resetAt) {
				delete(l.buckets, candidate)
			}
		}
		l.nextSweep = now.Add(time.Minute)
	}
	bucket := l.buckets[key]
	if bucket.resetAt.IsZero() || !now.Before(bucket.resetAt) {
		if _, exists := l.buckets[key]; !exists && len(l.buckets) >= maxRateLimitBuckets {
			return false
		}
		l.buckets[key] = rateBucket{resetAt: now.Add(window), count: 1}
		return true
	}
	if bucket.count >= limit {
		return false
	}
	bucket.count++
	l.buckets[key] = bucket
	return true
}

func (l *rateLimiter) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil || !l.isTrustedProxy(remote) {
		return host
	}
	forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for index := len(forwarded) - 1; index >= 0; index-- {
		candidate, err := netip.ParseAddr(strings.TrimSpace(forwarded[index]))
		if err != nil {
			continue
		}
		if !l.isTrustedProxy(candidate) {
			return candidate.String()
		}
	}
	return remote.String()
}

func (l *rateLimiter) isTrustedProxy(address netip.Addr) bool {
	for _, prefix := range l.trustedProxies {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
