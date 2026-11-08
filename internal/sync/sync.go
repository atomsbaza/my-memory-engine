// Package sync implements the local Git synchronisation of a memory home
// (P2.2, REQ-SYNC-01 / REQ-SYNC-02): stage everything, commit locally, then
// fetch → pull --rebase --autostash → push against whatever upstream the home
// has configured.
//
// Contract boundaries this package keeps:
//
//   - git CLI only (subprocess), never a provider REST API and never stored
//     credentials — authentication is the user's git credential helper
//     (NFR-SEC-01);
//   - no `--force` anywhere: the argument is refused outright, so a push that
//     needs rewriting history fails loudly instead of destroying a peer's work
//     (design §7, REQ-SYNC-02);
//   - a home with no remote/upstream never reaches the network: Run reports
//     "skipped" before fetch/pull/push (NFR-NET-01);
//   - a conflict stops the run, names the files and returns *ConflictError
//     with no push attempt (REQ-SYNC-02). The rebase is deliberately left in
//     place for a human to resolve — auto-aborting or auto-merging would hide
//     which side won;
//   - every failure is a typed, readable error carrying the git stderr, never
//     a swallowed exit status (NFR-OPS-01).
package sync

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultRetries is the Python baseline's push attempt budget.
const DefaultRetries = 3

// Options configures one sync run.
type Options struct {
	// Bin is the git executable; "" means "git" from PATH. Tests point it at
	// a recorder/failing stub to prove behaviour without a network.
	Bin string
	// Message is the explicit commit message. "" derives
	// "mem: sync (<n> files)" like the Python baseline.
	Message string
	// NoPush stops after the local commit.
	NoPush bool
	// Retries is the number of fetch/pull/push attempts; 0 → DefaultRetries.
	Retries int
	// Env is appended to the process environment for every git call.
	Env []string
	// Log receives one progress line per step; nil discards them.
	Log io.Writer
}

// Result reports what a run did. Every field is meaningful even when the run
// failed, so callers can print a partial outcome instead of only an error.
type Result struct {
	Committed      bool
	CommittedFiles int
	CommitMessage  string

	HasUpstream bool
	Upstream    string
	Pushed      bool
	Attempts    int

	Notes []string
}

// GitError is a failed git invocation: the arguments, git's stderr and the
// underlying process error. It is the observable-failure contract for callers
// that branch on the cause (NFR-OPS-01).
type GitError struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *GitError) Error() string {
	cmd := "git " + strings.Join(e.Args, " ")
	if msg := firstLine(e.Stderr); msg != "" {
		return fmt.Sprintf("%s: %s", cmd, msg)
	}
	return fmt.Sprintf("%s: %v", cmd, e.Err)
}

func (e *GitError) Unwrap() error { return e.Err }

// ConflictError is a rebase/merge conflict: pull stopped, the named files need
// a human, and nothing was pushed (REQ-SYNC-02).
type ConflictError struct {
	Files  []string
	Stderr string
	Err    error
}

func (e *ConflictError) Error() string {
	files := "(git ไม่ได้ระบุชื่อไฟล์)"
	if len(e.Files) > 0 {
		files = strings.Join(e.Files, ", ")
	}
	return "conflict — ต้องแก้ด้วยมือ ห้าม force push: " + files
}

func (e *ConflictError) Unwrap() error { return e.Err }

// IsRepo reports whether root is a git work tree. A missing git binary is an
// error (it is an installation fault), a plain non-repo directory is not.
func IsRepo(root string, o Options) (bool, error) {
	out, err := o.git(root, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		var ge *GitError
		if errors.As(err, &ge) {
			// "not a git repository" is the normal answer for a non-repo home.
			if strings.Contains(ge.Stderr, "not a git repository") {
				return false, nil
			}
			if errors.Is(ge.Err, exec.ErrNotFound) {
				return false, err
			}
		}
		return false, nil
	}
	return strings.TrimSpace(out) == "true", nil
}

