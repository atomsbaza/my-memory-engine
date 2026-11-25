package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIBiTemporalAndContradictsLinks(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	f1 := filepath.Join(t.TempDir(), "a.md")
	f2 := filepath.Join(t.TempDir(), "b.md")
	if err := os.WriteFile(f1, []byte("prefer python for quick scripts\n**NOT.** never use bash for json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, []byte("prefer go for production cli tools\n**NOT.** never use python for cli"), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (int, string) {
		var out, errBuf bytes.Buffer
		code := Main(append([]string{"--home", home}, args...), &out, &errBuf)
		return code, out.String() + errBuf.String()
	}

	code, out := run("add", "--layer", "lesson", "--scope", "global", "--file", f1, "--valid-from", "2026-01-01", "--no-commit")
	if code != ExitOK {
		t.Fatalf("add 1 failed (%d): %s", code, out)
	}
	id1 := strings.Fields(out)[0]

	code, out2 := run("add", "--layer", "lesson", "--scope", "global", "--file", f2, "--valid-from", "2026-11-01", "--contradicts", id1, "--no-commit")
	if code != ExitOK {
		t.Fatalf("add 2 failed (%d): %s", code, out2)
	}
	id2 := strings.Fields(out2)[0]

	code, lout := run("links", id2, "--json")
	if code != ExitOK || !strings.Contains(lout, `"contradicts"`) || !strings.Contains(lout, id1) {
		t.Fatalf("links %s failed (%d): %s", id2, code, lout)
	}

	code, lout1 := run("links", id1, "--json")
	if code != ExitOK || !strings.Contains(lout1, `"contradicted-by"`) || !strings.Contains(lout1, id2) {
		t.Fatalf("links %s failed (%d): %s", id1, code, lout1)
	}
}
