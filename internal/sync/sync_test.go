// Sync mechanics tests: the local commit path, the "no upstream means no
// network" invariant, typed/observable git failures and the conflict contract
// (REQ-SYNC-01, REQ-SYNC-02, NFR-NET-01). Real git for the local parts, a
// recording stub for the network/call-ordering assertions.
package sync

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repo makes a throwaway git repo.
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "test"},
	} {
		runGit(t, dir, args...)
	}
	return dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func TestIsRepo(t *testing.T) {
	dir := repo(t)
	ok, err := IsRepo(dir, Options{})
	if err != nil || !ok {
		t.Fatalf("IsRepo(repo) = %v, %v; want true, nil", ok, err)
	}
	plain := t.TempDir()
	ok, err = IsRepo(plain, Options{})
	if err != nil || ok {
		t.Fatalf("IsRepo(plain dir) = %v, %v; want false, nil", ok, err)
	}
}

func TestCommitAllCountsAndSkips(t *testing.T) {
	dir := repo(t)
	write(t, dir, "lessons/a.md", "one\n")
	write(t, dir, "rules/b.md", "two\n")

	n, err := CommitAll(dir, "", Options{})
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if n != 2 {
		t.Fatalf("CommitAll staged %d paths, want 2", n)
	}
	if log := runGit(t, dir, "log", "--oneline", "-1"); !strings.Contains(log, "mem: sync (2 files)") {
		t.Fatalf("default commit message missing: %s", log)
	}

	n, err = CommitAll(dir, "custom", Options{})
	if err != nil || n != 0 {
		t.Fatalf("second CommitAll = (%d, %v), want (0, nil)", n, err)
	}
}