// Upstream returns the configured upstream ref of HEAD ("origin/main") and
// whether one exists at all.
func Upstream(root string, o Options) (string, bool, error) {
	out, err := o.git(root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err != nil {
		return "", false, nil // no upstream is a state, not a failure
	}
	name := strings.TrimSpace(out)
	return name, name != "", nil
}

// Remotes lists the configured remotes, so doctor/sync can report "local only"
// without a network call (`git remote -v` is a config read).
func Remotes(root string, o Options) ([]string, error) {
	out, err := o.git(root, "remote")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// Dirty returns the porcelain paths of uncommitted changes (relative to root).
func Dirty(root string, o Options) ([]string, error) {
	out, err := o.git(root, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	return porcelainPaths(out), nil
}

// CommitAll stages every change and commits it when there is anything to
// commit. It returns the number of staged paths (0 = nothing to do).
func CommitAll(root, message string, o Options) (int, error) {
	if _, err := o.git(root, "add", "-A"); err != nil {
		return 0, err
	}
	paths, err := Dirty(root, o)
	if err != nil {
		return 0, err
	}
	if len(paths) == 0 {
		return 0, nil
	}
	if message == "" {
		message = fmt.Sprintf("mem: sync (%d files)", len(paths))
	}
	if _, err := o.git(root, "commit", "-m", message); err != nil {
		return len(paths), err
	}
	return len(paths), nil
}

// ConflictFiles lists the unmerged paths of an in-progress rebase/merge.
func ConflictFiles(root string, o Options) ([]string, error) {
	seen := map[string]bool{}
	var files []string
	add := func(list []string) {
		for _, f := range list {
			if f != "" && !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	if out, err := o.git(root, "diff", "--name-only", "--diff-filter=U"); err == nil {
		add(splitLines(out))
	}
	if out, err := o.git(root, "ls-files", "-u"); err == nil {
		// "<mode> <sha> <stage>\t<path>" per stage — keep the path column.
		var paths []string
		for _, line := range strings.Split(out, "\n") {
			if _, path, ok := strings.Cut(line, "\t"); ok {
				paths = append(paths, path)
			}
		}
		add(paths)
	}
	sort.Strings(files)
	return files, nil
}

// Run performs the full sync: commit locally, then (unless NoPush or no
// upstream) fetch/pull --rebase --autostash/push with up to o.Retries attempts.
//
// A conflict returns *ConflictError; any other git failure returns *GitError.
// Both carry Result for the caller's report.
func Run(root string, o Options) (Result, error) {
	res := Result{}
	if o.Retries <= 0 {
		o.Retries = DefaultRetries
	}

	repo, err := IsRepo(root, o)
	if err != nil {
		return res, err
	}
	if !repo {
		return res, fmt.Errorf("repo นี้ยังไม่ใช่ git repo — `git init` ก่อน")
	}

	n, err := CommitAll(root, o.Message, o)
	res.CommittedFiles = n
	if n > 0 {
		res.Committed = true
		res.CommitMessage = o.Message
		if res.CommitMessage == "" {
			res.CommitMessage = fmt.Sprintf("mem: sync (%d files)", n)
		}
		o.logf("committed %d files", n)
	} else {
		o.logf("ไม่มีอะไรเปลี่ยน")
	}
	if err != nil {
		return res, err
	}

	if o.NoPush {
		res.Notes = append(res.Notes, "ข้ามการ push (--no-push)")
		return res, nil
	}

	upstream, ok, err := Upstream(root, o)
	if err != nil {
		return res, err
	}
	if !ok {
		res.Notes = append(res.Notes, "ยังไม่มี remote/upstream — ข้ามการ push")
		return res, nil
	}
	res.HasUpstream = true
	res.Upstream = upstream

	var lastErr error
	for attempt := 1; attempt <= o.Retries; attempt++ {
		res.Attempts = attempt
		if err := o.syncOnce(root, upstream); err != nil {
			lastErr = err
			var ce *ConflictError
			if errors.As(err, &ce) {
				res.Notes = append(res.Notes, "rebase ค้างอยู่ — แก้ conflict ให้จบ (git rebase --continue / --abort) แล้วรัน `mem sync` อีกครั้ง")
				return res, err
			}
			o.logf("mem: push ล้มเหลวครั้งที่ %d: %v", attempt, err)
			res.Notes = append(res.Notes, fmt.Sprintf("ครั้งที่ %d ล้มเหลว: %v", attempt, firstLine(gitStderr(err))))
			continue
		}
		res.Pushed = true
		o.logf("pushed -> %s", upstream)
		return res, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("push ไม่สำเร็จ %d ครั้ง — ต้องแก้ด้วยมือ (ห้าม force push)", o.Retries)
	}
	return res, fmt.Errorf("push ไม่สำเร็จ %d ครั้ง — ต้องแก้ด้วยมือ (ห้าม force push): %w", o.Retries, lastErr)
}

// syncOnce is one fetch → pull --rebase --autostash → push cycle. A conflict
// is detected before returning so the caller never pushes mid-rebase.
func (o Options) syncOnce(root, upstream string) error {
	for _, args := range [][]string{
		{"fetch", "--quiet"},
		{"pull", "--rebase", "--autostash", "--quiet"},
		{"push", "--quiet"},
	} {
		if _, err := o.git(root, args...); err != nil {
			if files, ferr := ConflictFiles(root, o); ferr == nil && len(files) > 0 {
				return &ConflictError{Files: files, Stderr: gitStderr(err), Err: err}
			}
			return err
		}
	}
	return nil
}

// git runs one git command in root and returns its stdout. Force flags are
// refused before the process starts (REQ-SYNC-02 invariant).
func (o Options) git(root string, args ...string) (string, error) {
	for _, a := range args {
		if a == "--force" || a == "-f" || strings.HasPrefix(a, "--force-with-lease") {
			return "", fmt.Errorf("sync: refusing a force argument %q — force push/pull is never allowed", a)
		}
	}
	bin := o.Bin
	if bin == "" {
		bin = "git"
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = root
	if len(o.Env) > 0 {
		cmd.Env = append(os.Environ(), o.Env...)
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), &GitError{
			Args:   args,
			Stderr: strings.TrimSpace(stderr.String()),
			Err:    err,
		}
	}
	return stdout.String(), nil
}

func (o Options) logf(format string, a ...any) {
	if o.Log == nil {
		return
	}
	fmt.Fprintf(o.Log, format+"\n", a...)
}

// porcelainPaths extracts the path of each "XY path" / "XY old -> new" line.
func porcelainPaths(status string) []string {
	var paths []string
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if _, after, ok := strings.Cut(path, "->"); ok {
			path = after
		}
		path = strings.Trim(strings.TrimSpace(path), `"`)
		if path != "" {
			paths = append(paths, filepath.ToSlash(path))
		}
	}
	return paths
}

func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, filepath.ToSlash(line))
		}
	}
	return out
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func gitStderr(err error) string {
	var ge *GitError
	if errors.As(err, &ge) {
		return ge.Stderr
	}
	return err.Error()
}
