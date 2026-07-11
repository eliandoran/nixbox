//go:build !cgo

package auth

import "errors"

// PAM in a CGO_ENABLED=0 build: the real backend needs cgo + libpam, so
// this stub fails closed — a pure-Go binary configured with
// NIXBOX_AUTH=pam rejects every login instead of silently allowing any.
// Production builds (nix/package.nix) always enable cgo.
type PAM struct {
	Service string
	ConfDir string
}

var errNoPAM = errors.New("nixbox was built without cgo: PAM authentication is unavailable")

func (p *PAM) Authenticate(username, password string) error { return errNoPAM }