func TestRunWithoutUpstreamSkipsNetwork(t *testing.T) {
	dir := repo(t)
	bin, log := fakeGit(t, map[string]string{"FAKE_GIT_STATUS": " M lessons/a.md"})
	write(t, dir, "lessons/a.md", "one\n")

	res, err := Run(dir, Options{Bin: bin, Env: envWith(log)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Committed || res.Pushed {
		t.Fatalf("result = %+v, want committed and not pushed", res)
	}
	if res.HasUpstream {
		t.Fatalf("HasUpstream = true without an upstream")
	}
	if len(res.Notes) == 0 || !strings.Contains(strings.Join(res.Notes, " "), "ยังไม่มี remote/upstream") {
		t.Fatalf("notes = %v, want the no-upstream note", res.Notes)
	}
	for _, banned := range []string{"fetch", "pull", "push"} {
		if calls := readCalls(t, log); contains(calls, banned) {
			t.Fatalf("%q was invoked with no upstream — default paths must stay offline:\n%v", banned, calls)
		}
	}
}

func TestRunNoPushStopsAfterCommit(t *testing.T) {
	dir := repo(t)
	bin, log := fakeGit(t, map[string]string{
		"FAKE_GIT_STATUS":     " M lessons/a.md",
		"FAKE_GIT_UPSTREAM":   "origin/main",
		"FAKE_GIT_REMOTES":    "origin",
		"FAKE_GIT_PULL_FAIL":  "",
		"FAKE_GIT_CONFLICT":   "",
		"FAKE_GIT_FETCH_FAIL": "",
	})
	write(t, dir, "lessons/a.md", "one\n")

	res, err := Run(dir, Options{Bin: bin, NoPush: true, Env: envWith(log)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Pushed {
		t.Fatalf("--no-push pushed anyway: %+v", res)
	}
	calls := readCalls(t, log)
	for _, banned := range []string{"fetch", "pull", "push"} {
		if contains(calls, banned) {
			t.Fatalf("%q invoked despite no-push:\n%v", banned, calls)
		}
	}
}

// TestRunConflictStopsBeforePush: pull fails with an unmerged path → typed
// ConflictError naming the file, and push is never called (REQ-SYNC-02).
func TestRunConflictStopsBeforePush(t *testing.T) {
	dir := repo(t)
	bin, log := fakeGit(t, map[string]string{
		"FAKE_GIT_UPSTREAM":  "origin/main",
		"FAKE_GIT_REMOTES":   "origin",
		"FAKE_GIT_PULL_FAIL": "CONFLICT (content): Merge conflict in lessons/a.md",
		"FAKE_GIT_CONFLICT":  "lessons/a.md",
	})
	_, err := Run(dir, Options{Bin: bin, Env: envWith(log)})
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *ConflictError", err)
	}
	if len(ce.Files) != 1 || ce.Files[0] != "lessons/a.md" {
		t.Fatalf("conflict files = %v, want [lessons/a.md]", ce.Files)
	}
	if !strings.Contains(ce.Error(), "lessons/a.md") {
		t.Fatalf("conflict message does not name the file: %q", ce.Error())
	}
	if calls := readCalls(t, log); contains(calls, "push") {
		t.Fatalf("push ran after a conflict:\n%v", calls)
	}
}

// TestRunGitFailureIsObservable: a plain git failure (no conflict) retries and
// surfaces the stderr instead of a bare exit status.
func TestRunGitFailureIsObservable(t *testing.T) {
	dir := repo(t)
	bin, log := fakeGit(t, map[string]string{
		"FAKE_GIT_UPSTREAM":   "origin/main",
		"FAKE_GIT_REMOTES":    "origin",
		"FAKE_GIT_FETCH_FAIL": "fatal: could not read from remote repository",
	})
	_, err := Run(dir, Options{Bin: bin, Retries: 2, Env: envWith(log)})
	if err == nil {
		t.Fatal("Run succeeded with a failing fetch")
	}
	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("err = %v, want *GitError", err)
	}
	if !strings.Contains(ge.Stderr, "could not read from remote") {
		t.Fatalf("GitError.Stderr = %q, want the git stderr", ge.Stderr)
	}
	if !strings.Contains(err.Error(), "3 ครั้ง") && !strings.Contains(err.Error(), "2 ครั้ง") {
		t.Fatalf("error does not report the retry exhaustion: %q", err)
	}
	if got := countCalls(readCalls(t, log), "fetch"); got != 2 {
		t.Fatalf("fetch attempts = %d, want 2 (Retries)", got)
	}
}

func TestRunRejectsForceArguments(t *testing.T) {
	dir := repo(t)
	bin, log := fakeGit(t, nil)
	if _, err := (Options{Bin: bin, Env: envWith(log)}).git(dir, "push", "--force"); err == nil {
		t.Fatal("a --force argument was accepted")
	}
	if calls := readCalls(t, log); len(calls) != 0 {
		t.Fatalf("the force call reached the git binary:\n%v", calls)
	}
}

func TestRemotesAndUpstream(t *testing.T) {
	dir := repo(t)
	remotes, err := Remotes(dir, Options{})
	if err != nil || len(remotes) != 0 {
		t.Fatalf("Remotes = %v, %v; want empty", remotes, err)
	}
	runGit(t, dir, "remote", "add", "origin", "https://example.invalid/r.git")
	remotes, err = Remotes(dir, Options{})
	if err != nil || len(remotes) != 1 || remotes[0] != "origin" {
		t.Fatalf("Remotes = %v, %v; want [origin]", remotes, err)
	}
	if name, ok, err := Upstream(dir, Options{}); err != nil || ok || name != "" {
		t.Fatalf("Upstream = (%q, %v, %v); want no upstream", name, ok, err)
	}
}

// --- fake git ---------------------------------------------------------------

// fakeGit writes a recording git stub and returns its path plus the call log.
func fakeGit(t *testing.T, extraEnv map[string]string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
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
  remote) if [ -n "$FAKE_GIT_REMOTES" ]; then printf '%s\n' $FAKE_GIT_REMOTES; fi; exit 0 ;;
  diff) if [ -n "$FAKE_GIT_CONFLICT" ]; then printf '%s\n' "$FAKE_GIT_CONFLICT"; fi; exit 0 ;;
  ls-files) exit 0 ;;
  fetch) if [ -n "$FAKE_GIT_FETCH_FAIL" ]; then echo "$FAKE_GIT_FETCH_FAIL" >&2; exit 1; fi; exit 0 ;;
  pull) if [ -n "$FAKE_GIT_PULL_FAIL" ]; then echo "$FAKE_GIT_PULL_FAIL" >&2; exit 1; fi; exit 0 ;;
  push) exit 0 ;;
esac
exit 0
`
	path := filepath.Join(dir, "git")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	env := map[string]string{"FAKE_GIT_LOG": logPath}
	for k, v := range extraEnv {
		env[k] = v
	}
	t.Setenv("FAKE_GIT_LOG", logPath)
	for k, v := range env {
		t.Setenv(k, v)
	}
	return path, logPath
}

// envWith turns the fake-git environment into Options.Env entries (Run only
// passes Options.Env through, so every variable the stub reads must be here).
func envWith(logPath string) []string {
	env := os.Environ()
	for _, key := range []string{"FAKE_GIT_LOG", "FAKE_GIT_UPSTREAM", "FAKE_GIT_STATUS",
		"FAKE_GIT_REMOTES", "FAKE_GIT_CONFLICT", "FAKE_GIT_PULL_FAIL", "FAKE_GIT_FETCH_FAIL"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return env
}

func readCalls(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
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

func contains(calls []string, verb string) bool { return countCalls(calls, verb) > 0 }

func countCalls(calls []string, verb string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, verb+" ") || c == verb {
			n++
		}
	}
	return n
}
