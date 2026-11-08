// Binary-level integration tests: they build cmd/mem and drive the real
// process, so the acceptance commands in AC-P2-07/08 (`mem init`, `mem verify`,
// `mem doctor`, `mem stats --json`, exit codes) are proved on the shipped
// binary rather than on in-process calls.
package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var (
	memBin  string
	scratch string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mem-cli-e2e-")
	if err != nil {
		panic(err)
	}
	scratch = dir
	memBin = filepath.Join(dir, "mem")
	build := exec.Command("go", "build", "-o", memBin, "../../cmd/mem")
	if out, berr := build.CombinedOutput(); berr != nil {
		os.Stderr.WriteString("go build cmd/mem: " + berr.Error() + "\n" + string(out))
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// env is the isolated environment every invocation gets: HOME points into the
// scratch dir, so the binary can never read or write the developer's real
// memory repo, and git never prompts.
func env() []string {
	return append(os.Environ(),
		"HOME="+filepath.Join(scratch, "home"),
		"XDG_CONFIG_HOME="+filepath.Join(scratch, "home", ".config"),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
}

// runBin executes the binary and returns exit code, stdout and stderr.
func runBin(t *testing.T, stdin string, argv ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(memBin, argv...)
	cmd.Env = env()
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run %v: %v", argv, err)
	}
	return code, out.String(), errb.String()
}

func TestBinaryInitVerifyDoctor(t *testing.T) {
	home := filepath.Join(t.TempDir(), "memhome")

	code, out, stderr := runBin(t, "", "init", home)
	if code != 0 {
		t.Fatalf("mem init: exit = %d\nstdout: %s\nstderr: %s", code, out, stderr)
	}

	code, out, stderr = runBin(t, "", "--home", home, "verify")
	if code != 0 {
		t.Fatalf("mem verify: exit = %d\nstdout: %s\nstderr: %s", code, out, stderr)
	}
	if !strings.Contains(out, "0 records · 0 problems") {
		t.Fatalf("mem verify output = %q, want 0 problems", out)
	}

	code, out, stderr = runBin(t, "", "--home", home, "doctor")
	if code != 0 {
		t.Fatalf("mem doctor: exit = %d\nstdout: %s\nstderr: %s", code, out, stderr)
	}
	if !strings.Contains(out, "0 problems") {
		t.Fatalf("mem doctor output = %q", out)
	}
}

// TestBinaryManifest: the help table is the 18 legacy families + init + doctor.
func TestBinaryManifest(t *testing.T) {
	code, out, stderr := runBin(t, "", "--help")
	if code != 0 {
		t.Fatalf("mem --help: exit = %d (stderr %s)", code, stderr)
	}
	for _, name := range []string{
		"context", "search", "list", "show", "add", "touch", "cite", "review",
		"supersede", "verify", "reindex", "render", "sync", "import", "hook",
		"stats", "eval", "mcp", "init", "doctor",
	} {
		if !strings.Contains(out, "\n  "+name) {
			t.Fatalf("help table is missing %s:\n%s", name, out)
		}
	}
}

// TestBinaryWritePathExitCodes: 0 / 1 / 2 / 3 through the real process.
func TestBinaryWritePathExitCodes(t *testing.T) {
	home := filepath.Join(t.TempDir(), "mh")
	if code, _, stderr := runBin(t, "", "init", home); code != 0 {
		t.Fatalf("init: exit %d (%s)", code, stderr)
	}

	body := "**Rule.** keep the derived index out of the canonical tree\n\n**NOT.** committing the index file\n\n**Evidence.** binary test\n"
	code, out, stderr := runBin(t, body, "--home", home, "add", "--layer", "lesson", "--scope", "global", "--file", "-")
	if code != 0 {
		t.Fatalf("add: exit = %d\nstdout: %s\nstderr: %s", code, out, stderr)
	}
	if !strings.HasPrefix(out, "lesson.") {
		t.Fatalf("add output = %q, want the record id", out)
	}

	// 3 — same fact again (duplicate id in the same layer/scope).
	if code, _, stderr := runBin(t, body, "--home", home, "add", "--layer", "lesson", "--scope", "global", "--file", "-"); code != 3 {
		t.Fatalf("duplicate add: exit = %d, want 3 (%s)", code, stderr)
	}

	// 2 — a secret in the body.
	if code, _, _ := runBin(t, "**Rule.** token: abcdefghijklmnopqrstuvwxyz123456\n",
		"--home", home, "add", "--layer", "lesson", "--scope", "global", "--file", "-"); code != 2 {
		t.Fatalf("secret add: exit = %d, want 2", code)
	}

	// 1 — unknown record.
	if code, _, _ := runBin(t, "", "--home", home, "show", "lesson.deadbeef"); code != 1 {
		t.Fatalf("show missing: exit = %d, want 1", code)
	}

	// 2 — unknown command (argparse parity).
	if code, _, _ := runBin(t, "", "frobnicate"); code != 2 {
		t.Fatalf("unknown command: exit = %d, want 2", code)
	}
}

// TestBinaryStatsJSONPurity: `mem stats --json` is one JSON document, so
// `| jq -e .` succeeds.
func TestBinaryStatsJSONPurity(t *testing.T) {
	home := filepath.Join(t.TempDir(), "mh")
	if code, _, stderr := runBin(t, "", "init", home); code != 0 {
		t.Fatalf("init: exit %d (%s)", code, stderr)
	}
	code, out, stderr := runBin(t, "", "--home", home, "stats", "--json")
	if code != 0 {
		t.Fatalf("stats --json: exit = %d (%s)", code, stderr)
	}
	var payload map[string]any
	dec := json.NewDecoder(strings.NewReader(out))
	if err := dec.Decode(&payload); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	if dec.More() {
		t.Fatalf("stdout carries more than one document:\n%s", out)
	}
	for _, key := range []string{"records", "active", "by_layer", "harness", "never_wrote"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("stats payload missing %q: %s", key, out)
		}
	}
}

// TestBinarySyncLocalOnly: a fresh home has no remote, and sync must not
// create one or reach the network.
func TestBinarySyncLocalOnly(t *testing.T) {
	home := filepath.Join(t.TempDir(), "mh")
	if code, _, stderr := runBin(t, "", "init", home); code != 0 {
		t.Fatalf("init: exit %d (%s)", code, stderr)
	}
	code, out, stderr := runBin(t, "", "--home", home, "sync")
	if code != 0 {
		t.Fatalf("sync: exit = %d\nstdout: %s\nstderr: %s", code, out, stderr)
	}
	if !strings.Contains(out, "ยังไม่มี remote/upstream") {
		t.Fatalf("sync output = %q, want the no-upstream note", out)
	}
	remotes := gitOut(t, home, "remote", "-v")
	if strings.TrimSpace(remotes) != "" {
		t.Fatalf("sync configured a remote: %s", remotes)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
