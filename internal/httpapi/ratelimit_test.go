package httpapi

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiterRejectsAfterLimit(t *testing.T) {
	limiter := newRateLimiter()
	if !limiter.allow("key", 2, time.Minute) || !limiter.allow("key", 2, time.Minute) {
		t.Fatal("expected first requests to pass")
	}
	if limiter.allow("key", 2, time.Minute) {
		t.Fatal("expected request over limit to fail")
	}
}

func TestClientIPTrustsForwardedHeaderOnlyFromConfiguredProxy(t *testing.T) {
	limiter := newRateLimiter("172.16.0.0/12")
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "172.18.0.4:54321"
	request.Header.Set("X-Forwarded-For", "203.0.113.7")
	if got := limiter.clientIP(request); got != "203.0.113.7" {
		t.Fatalf("clientIP() = %q", got)
	}

	request.RemoteAddr = "198.51.100.4:54321"
	request.Header.Set("X-Forwarded-For", "203.0.113.8")
	if got := limiter.clientIP(request); got != "198.51.100.4" {
		t.Fatalf("untrusted proxy header was accepted: %q", got)
	}
}

func TestRateLimiterEvictsExpiredBuckets(t *testing.T) {
	limiter := newRateLimiter()
	limiter.buckets["expired"] = rateBucket{resetAt: time.Now().Add(-time.Second), count: 1}
	limiter.nextSweep = time.Now().Add(-time.Second)
	if !limiter.allow("current", 1, time.Minute) {
		t.Fatal("expected current request to pass")
	}
	if _, exists := limiter.buckets["expired"]; exists {
		t.Fatal("expired bucket was not evicted")
	}
}
