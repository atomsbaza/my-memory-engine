// Package priv provides optional pure-Go AGE encryption/decryption for
// records with sensitivity=private using MEM_AGE_RECIPIENT and MEM_AGE_KEY /
// MEM_AGE_KEY_FILE.
package priv

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"
	"filippo.io/age/armor"
)

const armorHeader = "-----BEGIN AGE ENCRYPTED FILE-----"

// IsEncrypted reports whether body is an ASCII-armored age ciphertext.
func IsEncrypted(body string) bool {
	return strings.HasPrefix(strings.TrimSpace(body), armorHeader)
}

// RecipientsFromEnv parses comma-separated age recipients from MEM_AGE_RECIPIENT.
func RecipientsFromEnv() []string {
	raw := strings.TrimSpace(os.Getenv("MEM_AGE_RECIPIENT"))
	if raw == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// IdentitiesFromEnv loads age identities from MEM_AGE_KEY or MEM_AGE_KEY_FILE.
func IdentitiesFromEnv() ([]age.Identity, error) {
	if raw := strings.TrimSpace(os.Getenv("MEM_AGE_KEY")); raw != "" {
		return age.ParseIdentities(strings.NewReader(raw))
	}
	if path := strings.TrimSpace(os.Getenv("MEM_AGE_KEY_FILE")); path != "" {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("priv: open MEM_AGE_KEY_FILE: %w", err)
		}
		defer f.Close()
		return age.ParseIdentities(f)
	}
	return nil, nil
}

// Encrypt encrypts plaintext to ASCII-armored age format for the given X25519 recipients.
func Encrypt(plaintext string, recipientStrings []string) (string, error) {
	if len(recipientStrings) == 0 {
		return "", fmt.Errorf("priv: no recipients provided")
	}
	recipients := make([]age.Recipient, 0, len(recipientStrings))
	for _, rs := range recipientStrings {
		r, err := age.ParseX25519Recipient(rs)
		if err != nil {
			return "", fmt.Errorf("priv: invalid recipient %q: %w", rs, err)
		}
		recipients = append(recipients, r)
	}
	var out bytes.Buffer
	aw := armor.NewWriter(&out)
	w, err := age.Encrypt(aw, recipients...)
	if err != nil {
		return "", fmt.Errorf("priv: encrypt init: %w", err)
	}
	if _, err := io.WriteString(w, plaintext); err != nil {
		return "", fmt.Errorf("priv: encrypt write: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("priv: encrypt close: %w", err)
	}
	if err := aw.Close(); err != nil {
		return "", fmt.Errorf("priv: armor close: %w", err)
	}
	return out.String(), nil
}

// Decrypt decrypts an ASCII-armored age ciphertext using the given identities.
func Decrypt(ciphertext string, identities []age.Identity) (string, error) {
	if len(identities) == 0 {
		return "", fmt.Errorf("priv: no identities available")
	}
	ar := armor.NewReader(strings.NewReader(ciphertext))
	r, err := age.Decrypt(ar, identities...)
	if err != nil {
		return "", fmt.Errorf("priv: decrypt: %w", err)
	}
	plain, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("priv: read decrypted: %w", err)
	}
	return string(plain), nil
}
