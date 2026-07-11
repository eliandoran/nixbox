// Package secret encrypts workload secrets with age for the agenix
// NixOS module to decrypt at activation time.
//
// The recipient is the host's SSH ed25519 host key — the same identity
// agenix's default age.identityPaths uses for decryption — so no key
// material is ever generated or stored by nixbox: the public half
// encrypts here, the private half (root-only, already on every host
// running sshd) decrypts during nixos-rebuild activation. Plaintext
// exists only in the request that carries it; nixbox persists ciphertext
// alone (state/secrets/<name>.age), which is what makes committing it to
// the world-readable Nix store safe.
package secret

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"filippo.io/age"
	"filippo.io/age/agessh"
)

// DefaultRecipientFile is the SSH host public key used as the age
// recipient when none is configured. agenix decrypts with the matching
// private key by default (age.identityPaths).
const DefaultRecipientFile = "/etc/ssh/ssh_host_ed25519_key.pub"

// LoadRecipient parses an OpenSSH public key file (authorized_keys
// format, as written by ssh-keygen) into an age recipient.
func LoadRecipient(path string) (age.Recipient, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading age recipient key: %w (nixbox encrypts secrets to the host's SSH key; is sshd configured?)", err)
	}
	rec, err := agessh.ParseRecipient(string(bytes.TrimSpace(b)))
	if err != nil {
		return nil, fmt.Errorf("parsing %s as an age recipient: %w", path, err)
	}
	return rec, nil
}

// Encrypt seals plaintext to the recipient in the binary age format
// agenix expects in its .file (a .age file in the state flake).
func Encrypt(rec age.Recipient, plaintext []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := encryptTo(&buf, rec, plaintext); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// encryptTo is Encrypt against a caller-supplied writer, split out so
// the write-path errors are testable (a bytes.Buffer never fails).
func encryptTo(dst io.Writer, rec age.Recipient, plaintext []byte) error {
	w, err := age.Encrypt(dst, rec)
	if err != nil {
		return err
	}
	if _, err := w.Write(plaintext); err != nil {
		return err
	}
	return w.Close()
}

// Decrypt opens ciphertext with an identity. nixbox itself only needs
// this in tests today; a future re-key/edit flow would use the host's
// private key the same way agenix does.
func Decrypt(id age.Identity, ciphertext []byte) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(ciphertext), id)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}
