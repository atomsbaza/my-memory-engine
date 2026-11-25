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
