package priv

import (
	"testing"

	"filippo.io/age"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	plain := "secret token or private reflection\n**NOT.** never leak"
	ct, err := Encrypt(plain, []string{id.Recipient().String()})
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if !IsEncrypted(ct) {
		t.Fatalf("expected armored age ciphertext, got:\n%s", ct)
	}
	got, err := Decrypt(ct, []age.Identity{id})
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if got != plain {
		t.Fatalf("Decrypt = %q, want %q", got, plain)
	}
}
