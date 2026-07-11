// Package auth verifies who is using nixbox and whether they are allowed
// to. Authentication (is this password right?) is delegated to the host's
// PAM stack — nixbox never stores credentials of its own — and
// authorization (may this user administer the machine?) is a Unix group
// check, because a PAM-valid login alone would hand every local account a
// root-equivalent UI.
package auth

import "errors"

// ErrBadCredentials means the username/password pair was rejected. It is
// deliberately one error for unknown user, wrong password, and disabled
// account: the login form shows the same message for all three.
var ErrBadCredentials = errors.New("bad credentials")

// ErrNotAuthorized means the credentials were valid but the user is not
// in any allowed group.
var ErrNotAuthorized = errors.New("user not in an allowed group")

// Authenticator verifies a username/password pair. Implementations
// return ErrBadCredentials for a rejection; any other error is a backend
// failure.
type Authenticator interface {
	Authenticate(username, password string) error
}

// Authorizer decides whether an authenticated user may use nixbox.
type Authorizer interface {
	Authorize(username string) error
}
