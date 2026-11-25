package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIMaintainDryRunAndApplyJSON(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out, errBuf bytes.Buffer
	code := Main([]string{"--home", home, "init"}, &out, &errBuf)
	if code != ExitOK {
		t.Fatalf("init failed (%d): %s", code, errBuf.String())
	}

	out.Reset()
	errBuf.Reset()
	code = Main([]string{"--home", home, "maintain", "--dry-run", "--json"}, &out, &errBuf)
	if code != ExitOK {
		t.Fatalf("maintain --dry-run --json failed (%d): %s\n%s", code, out.String(), errBuf.String())
	}
	var rep maintainReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out.String())
	}
	if !rep.DryRun {
		t.Fatalf("expected DryRun=true")
	}

	out.Reset()
	errBuf.Reset()
	code = Main([]string{"--home", home, "maintain"}, &out, &errBuf)
	if code != ExitOK || !strings.Contains(out.String(), "maintain (apply)") {
		t.Fatalf("maintain apply failed (%d): %s\n%s", code, out.String(), errBuf.String())
	}
}
