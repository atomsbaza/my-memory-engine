// Atomic persistence + observable commit for the local store (P1.4).
//
// REQ-STORE-03 / AC-P1-07: a write is a temp file + rename in the same
// directory (atomic); a missing git or a failed local commit never loses the
// record — it stays on disk with committed=false, a reason and exactly one
// runtime-log line under .memory/. Errors are never swallowed.

package store

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/atomsbaza/my-memory-engine/internal/codec"
	"github.com/atomsbaza/my-memory-engine/internal/schema"
)

// ---------------------------------------------------------------------------
// AtomicWrite
// ---------------------------------------------------------------------------

// afterTempWrite is a fault-injection point for tests: when non-nil it runs
// after the temp file is fully written and fsynced but before the rename.
// A non-nil error aborts the write and removes the temp file; a hard kill
// inside the hook simulates a crash between temp write and rename.
var afterTempWrite func(tmpPath, destPath string) error

// AtomicWrite writes content to path atomically: a temp file in the same
// directory is written, fsynced and then renamed over the destination. A
// crash at any point leaves either the previous bytes or the new bytes at
// path — never a partial write.
func AtomicWrite(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("store: atomic write %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("store: atomic write %s: create temp: %w", path, err)
	}
	renamed := false
	defer func() {
		if !renamed {
			// We are alive and failed — clean the temp file. (A real crash
			// never runs this; the orphan temp is inert and unique-named.)
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("store: atomic write %s: write temp: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("store: atomic write %s: fsync temp: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: atomic write %s: close temp: %w", path, err)
	}
	// Records are plain markdown in a shared repo: match the usual 0644
	// (CreateTemp creates 0600).
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return fmt.Errorf("store: atomic write %s: chmod temp: %w", path, err)
	}
	if afterTempWrite != nil {
		if err := afterTempWrite(tmp.Name(), path); err != nil {
			return fmt.Errorf("store: atomic write %s: aborted before rename: %w", path, err)
		}
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("store: atomic write %s: rename: %w", path, err)
	}
	renamed = true
	// Best-effort directory fsync so the rename itself is durable. Some
	// filesystems do not support syncing a directory fd; the rename is
	// already atomic, so this is deliberately not a failure path.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// ---------------------------------------------------------------------------
// WriteRecord — validate → guard → atomic write → local commit
// ---------------------------------------------------------------------------

// WriteResult reports where a record landed and whether the local git commit
// succeeded. Committed==false always comes with a non-empty CommitReason and
// one runtime-log line (REQ-STORE-03); it is not an error return — the
// record itself is safely persisted.
type WriteResult struct {
	Path         string
	Rel          string // path relative to root, slash-separated
	Committed    bool
	CommitReason string // non-empty iff !Committed
}

// WriteRecord persists rec under root: it merges the record's front-matter,
// fills defaults (id via the sha1 formula, title derived from the first body
// line, created/status/sensitivity), validates against the root's schema,
// guards the content, then writes atomically under an exclusive flock and
// attempts a local git commit of just that file.
//
// The append-only layer (ops/) cannot be written through this path — it is a
// plain append log, mirroring Python mem add.
func WriteRecord(root string, rec Record) (WriteResult, error) {
	sch, err := schema.LoadSchema(root)
	if err != nil {
		return WriteResult{}, fmt.Errorf("store: write %s: %w", root, err)
	}

	body := strings.TrimSpace(rec.Body)
	if body == "" {
		return WriteResult{}, fmt.Errorf("store: record body is empty — nothing to write")
	}
	body += "\n"

	// Front-matter: explicit Front entries win; typed fields fill the gaps.
	fm := codec.Frontmatter{}
	for k, v := range rec.Front {
		fm[k] = v
	}
	setIfNonEmpty(fm, "id", rec.ID)
	setIfNonEmpty(fm, "title", rec.Title)
	setIfNonEmpty(fm, "layer", rec.Layer)
	setIfNonEmpty(fm, "scope", rec.Scope)
	setIfNonEmpty(fm, "status", rec.Status)
	setIfNonEmpty(fm, "sensitivity", rec.Sensitivity)
	setIfNonEmpty(fm, "created", rec.Created)
	setIfNonEmpty(fm, "expires", rec.Expires)
	if len(rec.Tags) > 0 {
		setIfAbsent(fm, keyTags, rec.Tags)
	}
	if rec.Uses > 0 {
		setIfAbsent(fm, "uses", rec.Uses)
	}
	if rec.Successes > 0 {
		setIfAbsent(fm, "successes", rec.Successes)
	}

	layer := strField(fm, "layer")
	def, ok := sch.Layers[layer]
	if !ok {
		return WriteResult{}, fmt.Errorf("store: unknown layer %q (known: %s)", layer, strings.Join(sortedLayerNames(sch.Layers), ", "))
	}
	if def.FileMode == "append-only" {
		return WriteResult{}, fmt.Errorf("store: layer %q is an append-only log (%s/) — records cannot be written through the store; append to the log directly", layer, def.Dir)
	}

	scope := strField(fm, "scope")
	if scope == "" {
		return WriteResult{}, fmt.Errorf("store: record requires a %q (global | project:<Name> | repo:<owner/name> | agent:<name>)", "scope")
	}

	title := strField(fm, "title")
	if title == "" {
		title = deriveTitle(body)
		fm["title"] = title
	}
	id := strField(fm, "id")
	if id == "" {
		id = codec.MakeID(layer, scope, title, body)
		fm["id"] = id
	}
	setDefault(fm, "status", "active")
	setDefault(fm, "sensitivity", "internal")
	setDefault(fm, "created", time.Now().Format("2006-01-02"))

	sch.Stamp(fm)
	if err := sch.ValidateRecord(fm); err != nil {
		return WriteResult{}, fmt.Errorf("store: record refused: %w", err)
	}

	text := codec.DumpFront(fm) + "\n" + strings.TrimLeft(body, "\n")
	if err := GuardWith(&sch, title+"\n"+body); err != nil {
		return WriteResult{}, err
	}

	rel := def.Dir + "/" + codec.Filename(layer, title, id)
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return WriteResult{}, fmt.Errorf("store: write %s: %w", rel, err)
	}
	if _, err := os.Stat(path); err == nil {
		return WriteResult{}, fmt.Errorf("store: %s already exists — refusing to overwrite (supersede is a later-phase operation)", rel)
	}
	existing, err := Scan(root)
	if err != nil {
		return WriteResult{}, err
	}
	for _, r := range existing {
		if r.ID == id {
			return WriteResult{}, fmt.Errorf("store: id %s already exists at %s — refusing duplicate write", id, r.Rel)
		}
	}

	unlock, err := lockRoot(root)
	if err != nil {
		return WriteResult{}, err
	}
	defer unlock()

	if err := AtomicWrite(path, []byte(text)); err != nil {
		return WriteResult{}, err
	}

	res := WriteResult{Path: path, Rel: rel}
	if cerr := commitFile(root, rel, layer, scope, title); cerr != nil {
		res.CommitReason = cerr.Error()
		if lerr := appendRuntimeLog(root, rel, cerr); lerr != nil {
			return res, fmt.Errorf("store: record persisted at %s but the runtime log failed: %v (commit failure was: %v)", rel, lerr, cerr)
		}
		return res, nil
	}
	res.Committed = true
	return res, nil
}

func setIfAbsent(fm codec.Frontmatter, key string, val any) {
	if _, ok := fm[key]; !ok {
		fm[key] = val
	}
}

// setIfNonEmpty fills key from a typed field only when that field carries a
// value (an empty typed field must not shadow or poison the entry).
func setIfNonEmpty(fm codec.Frontmatter, key, val string) {
	if val != "" {
		setIfAbsent(fm, key, val)
	}
}

// setDefault fills key when it is absent or empty (defaults never mask an
// explicit value).
func setDefault(fm codec.Frontmatter, key, val string) {
	if v, ok := fm[key]; !ok || v == "" {
		fm[key] = val
	}
}

// deriveTitle returns the first body line truncated to 80 runes — the same
// derivation TitleOrDerived applies on read (S4: the derived title feeds the
// id formula, so write and recompute must agree).
func deriveTitle(body string) string {
	line := strings.TrimSpace(body)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	runes := []rune(line)
	if len(runes) > 80 {
		runes = runes[:80]
	}
	return string(runes)
}

// ---------------------------------------------------------------------------
// Local git commit — failure stays observable
// ---------------------------------------------------------------------------

// gitBin is the git executable used for local commits; tests replace it to
// inject failures.
var gitBin = "git"

func isGitRepo(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".git"))
	return err == nil
}

