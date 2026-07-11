package server

import (
	"testing"
	"time"
)

func TestLoginLimiter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newLoginLimiter(3, time.Minute)
	l.now = func() time.Time { return now }

	if l.blocked("10.0.0.1") {
		t.Error("fresh ip should not be blocked")
	}
	for range 3 {
		l.fail("10.0.0.1")
	}
	if !l.blocked("10.0.0.1") {
		t.Error("ip at the failure cap should be blocked")
	}
	// Other clients are unaffected.
	if l.blocked("10.0.0.2") {
		t.Error("unrelated ip blocked")
	}

	// Failures age out of the window.
	now = now.Add(2 * time.Minute)
	if l.blocked("10.0.0.1") {
		t.Error("failures should age out")
	}
	// ...and the aged entry was pruned outright, not kept forever.
	l.mu.Lock()
	if _, ok := l.fails["10.0.0.1"]; ok {
		t.Error("pruned ip still tracked")
	}
	l.mu.Unlock()

	// A successful login clears the slate.
	l.fail("10.0.0.3")
	l.fail("10.0.0.3")
	l.fail("10.0.0.3")
	l.clear("10.0.0.3")
	if l.blocked("10.0.0.3") {
		t.Error("clear should reset the counter")
	}
}
