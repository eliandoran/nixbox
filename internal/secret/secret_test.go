package secret

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age/agessh"
	"golang.org/x/crypto/ssh"
)

// TestRoundTrip exercises the exact production path: an OpenSSH ed25519
// public key file as the recipient, decryption with the matching private
// key — what agenix does on the host at activation.
func TestRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "ssh_host_ed25519_key.pub")
	if err := os.WriteFile(keyFile, ssh.MarshalAuthorizedKey(sshPub), 0o644); err != nil {
		t.Fatal(err)
	}

	rec, err := LoadRecipient(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("s3cret value\nwith a second line")
	ciphertext, err := Encrypt(rec, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if string(ciphertext) == string(plaintext) {
		t.Fatal("ciphertext equals plaintext")
	}

	id, err := agessh.NewEd25519Identity(priv)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decrypt(id, ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("round trip: got %q, want %q", got, plaintext)
	}
}

func TestLoadRecipientMissing(t *testing.T) {
	if _, err := LoadRecipient(filepath.Join(t.TempDir(), "nope.pub")); err == nil {
		t.Error("expected error for missing key file")
	}
}
