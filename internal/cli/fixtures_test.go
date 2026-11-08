// Shared helpers for the CLI tests: a synthetic home copied from
// testdata/domain/home (never the real ~/my-memory), and a runner that drives
// MainWithIO against pipes so every assertion is on real process output.
package cli

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureSrc is the synthetic home every CLI test copies (records + taxonomy).
const fixtureSrc = "../../testdata/domain/home"

// fixtureHome copies the synthetic home into a temp dir and makes it a git
// repo with one baseline commit, so the write path exercises its real commit
// behaviour and commit counts start from a known point.
func fixtureHome(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	copyTree(t, fixtureSrc, dst)
	gitInit(t, dst)
	git(t, dst, "add", "-A")
	git(t, dst, "commit", "-q", "-m", "fixture baseline")
	return dst
}

// bareHome copies the synthetic home without a git repo.
func bareHome(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	copyTree(t, fixtureSrc, dst)
	return dst
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copyTree %s -> %s: %v", src, dst, err)
	}
}

// gitInit makes dir a git repo with a commit identity, locally only.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "test@example.invalid")
	git(t, dir, "config", "user.name", "test")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Keep the developer's git config (hooks, signing) out of the test paths.
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// runMain drives the router with a scripted stdin and returns (code, stdout,
// stderr).
func runMain(t *testing.T, stdin string, argv ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	var in io.Reader = strings.NewReader(stdin)
	code := MainWithIO(argv, in, &out, &errb)
	return code, out.String(), errb.String()
}

// runHome is runMain with --home prepended.
func runHome(t *testing.T, home, stdin string, argv ...string) (int, string, string) {
	t.Helper()
	return runMain(t, stdin, append([]string{"--home", home}, argv...)...)
}

// mustCode asserts the exit code and returns stdout/stderr.
func mustCode(t *testing.T, want, got int, stdout, stderr string, what string) (string, string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: exit = %d, want %d\nstdout: %s\nstderr: %s", what, got, want, stdout, stderr)
	}
	return stdout, stderr
}

// writeFile writes a file under home, creating parents.
func writeFile(t *testing.T, home, rel, content string) string {
	t.Helper()
	path := filepath.Join(home, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return path
}
