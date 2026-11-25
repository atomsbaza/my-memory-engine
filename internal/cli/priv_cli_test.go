package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

func TestCLIPrivateRecordEncryptAndShowDecrypt(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEM_AGE_RECIPIENT", id.Recipient().String())
	t.Setenv("MEM_AGE_KEY", id.String())

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "priv.md")
	secretNote := "private strategy details\n**NOT.** never commit plaintext"
	if err := os.WriteFile(f, []byte(secretNote), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errBuf bytes.Buffer
	code := Main([]string{"--home", home, "add", "--layer", "lesson", "--scope", "global", "--file", f, "--sensitivity", "private", "--no-commit"}, &out, &errBuf)
	if code != ExitOK {
		t.Fatalf("add private failed (%d): %s\n%s", code, out.String(), errBuf.String())
	}
	recID := strings.Fields(out.String())[0]

	// On disk, the body must be encrypted (not plaintext).
	matches, err := filepath.Glob(filepath.Join(home, "lessons", "*.md"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected 1 lesson file, got %v, %v", matches, err)
	}
	rawDisk, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawDisk), "never commit plaintext") {
		t.Fatalf("plaintext body leaked on disk:\n%s", string(rawDisk))
	}
	if !strings.Contains(string(rawDisk), "BEGIN AGE ENCRYPTED FILE") {
		t.Fatalf("expected age armor on disk:\n%s", string(rawDisk))
	}

	// `mem show` decrypts when MEM_AGE_KEY is set.
	out.Reset()
	errBuf.Reset()
	code = Main([]string{"--home", home, "show", recID}, &out, &errBuf)
	if code != ExitOK || !strings.Contains(out.String(), "private strategy details") {
		t.Fatalf("show failed to decrypt (%d): %s\n%s", code, out.String(), errBuf.String())
	}
}
