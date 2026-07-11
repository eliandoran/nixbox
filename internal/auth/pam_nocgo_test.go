//go:build !cgo

package auth

import "testing"

func TestPAMStubFailsClosed(t *testing.T) {
	if err := (&PAM{Service: "nixbox"}).Authenticate("root", "pw"); err == nil {
		t.Error("cgo-less PAM stub must reject every login")
	}
}
