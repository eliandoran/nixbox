//go:build cgo

package auth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/msteinert/pam/v2"
)

// pamConfDir writes throwaway PAM service files wired to the module dir
// the devShell exports (NIXBOX_PAM_TEST_MODULES → pam_permit.so and
// friends), so these tests run real PAM transactions without touching
// /etc/pam.d or needing root. The success path in the VM differs only in
// which modules the stack lists.
func pamConfDir(t *testing.T) string {
	t.Helper()
	modules := os.Getenv("NIXBOX_PAM_TEST_MODULES")
	if modules == "" {
		t.Skip("NIXBOX_PAM_TEST_MODULES not set (enter the devShell for real-PAM tests)")
	}
	if !pam.CheckPamHasStartConfdir() {
		t.Skip("libpam too old for pam_start_confdir")
	}
	dir := t.TempDir()
	permit := filepath.Join(modules, "pam_permit.so")
	deny := filepath.Join(modules, "pam_deny.so")
	for service, stack := range map[string]string{
		"nixbox-permit":  "auth required " + permit + "\naccount required " + permit + "\n",
		"nixbox-deny":    "auth required " + deny + "\naccount required " + deny + "\n",
		"nixbox-expired": "auth required " + permit + "\naccount required " + deny + "\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, service), []byte(stack), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestPAMAuthenticate(t *testing.T) {
	dir := pamConfDir(t)

	if err := (&PAM{Service: "nixbox-permit", ConfDir: dir}).Authenticate("someone", "pw"); err != nil {
		t.Errorf("permit stack: %v", err)
	}
	err := (&PAM{Service: "nixbox-deny", ConfDir: dir}).Authenticate("someone", "pw")
	if !errors.Is(err, ErrBadCredentials) {
		t.Errorf("deny stack: err = %v, want ErrBadCredentials", err)
	}
	// Password accepted but the account stage rejects (expired/locked):
	// still a login failure.
	err = (&PAM{Service: "nixbox-expired", ConfDir: dir}).Authenticate("someone", "pw")
	if !errors.Is(err, ErrBadCredentials) {
		t.Errorf("expired stack: err = %v, want ErrBadCredentials", err)
	}
	// A service with no config file must fail (PAM's default is deny),
	// never pass silently.
	if err := (&PAM{Service: "nixbox-missing", ConfDir: dir}).Authenticate("someone", "pw"); err == nil {
		t.Error("missing service file: want an error")
	}
}

// TestPAMSystemConfDir exercises the ConfDir=="" branch against the
// machine's real /etc/pam.d — read-only, and the service name is chosen
// to not exist, so PAM's "other" fallback (or a start failure) denies.
func TestPAMSystemConfDir(t *testing.T) {
	err := (&PAM{Service: "nixbox-test-no-such-service-c41f"}).Authenticate("someone", "pw")
	if err == nil {
		t.Error("unknown system service: want an error")
	}
}

func TestPAMEmptyCredentials(t *testing.T) {
	// Rejected before any PAM call — no conf dir or modules needed.
	p := &PAM{Service: "nixbox"}
	for _, c := range [][2]string{{"", "pw"}, {"someone", ""}, {"", ""}} {
		if err := p.Authenticate(c[0], c[1]); !errors.Is(err, ErrBadCredentials) {
			t.Errorf("Authenticate(%q, %q) = %v, want ErrBadCredentials", c[0], c[1], err)
		}
	}
}

func TestPasswordConv(t *testing.T) {
	conv := passwordConv("s3cret")

	if got, err := conv(pam.PromptEchoOff, "Password: "); err != nil || got != "s3cret" {
		t.Errorf("echo-off prompt: %q, %v", got, err)
	}
	// Informational and error messages are acknowledged, not answered.
	for _, s := range []pam.Style{pam.ErrorMsg, pam.TextInfo} {
		if got, err := conv(s, "notice"); err != nil || got != "" {
			t.Errorf("style %v: %q, %v", s, got, err)
		}
	}
	// An echoing prompt asks for something that is not a password —
	// refuse rather than leak the one secret we hold.
	if _, err := conv(pam.PromptEchoOn, "Username: "); err == nil {
		t.Error("echo-on prompt: want a refusal")
	}
}
