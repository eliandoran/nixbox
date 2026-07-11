package secret

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
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

func TestLoadRecipientGarbage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "junk.pub")
	if err := os.WriteFile(p, []byte("not an ssh key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRecipient(p); err == nil {
		t.Error("expected error for unparseable key file")
	}
}

// failRecipient errors at Wrap, standing in for an unusable key.
type failRecipient struct{}

func (failRecipient) Wrap(fileKey []byte) ([]*age.Stanza, error) {
	return nil, errors.New("wrap failed")
}

func TestEncryptBadRecipient(t *testing.T) {
	if _, err := Encrypt(failRecipient{}, []byte("x")); err == nil {
		t.Error("expected error from failing recipient")
	}
}

// failAfterWriter fails once n bytes have been accepted, to fault each
// stage of the age stream: the header (written at age.Encrypt), a chunk
// flush mid-Write (chunks are 64 KiB), and the final flush at Close.
type failAfterWriter struct{ n int }

func (w *failAfterWriter) Write(p []byte) (int, error) {
	if len(p) > w.n {
		n := w.n
		w.n = 0
		return n, errors.New("write failed")
	}
	w.n -= len(p)
	return len(p), nil
}

func TestEncryptToWriteErrors(t *testing.T) {
	rec, err := agessh.ParseRecipient(testPubKey(t))
	if err != nil {
		t.Fatal(err)
	}
	// Header write fails immediately.
	if err := encryptTo(&failAfterWriter{n: 0}, rec, []byte("x")); err == nil {
		t.Error("expected header write error")
	}
	// Header fits but the payload doesn't: a small payload's single
	// chunk is flushed at Close, so cutting one byte off the full size
	// faults the final flush.
	ct, err := Encrypt(rec, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := encryptTo(&failAfterWriter{n: len(ct) - 1}, rec, []byte("x")); err == nil {
		t.Error("expected close (final chunk) error")
	}
	// A payload past the 64 KiB chunk size flushes during Write itself.
	big := bytes.Repeat([]byte("a"), 130*1024)
	if err := encryptTo(&failAfterWriter{n: len(ct)}, rec, big); err == nil {
		t.Error("expected mid-write chunk error")
	}
}

func TestDecryptErrors(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := agessh.ParseRecipient(string(ssh.MarshalAuthorizedKey(sshPub)))
	if err != nil {
		t.Fatal(err)
	}
	id, err := agessh.NewEd25519Identity(priv)
	if err != nil {
		t.Fatal(err)
	}

	// Garbage never parses as an age header.
	if _, err := Decrypt(id, []byte("not age data")); err == nil {
		t.Error("expected header parse error")
	}

	// A corrupted payload passes the header but fails while reading.
	ct, err := Encrypt(rec, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	ct[len(ct)-1] ^= 0xff
	if _, err := Decrypt(id, ct); err == nil {
		t.Error("expected payload read error")
	}
}

// testPubKey returns a fresh OpenSSH ed25519 public key line.
func testPubKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(ssh.MarshalAuthorizedKey(sshPub))
}
