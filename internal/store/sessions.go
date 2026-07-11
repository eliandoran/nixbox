package store

import (
	"database/sql"
	"errors"
	"time"
)

// Session is one logged-in browser. The cookie holds the random token;
// only its SHA-256 is stored, so a leaked database cannot be replayed
// into a live session.
type Session struct {
	TokenHash string
	Username  string
	CreatedAt time.Time
	ExpiresAt time.Time
}

func (s *Store) CreateSession(tokenHash, username string, expiresAt time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO sessions (token_hash, username, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		tokenHash, username, time.Now().UTC(), expiresAt.UTC())
	return err
}

// ValidSession returns the session for a token hash if it has not expired
// at the given instant; a missing and an expired session are both
// ErrNotFound (the caller treats them identically, and answering
// differently would only help someone probing tokens).
func (s *Store) ValidSession(tokenHash string, now time.Time) (*Session, error) {
	var sess Session
	err := s.db.QueryRow(
		`SELECT token_hash, username, created_at, expires_at FROM sessions
		 WHERE token_hash = ? AND expires_at > ?`, tokenHash, now.UTC()).
		Scan(&sess.TokenHash, &sess.Username, &sess.CreatedAt, &sess.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// TouchSession pushes a session's deadline out (sliding expiry).
func (s *Store) TouchSession(tokenHash string, expiresAt time.Time) error {
	_, err := s.db.Exec(`UPDATE sessions SET expires_at = ? WHERE token_hash = ?`,
		expiresAt.UTC(), tokenHash)
	return err
}

func (s *Store) DeleteSession(tokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// DeleteExpiredSessions drops rows whose deadline has passed. Called
// opportunistically (startup, logins) rather than on a timer — expired
// rows are already unusable via ValidSession; this just stops them
// accumulating.
func (s *Store) DeleteExpiredSessions(now time.Time) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, now.UTC())
	return err
}
