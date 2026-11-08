// Local bare-repo end-to-end sync tests: real git, no network, no remote host.
// They cover the P2.2 acceptance path — commit → fetch → pull --rebase
// --autostash → push — plus the conflict contract (REQ-SYNC-01/02).
package sync_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atomsbaza/my-memory-engine/internal/sync"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
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

// workspace is a bare "remote" plus two independent clones of it.
type workspace struct {
	bare string
	a    string
	b    string
}

// newWorkspace creates the bare repo, seeds it from clone A and clones B.
func newWorkspace(t *testing.T) workspace {
	t.Helper()
	base := t.TempDir()
	w := workspace{
		bare: filepath.Join(base, "remote.git"),
		a:    filepath.Join(base, "a"),
		b:    filepath.Join(base, "b"),
	}
	git(t, base, "init", "--bare", "-q", w.bare)
	git(t, base, "clone", "-q", w.bare, w.a)
	write(t, w.a, "lessons/seed.md", "seed\n")
	git(t, w.a, "add", "-A")
	git(t, w.a, "commit", "-q", "-m", "seed")
	git(t, w.a, "push", "-q", "-u", "origin", "HEAD")
	git(t, base, "clone", "-q", w.bare, w.b)
	return w
}

// TestLocalSyncEndToEnd: A commits and pushes; B pulls through the same
// pipeline and sees the file. No remote host, no credentials.
func TestLocalSyncEndToEnd(t *testing.T) {
	w := newWorkspace(t)

	write(t, w.a, "lessons/from-a.md", "written by a\n")
	res, err := sync.Run(w.a, sync.Options{})
	if err != nil {
		t.Fatalf("Run(a): %v", err)
	}
	if !res.Committed || !res.Pushed {
		t.Fatalf("result = %+v, want committed + pushed", res)
	}
	if res.Upstream != "origin/main" && res.Upstream != "origin/master" {
		t.Fatalf("Upstream = %q, want origin/<branch>", res.Upstream)
	}

	res, err = sync.Run(w.b, sync.Options{})
	if err != nil {
		t.Fatalf("Run(b): %v", err)
	}
	if !res.Pushed {
		t.Fatalf("B did not push its (empty) sync: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(w.b, "lessons", "from-a.md")); err != nil {
		t.Fatalf("B did not receive A's file after pull: %v", err)
	}
	if !sameHead(t, w.a, w.bare) || !sameHead(t, w.b, w.bare) {
		t.Fatalf("clones and the bare repo diverge after sync")
	}
}

// TestLocalSyncAutostashKeepsDirtyWork: A has an uncommitted change while B
// pushes; `pull --rebase --autostash` must preserve A's dirty file.
func TestLocalSyncAutostashKeepsDirtyWork(t *testing.T) {
	w := newWorkspace(t)

	write(t, w.b, "lessons/from-b.md", "written by b\n")
	if _, err := sync.Run(w.b, sync.Options{}); err != nil {
		t.Fatalf("Run(b): %v", err)
	}

	// A edits its own file without committing, then syncs (commit-all would
	// hide the autostash path, so keep it untracked-in-flight by syncing with
	// NoPush first... instead use a tracked file changed after A's own commit).
	write(t, w.a, "lessons/from-a.md", "a's committed work\n")
	if n, err := sync.CommitAll(w.a, "a: own work", sync.Options{}); err != nil || n != 1 {
		t.Fatalf("CommitAll(a) = (%d, %v), want (1, nil)", n, err)
	}
	// Now the dirty work that must survive the rebase:
	write(t, w.a, "lessons/dirty.md", "uncommitted scratch\n")

	res, err := sync.Run(w.a, sync.Options{})
	if err != nil {
		t.Fatalf("Run(a) with dirty tree: %v", err)
	}
	if !res.Pushed {
		t.Fatalf("result = %+v, want pushed", res)
	}
	if _, err := os.Stat(filepath.Join(w.a, "lessons", "from-b.md")); err != nil {
		t.Fatalf("A did not receive B's file: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(w.a, "lessons", "dirty.md"))
	if err != nil || string(data) != "uncommitted scratch\n" {
		t.Fatalf("autostash lost the dirty file: %v (%q)", err, data)
	}
}

// TestLocalSyncConflictNamesFileAndDoesNotPush: both clones rewrite the same
// record; the second sync must stop, name the file and leave the remote alone.
func TestLocalSyncConflictNamesFileAndDoesNotPush(t *testing.T) {
	w := newWorkspace(t)
	remoteBefore := git(t, w.bare, "rev-parse", "HEAD")

	// B rewrites the record first and pushes it.
	write(t, w.b, "lessons/seed.md", "seed from b\n")
	if _, err := sync.Run(w.b, sync.Options{}); err != nil {
		t.Fatalf("Run(b): %v", err)
	}
	remoteAfterB := git(t, w.bare, "rev-parse", "HEAD")

	// A rewrites the same record from its stale base, so its pull cannot rebase.
	write(t, w.a, "lessons/seed.md", "seed from a\n")
	_, err := sync.Run(w.a, sync.Options{})
	var ce *sync.ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *sync.ConflictError", err)
	}
	if len(ce.Files) == 0 || !strings.Contains(strings.Join(ce.Files, ","), "lessons/seed.md") {
		t.Fatalf("conflict files = %v, want lessons/seed.md", ce.Files)
	}
	if !strings.Contains(err.Error(), "lessons/seed.md") {
		t.Fatalf("error does not name the file: %q", err.Error())
	}
	if remoteAfterB != git(t, w.bare, "rev-parse", "HEAD") {
		t.Fatalf("a conflicted sync pushed to the remote")
	}
	if _, statErr := os.Stat(filepath.Join(w.a, ".git", "rebase-merge")); statErr != nil {
		if _, statErr2 := os.Stat(filepath.Join(w.a, ".git", "rebase-apply")); statErr2 != nil {
			t.Fatalf("no rebase state was left for a human to resolve")
		}
	}
	if remoteBefore == remoteAfterB {
		t.Fatalf("test setup is broken: B's push did not move the remote")
	}
}

// TestLocalSyncWithoutRemoteStaysOffline: a home with no remote commits and
// stops — the default path never reaches the network.
func TestLocalSyncWithoutRemoteStaysOffline(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	write(t, dir, "lessons/local.md", "local only\n")

	res, err := sync.Run(dir, sync.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Pushed || res.HasUpstream {
		t.Fatalf("result = %+v, want a local-only run", res)
	}
	notes := strings.Join(res.Notes, " ")
	if !strings.Contains(notes, "ยังไม่มี remote/upstream") {
		t.Fatalf("notes = %q, want the no-upstream note", notes)
	}
	if strings.TrimSpace(git(t, dir, "remote")) != "" {
		t.Fatalf("sync configured a remote on its own")
	}
}

// sameHead reports whether dir and the bare repo point at the same commit.
func sameHead(t *testing.T, dir, bare string) bool {
	t.Helper()
	local := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	remote := strings.TrimSpace(git(t, bare, "rev-parse", "HEAD"))
	return local == remote
}
