// `mem sync` at the CLI boundary: exit-code mapping (0 / 1 / 3), the local-only
// default, and the guarantee that a default run never touches the network
// (AC-P2-14, NFR-NET-01). A recording git stub keeps the assertions exact.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGit writes a recording git stub into a temp dir. The stub answers the
// reads `mem sync` performs and fails the verbs listed in behaviour.
func fakeGit(t *testing.T, behaviour map[string]string) (bin, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_GIT_LOG"
case "$1" in
  rev-parse)
    case "$*" in
      *--is-inside-work-tree*) echo true; exit 0 ;;
      *"@{u}"*) if [ -z "$FAKE_GIT_UPSTREAM" ]; then echo "fatal: no upstream" >&2; exit 128; fi; echo "$FAKE_GIT_UPSTREAM"; exit 0 ;;
    esac
    echo "-"; exit 0 ;;
  status) printf '%s' "$FAKE_GIT_STATUS"; exit 0 ;;
  add|commit) exit 0 ;;
  remote) if [ -n "$FAKE_GIT_REMOTES" ]; then printf '%s\n' "$FAKE_GIT_REMOTES"; fi; exit 0 ;;
  diff) if [ -n "$FAKE_GIT_CONFLICT" ]; then printf '%s\n' "$FAKE_GIT_CONFLICT"; fi; exit 0 ;;
  ls-files) exit 0 ;;
  fetch) if [ -n "$FAKE_GIT_FETCH_FAIL" ]; then echo "$FAKE_GIT_FETCH_FAIL" >&2; exit 1; fi; exit 0 ;;
  pull) if [ -n "$FAKE_GIT_PULL_FAIL" ]; then echo "$FAKE_GIT_PULL_FAIL" >&2; exit 1; fi; exit 0 ;;
  push) exit 0 ;;
esac
exit 0
`
	bin = filepath.Join(dir, "git")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("FAKE_GIT_LOG", logPath)
	for k, v := range behaviour {
		t.Setenv(k, v)
	}
	return bin, logPath
}

// syncArgs builds a `mem sync` argv that runs the stub instead of real git:
// PATH is prefixed with the stub's dir so internal/sync's default lookup finds it.
func withFakeGit(t *testing.T, bin string, home string, argv ...string) (int, string, string) {
	t.Helper()
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+oldPath)
	return runHome(t, home, "", argv...)
}

func TestSyncNoUpstreamStaysOffline(t *testing.T) {
	home := fixtureHome(t)
	bin, logPath := fakeGit(t, map[string]string{"FAKE_GIT_STATUS": " M lessons/a.md"})
	code, out, stderr := withFakeGit(t, bin, home, "sync")
	mustCode(t, ExitOK, code, out, stderr, "sync without upstream")
	if !strings.Contains(out, "ยังไม่มี remote/upstream") {
		t.Fatalf("sync output = %q, want the no-upstream note", out)
	}
	calls := readLog(t, logPath)
	for _, banned := range []string{"fetch", "pull", "push"} {
		if logHasVerb(calls, banned) {
			t.Fatalf("%q ran with no upstream — default paths must stay offline:\n%v", banned, calls)
		}
	}
}

func TestSyncConflictExitsThree(t *testing.T) {
	home := fixtureHome(t)
	bin, logPath := fakeGit(t, map[string]string{
		"FAKE_GIT_UPSTREAM":  "origin/main",
		"FAKE_GIT_REMOTES":   "origin",
		"FAKE_GIT_PULL_FAIL": "CONFLICT (content): Merge conflict in lessons/a.md",
		"FAKE_GIT_CONFLICT":  "lessons/a.md",
	})
	code, out, stderr := withFakeGit(t, bin, home, "sync")
	if code != ExitConflict {
		t.Fatalf("sync conflict: exit = %d, want %d\nstdout: %s\nstderr: %s", code, ExitConflict, out, stderr)
	}
	if !strings.Contains(stderr, "lessons/a.md") {
		t.Fatalf("stderr does not name the conflicted file: %q", stderr)
	}
	if logHasVerb(readLog(t, logPath), "push") {
		t.Fatalf("push ran after a conflict")
	}
}

func TestSyncOtherGitFailureExitsOne(t *testing.T) {
	home := fixtureHome(t)
	bin, _ := fakeGit(t, map[string]string{
		"FAKE_GIT_UPSTREAM":   "origin/main",
		"FAKE_GIT_REMOTES":    "origin",
		"FAKE_GIT_FETCH_FAIL": "fatal: could not read from remote repository",
	})
	code, out, stderr := withFakeGit(t, bin, home, "sync")
	if code != ExitError {
		t.Fatalf("sync fetch failure: exit = %d, want %d\nstdout: %s\nstderr: %s", code, ExitError, out, stderr)
	}
	if !strings.Contains(stderr, "could not read from remote") {
		t.Fatalf("stderr does not surface the git failure: %q", stderr)
	}
}

func TestSyncHappyPathReportsPushed(t *testing.T) {
	home := fixtureHome(t)
	bin, logPath := fakeGit(t, map[string]string{
		"FAKE_GIT_UPSTREAM": "origin/main",
		"FAKE_GIT_REMOTES":  "origin",
		"FAKE_GIT_STATUS":   " M lessons/a.md",
	})
	code, out, stderr := withFakeGit(t, bin, home, "sync", "-m", "test: sync")
	mustCode(t, ExitOK, code, out, stderr, "sync happy path")
	if !strings.Contains(out, "committed") || !strings.Contains(out, "pushed") {
		t.Fatalf("sync output = %q, want committed + pushed", out)
	}
	calls := readLog(t, logPath)
	for _, want := range []string{"fetch", "pull", "push"} {
		if !logHasVerb(calls, want) {
			t.Fatalf("%q did not run:\n%v", want, calls)
		}
	}
	if logHasVerb(calls, "--force") || strings.Contains(strings.Join(calls, " "), "--force") {
		t.Fatalf("sync used a force flag:\n%v", calls)
	}
}

// TestHookStopDoesNotPush: the stop hook commits and stops (TB-7).
func TestHookStopDoesNotPush(t *testing.T) {
	home := fixtureHome(t)
	bin, logPath := fakeGit(t, map[string]string{"FAKE_GIT_STATUS": " M lessons/a.md"})
	code, out, stderr := withFakeGit(t, bin, home, "hook", "--event", "stop", "--agent", "test")
	mustCode(t, ExitOK, code, out, stderr, "hook stop")
	calls := readLog(t, logPath)
	for _, banned := range []string{"fetch", "pull", "push"} {
		if logHasVerb(calls, banned) {
			t.Fatalf("stop hook ran %q:\n%v", banned, calls)
		}
	}
}

func readLog(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read call log: %v", err)
	}
	var calls []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			calls = append(calls, line)
		}
	}
	return calls
}

func logHasVerb(calls []string, verb string) bool {
	for _, c := range calls {
		if c == verb || strings.HasPrefix(c, verb+" ") {
			return true
		}
	}
	return false
}