// commitFile stages and commits exactly one record file locally with the
// standard add message. It never pushes and never touches the network
// (sync is a later phase).
func commitFile(root, rel, layer, scope, title string) error {
	return commitMsg(root, rel, fmt.Sprintf("%s(%s): %s", layer, scope, truncRunes(title, 72)))
}

// CommitPath stages and commits exactly one file with an explicit message.
// It is the exported seam for domain lifecycle commits that do not follow
// the "<layer>(<scope>): <title>" add format (review, supersede). It never
// pushes and never touches the network.
func CommitPath(root, rel, msg string) error {
	return commitMsg(root, rel, msg)
}

// commitMsg stages and commits exactly one file. A non-repo root, a missing
// git binary or a failed commit is returned as an error — the caller keeps
// the already-persisted file and reports the failure (never a silent skip).
func commitMsg(root, rel, msg string) error {
	if !isGitRepo(root) {
		return fmt.Errorf("%s is not a git repository — record persisted but not committed", root)
	}
	git, err := exec.LookPath(gitBin)
	if err != nil {
		return fmt.Errorf("git unavailable: %w — record persisted but not committed", err)
	}
	add := exec.Command(git, "-C", root, "add", "--", rel)
	if out, err := add.CombinedOutput(); err != nil {
		return fmt.Errorf("git add %s: %w: %s", rel, err, truncRunes(firstLine(string(out)), 200))
	}
	// Hermetic identity: commits must not depend on the machine's global
// git config.
	commit := exec.Command(git, "-C", root,
		"-c", "user.name=mem-engine", "-c", "user.email=mem-engine@localhost",
		"commit", "-m", msg, "--", rel)
	if out, err := commit.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit %s: %w: %s", rel, err, truncRunes(firstLine(string(out)), 200))
	}
	return nil
}

// WithLock runs fn while holding the store's exclusive flock on
// <root>/.memory/.lock. It is the exported seam for read-modify-write
// cycles above the store layer (domain cite/review/supersede), mirroring
// the Python engine's per-command locks. Callers must not invoke
// WriteRecord inside fn — WriteRecord takes the same lock itself and a
// second exclusive flock from one process would self-deadlock.
func WithLock(root string, fn func() error) error {
	unlock, err := lockRoot(root)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

// runtimeLogName is the observable partial-failure log under the root's
// .memory/ directory (one line per failed commit).
const runtimeLogName = ".memory/runtime.log"

func appendRuntimeLog(root, rel string, reason error) error {
	dir := filepath.Join(root, ".memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	f, err := os.OpenFile(filepath.Join(root, filepath.FromSlash(runtimeLogName)),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open runtime log: %w", err)
	}
	defer f.Close()
	line := time.Now().Format(time.RFC3339) + "\tcommit-failed\t" + rel + "\t" +
		truncRunes(firstLine(reason.Error()), 200) + "\n"
	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("append runtime log: %w", err)
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
