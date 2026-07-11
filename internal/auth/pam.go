//go:build cgo

package auth

import (
	"fmt"
	"sync"

	"github.com/msteinert/pam/v2"
)

// PAM authenticates against the host's PAM stack. In production the
// NixOS module declares security.pam.services.nixbox, which generates a
// standard pam_unix service file — the same integration Cockpit uses.
// nixbox runs as root, so pam_unix reads /etc/shadow directly.
type PAM struct {
	// Service is the PAM service name (the /etc/pam.d file consulted).
	Service string
	// ConfDir points PAM at an alternate service-file directory; tests
	// script permit/deny stacks there. Empty means the system /etc/pam.d.
	ConfDir string

	// One transaction at a time: libpam module state is not reliably
	// reentrant, and serializing logins doubles as a brute-force brake
	// on top of the per-IP limiter.
	mu sync.Mutex
}

func (p *PAM) Authenticate(username, password string) error {
	if username == "" || password == "" {
		return fmt.Errorf("empty credentials: %w", ErrBadCredentials)
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	conv := pam.ConversationFunc(passwordConv(password))
	var tx *pam.Transaction
	var err error
	if p.ConfDir != "" {
		tx, err = pam.StartConfDir(p.Service, username, conv, p.ConfDir)
	} else {
		tx, err = pam.Start(p.Service, username, conv)
	}
	if err != nil {
		return fmt.Errorf("starting pam service %q: %w", p.Service, err)
	}
	defer tx.End()

	if err := tx.Authenticate(0); err != nil {
		return fmt.Errorf("%w: %s", ErrBadCredentials, err)
	}
	// The account stage (expiry, lockout) is part of "may this user log
	// in" even after the password matched.
	if err := tx.AcctMgmt(0); err != nil {
		return fmt.Errorf("%w: %s", ErrBadCredentials, err)
	}
	return nil
}

// passwordConv answers PAM's conversation: the password for the hidden
// prompt, an acknowledgement for informational messages, and a refusal
// for any echoing prompt — nixbox holds exactly one secret, and a module
// asking for anything else (a username, an OTP we don't have) must fail
// rather than receive the password.
func passwordConv(password string) func(pam.Style, string) (string, error) {
	return func(s pam.Style, msg string) (string, error) {
		switch s {
		case pam.PromptEchoOff:
			return password, nil
		case pam.ErrorMsg, pam.TextInfo:
			return "", nil
		default: // PromptEchoOn and anything unknown
			return "", fmt.Errorf("refusing pam prompt style %d (%q)", s, msg)
		}
	}
}
