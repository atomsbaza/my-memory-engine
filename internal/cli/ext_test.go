package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIExtRoundTrip(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	vault := t.TempDir()
	if err := os.WriteFile(filepath.Join(vault, "note.md"), []byte("# Auth\nUse OAuth PKCE flow"), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (int, string) {
		var out, errBuf bytes.Buffer
		full := append([]string{"--home", home}, args...)
		code := Main(full, &out, &errBuf)
		return code, out.String() + errBuf.String()
	}

	if code, out := run("ext", "add", "docs", vault); code != ExitOK {
		t.Fatalf("ext add failed (%d): %s", code, out)
	}
	if code, out := run("ext", "list"); code != ExitOK || !strings.Contains(out, "docs") {
		t.Fatalf("ext list failed (%d): %s", code, out)
	}
	if code, out := run("ext", "reindex"); code != ExitOK || !strings.Contains(out, "1 files") {
		t.Fatalf("ext reindex failed (%d): %s", code, out)
	}
	if code, out := run("ext", "search", "PKCE"); code != ExitOK || !strings.Contains(out, "note.md") {
		t.Fatalf("ext search failed (%d): %s", code, out)
	}
	if code, out := run("ext", "rm", "docs"); code != ExitOK {
		t.Fatalf("ext rm failed (%d): %s", code, out)
	}
}

func TestCLIVerifyCheckExternalBrokenLinks(t *testing.T) {
	home := t.TempDir()
	for _, dir := range []string{".memory", "lessons", "knowledge", "projects", "rules"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	vault := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vault, "Auth"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "Auth", "OAuth.md"), []byte("# PKCE"), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (int, string) {
		var out, errBuf bytes.Buffer
		full := append([]string{"--home", home}, args...)
		code := Main(full, &out, &errBuf)
		return code, out.String() + errBuf.String()
	}

	if code, out := run("ext", "add", "obsidian", vault); code != ExitOK {
		t.Fatalf("ext add failed (%d): %s", code, out)
	}
	rec := "---\nid: knowledge.0123abcd\nlayer: knowledge\nscope: global\nstatus: active\nsensitivity: internal\ncreated: 2026-11-27\nsource: test\nsummary: Pointers check\n---\n\n# Pointers\n\nvalid: `vault://obsidian/Auth/OAuth.md#PKCE` and [[Auth/OAuth]]\n```bash\nif [[ -f \"$x\" ]]; then echo ok; fi\n```\nbroken: vault://obsidian/Missing and [[GhostNote]]\n"
	if err := os.WriteFile(filepath.Join(home, "knowledge", "p1.md"), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out := run("reindex"); code != ExitOK {
		t.Fatalf("reindex failed (%d): %s", code, out)
	}

	if code, out := run("verify"); code != ExitOK || !strings.Contains(out, "0 warnings") {
		t.Fatalf("verify without --check-external unexpected (%d): %s", code, out)
	}
	code, out := run("verify", "--check-external")
	if code != ExitOK {
		t.Fatalf("verify --check-external failed (%d): %s", code, out)
	}
	if !strings.Contains(out, "warn:") || !strings.Contains(out, "Missing") || !strings.Contains(out, "GhostNote") || !strings.Contains(out, "2 warnings") {
		t.Fatalf("verify --check-external missing expected warnings: %s", out)
	}
}
