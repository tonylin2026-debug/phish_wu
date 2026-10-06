package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestCleanupRunsOnTheConfiguredInterval asserts that the background cleanup
// goroutine actually fires.
//
// pollCleanup built its ticker by multiplying limiter.cleanupInterval by
// time.Second, but cleanupInterval is already a time.Duration. The default is
// one minute, so the conversion multiplied 60000000000 by 1000000000,
// overflowed int64 and wrapped to roughly 147 years. The ticker therefore
// never fired: Cleanup was dead code and the visitors map grew without bound,
// one bucket per source address, for the lifetime of the process.
func TestCleanupRunsOnTheConfiguredInterval(t *testing.T) {
	limiter := NewPostLimiter(
		WithCleanupInterval(5*time.Millisecond),
		WithExpiry(5*time.Millisecond),
	)
	handler := limiter.Limit(successHandler)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if got := visitorCount(limiter); got != 1 {
		t.Fatalf("expected the request to register one visitor, got %d", got)
	}

	// Generous relative to a 5ms interval, so this is not a timing race: if
	// the ticker fires at all, it has fired many times over by now.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if visitorCount(limiter) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the stale visitor was never cleaned up; the cleanup ticker does not fire")
}

func visitorCount(limiter *PostLimiter) int {
	limiter.RLock()
	defer limiter.RUnlock()
	return len(limiter.visitors)
}
