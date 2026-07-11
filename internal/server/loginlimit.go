package server

import (
	"sync"
	"time"
)

// loginLimiter counts recent login failures per client IP and blocks
// further attempts once a cap is hit, until the failures age out of the
// window. Deliberately in-process and approximate: nixbox binds loopback
// by default and PAM transactions are serialized anyway, so this only
// needs to make online guessing slow, not survive restarts.
type loginLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	now    func() time.Time // swappable in tests
	fails  map[string][]time.Time
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{max: max, window: window, now: time.Now, fails: map[string][]time.Time{}}
}

// blocked reports whether ip has reached the failure cap.
func (l *loginLimiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(ip)) >= l.max
}

// fail records a credentials failure for ip.
func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[ip] = append(l.prune(ip), l.now())
}

// clear forgets ip's failures (after a successful login).
func (l *loginLimiter) clear(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ip)
}

// prune drops failures older than the window, removing empty entries so
// the map cannot grow without bound. Callers hold the lock.
func (l *loginLimiter) prune(ip string) []time.Time {
	cutoff := l.now().Add(-l.window)
	var kept []time.Time
	for _, t := range l.fails[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.fails, ip)
	} else {
		l.fails[ip] = kept
	}
	return kept
}
