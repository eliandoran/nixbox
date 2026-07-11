package store

import (
	"testing"
	"time"
)

func TestSessionLifecycle(t *testing.T) {
	s := open(t)
	now := time.Now().UTC()

	if err := s.CreateSession("h1", "root", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	sess, err := s.ValidSession("h1", now)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Username != "root" || sess.TokenHash != "h1" {
		t.Errorf("unexpected session: %+v", sess)
	}
	if sess.CreatedAt.IsZero() || !sess.ExpiresAt.After(now) {
		t.Errorf("timestamps not persisted: %+v", sess)
	}

	// The token hash is the primary key: a duplicate insert must fail
	// rather than silently adopt another user's session.
	if err := s.CreateSession("h1", "eve", now.Add(time.Hour)); err == nil {
		t.Error("duplicate token hash should fail")
	}

	if _, err := s.ValidSession("missing", now); err != ErrNotFound {
		t.Errorf("unknown hash: err = %v, want ErrNotFound", err)
	}
	// An expired session is indistinguishable from a missing one.
	if _, err := s.ValidSession("h1", now.Add(2*time.Hour)); err != ErrNotFound {
		t.Errorf("expired session: err = %v, want ErrNotFound", err)
	}

	// Sliding expiry: touching moves the deadline out.
	if err := s.TouchSession("h1", now.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidSession("h1", now.Add(2*time.Hour)); err != nil {
		t.Errorf("touched session should be valid again: %v", err)
	}

	if err := s.DeleteSession("h1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidSession("h1", now); err != ErrNotFound {
		t.Errorf("deleted session: err = %v, want ErrNotFound", err)
	}
	// Deleting an already-gone session is a no-op, not an error (logout
	// with a stale cookie).
	if err := s.DeleteSession("h1"); err != nil {
		t.Errorf("double delete: %v", err)
	}
}

func TestDeleteExpiredSessions(t *testing.T) {
	s := open(t)
	now := time.Now().UTC()

	if err := s.CreateSession("old", "root", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession("live", "root", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteExpiredSessions(now); err != nil {
		t.Fatal(err)
	}
	// The old row is gone outright — even a lookup dated before its
	// expiry no longer finds it.
	if _, err := s.ValidSession("old", now.Add(-2*time.Hour)); err != ErrNotFound {
		t.Errorf("expired session survived the sweep: %v", err)
	}
	if _, err := s.ValidSession("live", now); err != nil {
		t.Errorf("live session swept: %v", err)
	}
}

func TestSessionScanFault(t *testing.T) {
	s := open(t)
	now := time.Now().UTC()
	if err := s.CreateSession("h1", "root", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE sessions SET created_at = ?`, corruptTime); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidSession("h1", now); err == nil || err == ErrNotFound {
		t.Errorf("corrupt timestamp: err = %v, want scan error", err)
	}
}

func TestSessionClosedDB(t *testing.T) {
	s := open(t)
	now := time.Now().UTC()
	s.Close()

	if err := s.CreateSession("h", "root", now); err == nil {
		t.Error("CreateSession on closed db should fail")
	}
	if _, err := s.ValidSession("h", now); err == nil || err == ErrNotFound {
		t.Errorf("ValidSession on closed db: err = %v, want driver error", err)
	}
	if err := s.TouchSession("h", now); err == nil {
		t.Error("TouchSession on closed db should fail")
	}
	if err := s.DeleteSession("h"); err == nil {
		t.Error("DeleteSession on closed db should fail")
	}
	if err := s.DeleteExpiredSessions(now); err == nil {
		t.Error("DeleteExpiredSessions on closed db should fail")
	}
}
