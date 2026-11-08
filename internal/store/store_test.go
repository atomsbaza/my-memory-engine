package store

// P1.4 LocalStore tests (AC-P1-05, AC-P1-06, AC-P1-07, AC-P1-10).
//
// All fixtures are synthetic (provenance: test-generated); no real corpus
// is used (NFR-PRIV-02). Tests are not parallel: several manipulate the
// process environment and working directory.

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atomsbaza/my-memory-engine/internal/codec"
	"github.com/atomsbaza/my-memory-engine/internal/schema"
)

var fixtureHome = filepath.Join("..", "..", "testdata", "store", "home")

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// copyTree copies a directory tree into dst (used to get a writable fixture
// home inside t.TempDir()).
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copyTree %s → %s: %v", src, dst, err)
	}
}

// chdir changes the working directory for the duration of the test.
func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})
}

// resolved makes paths comparable across the macOS /var ↔ /private/var
// symlink (FindRoot resolves symlinks like Python's Path.resolve()).
func resolved(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("abs %s: %v", p, err)
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	return abs
}

// withGitBin swaps the git executable for the duration of the test.
func withGitBin(t *testing.T, bin string) {
	t.Helper()
	old := gitBin
	gitBin = bin
	t.Cleanup(func() { gitBin = old })
}

// ---------------------------------------------------------------------------
// AC-P1-05 · FindRoot resolution order, fail loud
// ---------------------------------------------------------------------------

func TestFindRootOrder(t *testing.T) {
	home := t.TempDir()
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("MEM_HOME", "")
	t.Setenv("MY_MEMORY_HOME", "")

	// A resolvable env var must be honoured even when nothing exists on
	// disk yet (Python returns Path(env).resolve() unconditionally).
	t.Run("env path returned as-is", func(t *testing.T) {
		missing := filepath.Join(home, "does-not-exist")
		t.Setenv("MEM_HOME", missing)
		got, err := FindRoot()
		if err != nil {
			t.Fatalf("FindRoot: %v", err)
		}
		if got != resolved(t, missing) {
			t.Errorf("got %q, want %q", got, missing)
		}
	})

	t.Run("MEM_HOME wins over MY_MEMORY_HOME", func(t *testing.T) {
		a, b := t.TempDir(), t.TempDir()
		t.Setenv("MEM_HOME", a)
		t.Setenv("MY_MEMORY_HOME", b)
		got, err := FindRoot()
		if err != nil {
			t.Fatalf("FindRoot: %v", err)
		}
		if got != resolved(t, a) {
			t.Errorf("MEM_HOME must win: got %q, want %q", got, a)
		}
	})

	t.Run("MY_MEMORY_HOME used when MEM_HOME empty", func(t *testing.T) {
		b := t.TempDir()
		t.Setenv("MEM_HOME", "")
		t.Setenv("MY_MEMORY_HOME", b)
		got, err := FindRoot()
		if err != nil {
			t.Fatalf("FindRoot: %v", err)
		}
		if got != resolved(t, b) {
			t.Errorf("got %q, want %q", got, b)
		}
	})

	t.Run("tilde expansion", func(t *testing.T) {
		t.Setenv("MEM_HOME", "~/memroot")
		got, err := FindRoot()
		if err != nil {
			t.Fatalf("FindRoot: %v", err)
		}
		want := filepath.Join(fakeHome, "memroot")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("cwd walk-up finds repo", func(t *testing.T) {
		root := copyFixtureHome(t)
		t.Setenv("MEM_HOME", "")
		t.Setenv("MY_MEMORY_HOME", "")
		chdir(t, filepath.Join(root, "knowledge", "nested"))
		got, err := FindRoot()
		if err != nil {
			t.Fatalf("FindRoot: %v", err)
		}
		if got != resolved(t, root) {
			t.Errorf("walk-up: got %q, want %q", got, root)
		}
	})

	t.Run("documented default used when it is a repo", func(t *testing.T) {
		def := filepath.Join(fakeHome, "my-memory")
		if err := os.MkdirAll(filepath.Join(def, "schema"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(def, "schema", "taxonomy.json"), []byte(`{"version":1}`), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("MEM_HOME", "")
		t.Setenv("MY_MEMORY_HOME", "")
		chdir(t, t.TempDir()) // cwd has no repo above it except the OS temp tree
		got, err := FindRoot()
		if err != nil {
			t.Fatalf("FindRoot: %v", err)
		}
		if got != def {
			t.Errorf("default: got %q, want %q", got, def)
		}
	})

	t.Run("unresolvable fails loudly", func(t *testing.T) {
		t.Setenv("MEM_HOME", "")
		t.Setenv("MY_MEMORY_HOME", "")
		t.Setenv("HOME", t.TempDir()) // fresh home: no ~/my-memory default to find
		chdir(t, t.TempDir())
		got, err := FindRoot()
		if err == nil {
			t.Fatalf("expected loud failure, got root %q", got)
		}
		if !strings.Contains(err.Error(), "memory root not found") {
			t.Errorf("error is not diagnosable: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// AC-P1-06 · Scan skip rules, broken surfacing, stable counts
// ---------------------------------------------------------------------------

func copyFixtureHome(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	copyTree(t, fixtureHome, dst)
	return dst
}

func TestScanSkipsUnderscoreAndOps(t *testing.T) {
	root := copyFixtureHome(t)

	before := treeHash(t, root)
	rep, err := ScanWithReport(root)
	if err != nil {
		t.Fatalf("ScanWithReport: %v", err)
	}

	var rels []string
	for _, r := range rep.Records {
		rels = append(rels, r.Rel)
	}
	// Records come back in the taxonomy declaration order of their layer
	// (Python scan walks ctx.layers.items()), then by relative path.
	want := []string{
		"rules/pin-branch-per-feature-rule.1122aa33.md",
		"lessons/simple-fixture-lesson.aabbccdd.md",
		"knowledge/nested/deep-pointer-knowledge.4433ff00.md",
	}
	if fmt.Sprint(rels) != fmt.Sprint(want) {
		t.Errorf("records = %v\nwant      %v", rels, want)
	}

	// ops/ (append-only) and every "_"-prefixed component are skipped, the
	// broken file is surfaced instead of counted.
	if len(rep.Broken) != 1 || rep.Broken[0].Rel != "lessons/broken-no-frontmatter.md" {
		t.Fatalf("broken = %+v, want exactly lessons/broken-no-frontmatter.md", rep.Broken)
	}
	if rep.Broken[0].Problem == "" {
		t.Errorf("broken file must carry a problem description")
	}

	// Typed view.
	for _, r := range rep.Records {
		switch r.ID {
		case "lesson.aabbccdd":
			if r.Layer != "lesson" || r.Status != "active" || r.Uses != 3 || r.Successes != 2 || r.Decay != 0.75 {
				t.Errorf("lesson typed fields wrong: %+v", r)
			}
			if fmt.Sprint(r.Tags) != "[]" {
				t.Errorf("tags = %#v, want empty list", r.Tags)
			}
		case "knowledge.4433ff00":
			if r.Status != "active" {
				t.Errorf("missing status must default to active, got %q", r.Status)
			}
			if r.Title != "deep pointer" {
				t.Errorf("title = %q", r.Title)
			}
		case "rule.1122aa33":
			if fmt.Sprint(r.Tags) != "[git]" {
				t.Errorf("tags = %#v, want [git]", r.Tags)
			}
		}
	}

	// A no-op rescan must not move the count (REQ-STORE-02) and must not
	// modify any bytes.
	again, err := ScanWithReport(root)
	if err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if len(again.Records) != len(rep.Records) || len(again.Broken) != len(rep.Broken) {
		t.Errorf("no-op rescan changed counts: %d/%d → %d/%d",
			len(rep.Records), len(rep.Broken), len(again.Records), len(again.Broken))
	}
	if after := treeHash(t, root); after != before {
		t.Errorf("scan mutated the tree")
	}
}

// Discriminating control (AC-P1-06): with the append-only rule removed from
// the taxonomy, ops/ must be walked — its ledger (raw log lines, no
// front-matter) then surfaces as broken instead of staying invisible,
// proving the skip comes from file_mode, not from the directory name.
func TestScanControlWithoutAppendOnlyRule(t *testing.T) {
	root := copyFixtureHome(t)
	tax := filepath.Join(root, "schema", "taxonomy.json")
	data, err := os.ReadFile(tax)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "append-only") {
		t.Fatalf("fixture taxonomy lost its append-only marker")
	}
	if err := os.WriteFile(tax, []byte(strings.ReplaceAll(string(data), "append-only", "one-per-fact")), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := ScanWithReport(root)
	if err != nil {
		t.Fatalf("ScanWithReport: %v", err)
	}
	found := false
	for _, b := range rep.Broken {
		if b.Rel == "ops/ledger.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("without the append-only rule, ops/ledger.md must surface (broken = %+v)", rep.Broken)
	}
	if len(rep.Broken) != 2 || len(rep.Records) != 3 {
		t.Errorf("got %d records / %d broken, want 3 records / 2 broken", len(rep.Records), len(rep.Broken))
	}
}

// Python parity: scanning a root that does not exist yields no records and
// no error (absent layer directories are simply empty).
func TestScanMissingRootIsEmpty(t *testing.T) {
	recs, err := Scan(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("Scan missing root: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("records = %d, want 0", len(recs))
	}
}

// treeHash fingerprints every file's path+bytes under root.
func treeHash(t *testing.T, root string) string {
	t.Helper()
	var sb strings.Builder
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		sb.WriteString(rel)
		sb.WriteByte(':')
		sb.WriteString(fmt.Sprintf("%x", data))
		sb.WriteByte(';')
		return nil
	})
	if err != nil {
		t.Fatalf("treeHash: %v", err)
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// ReadRecord
// ---------------------------------------------------------------------------

func TestReadRecord(t *testing.T) {
	rec, err := ReadRecord(filepath.Join(fixtureHome, "lessons", "simple-fixture-lesson.aabbccdd.md"))
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	if rec.ID != "lesson.aabbccdd" || rec.Layer != "lesson" || rec.Scope != "global" {
		t.Errorf("identity fields wrong: %+v", rec)
	}
	if rec.Title != "simple fixture" {
		t.Errorf("title = %q", rec.Title)
	}
	if !strings.Contains(rec.Body, "Body text here.") {
		t.Errorf("body = %q", rec.Body)
	}
	if rec.Front["created"] != "2026-01-01" {
		t.Errorf("created = %#v", rec.Front["created"])
	}

	// Derived title: no title in front-matter → first body line, capped at
	// 80 runes (S4 edge case).
	noTitle := Record{ID: "lesson.00000001", Body: "first body line is the title\nsecond line\n"}
	if got := noTitle.TitleOrDerived(); got != "first body line is the title" {
		t.Errorf("TitleOrDerived = %q", got)
	}
	if (Record{ID: "lesson.00000002"}).TitleOrDerived() != "lesson.00000002" {
		t.Errorf("empty body must fall back to id")
	}
}

// ---------------------------------------------------------------------------
// Guard
// ---------------------------------------------------------------------------

func TestGuard(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string // substring of the error, "" for acceptance
	}{
		{"clean text accepted", "regression tests must fail before the fix lands", ""},
		{"secret refused", "the api_key = abcdefabcdefabcdef leaked", "secret"},
		{"github token refused", "ghp_AAAAABBBBBCCCCCDDDDDEEEEE", "secret"},
		{"absolute user path refused", "log lives at /Users/someone/notes.txt", "absolute path"},
		{"home path refused", "see /home/alice/project", "absolute path"},
		{"email refused", "contact alice@example.com for details", "personal data"},
		{"phone refused", "call 555-867-5309 now", "personal data"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Guard(tc.text)
			if tc.want == "" {
				if err != nil {
					t.Errorf("Guard(%q) = %v, want nil", tc.text, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Guard(%q) = %v, want error containing %q", tc.text, err, tc.want)
			}
		})
	}

	// GuardWith takes secret patterns from the schema, not from the store
	// defaults: a repo with its own pattern rejects its marker and accepts
	// text the default set would have refused.
	customRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(customRoot, "schema"), 0o755); err != nil {
		t.Fatal(err)
	}
	tax := `{"version":1,"secret_patterns":["ZZSECRET-[0-9]{4}"]}`
	if err := os.WriteFile(filepath.Join(customRoot, "schema", "taxonomy.json"), []byte(tax), 0o644); err != nil {
		t.Fatal(err)
	}
	sch, err := schema.LoadSchema(customRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := GuardWith(&sch, "marker ZZSECRET-1234 here"); err == nil || !strings.Contains(err.Error(), "secret") {
		t.Errorf("custom secret pattern must fire, got %v", err)
	}
	if err := GuardWith(&sch, "the api_key = abcdefabcdefabcdef leaked"); err != nil {
		t.Errorf("schema patterns replace defaults — default-only hit must pass, got %v", err)
	}
	if err := GuardWith(nil, "clean"); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Errorf("nil schema must fail loudly, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// AtomicWrite
// ---------------------------------------------------------------------------

func TestAtomicWriteBasics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lessons", "new-lesson.md")

	if err := AtomicWrite(path, []byte("one\n")); err != nil {
		t.Fatalf("AtomicWrite new: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "one\n" {
		t.Errorf("content = %q", got)
	}
	if err := AtomicWrite(path, []byte("two\n")); err != nil {
		t.Fatalf("AtomicWrite overwrite: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "two\n" {
		t.Errorf("overwritten content = %q", got)
	}
	if temps, _ := filepath.Glob(filepath.Join(dir, ".new-lesson.md.tmp-*")); len(temps) != 0 {
		t.Errorf("temp files left behind: %v", temps)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
}

// A failure injected between temp write and rename must leave the previous
// bytes untouched and clean up the temp file.
func TestAtomicWriteAbortBeforeRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.md")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	old := afterTempWrite
	afterTempWrite = func(tmp, dest string) error {
		return fmt.Errorf("injected failure")
	}
	t.Cleanup(func() { afterTempWrite = old })

	if err := AtomicWrite(path, []byte("after\n")); err == nil || !strings.Contains(err.Error(), "aborted before rename") {
		t.Fatalf("AtomicWrite = %v, want abort error", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "before\n" {
		t.Errorf("destination = %q, want previous bytes %q", got, "before\n")
	}
	if temps, _ := filepath.Glob(filepath.Join(dir, ".target.md.tmp-*")); len(temps) != 0 {
		t.Errorf("temp files left behind after handled failure: %v", temps)
	}
}

// A hard crash between temp write and rename (subprocess killed with no
// deferred cleanup) must still leave the previous bytes at the destination.
func TestAtomicWriteCrash(t *testing.T) {
	if os.Getenv("STORE_CRASH_CHILD") == "1" {
		// Child: crash hard once the temp file is fully written, before the
		// rename. os.Exit skips all deferred cleanup, like a real crash.
		afterTempWrite = func(tmp, dest string) error {
			fmt.Fprintf(os.Stderr, "child: temp written, crashing before rename of %s\n", dest)
			os.Exit(9)
			return nil // unreachable — the process is gone
		}
		if err := AtomicWrite(os.Getenv("STORE_CRASH_DEST"), []byte("after\n")); err != nil {
			fmt.Fprintf(os.Stderr, "child: AtomicWrite returned: %v\n", err)
		}
		os.Exit(7) // hook did not fire — broken test setup
	}

	dir := t.TempDir()
	dest := filepath.Join(dir, "target.md")
	if err := os.WriteFile(dest, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run", "^TestAtomicWriteCrash$")
	cmd.Env = append(os.Environ(), "STORE_CRASH_CHILD=1", "STORE_CRASH_DEST="+dest)
	runErr := cmd.Run()

	ee, ok := runErr.(*exec.ExitError)
	if !ok {
		t.Fatalf("child did not crash: %v", runErr)
	}
	if code := ee.ExitCode(); code != 9 {
		t.Fatalf("child exit code = %d, want 9 (hook must fire before rename)", code)
	}
	if got, _ := os.ReadFile(dest); string(got) != "before\n" {
		t.Fatalf("destination = %q — atomicity broken, want previous bytes %q", got, "before\n")
	}
	// A real crash leaves an orphan temp file (unique-named, inert).
	orphans, _ := filepath.Glob(filepath.Join(dir, ".target.md.tmp-*"))
	if len(orphans) == 0 {
		t.Errorf("expected an orphan temp file from the crash")
	}

	// And the next, healthy write still succeeds over the prior bytes.
	if err := AtomicWrite(dest, []byte("after\n")); err != nil {
		t.Fatalf("recovery write: %v", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "after\n" {
		t.Errorf("destination = %q after recovery, want %q", got, "after\n")
	}
}

// ---------------------------------------------------------------------------
// WriteRecord — no git / broken git / healthy git (AC-P1-07)
// ---------------------------------------------------------------------------

// writeFixture builds a minimal record for the write tests.
func writeFixture(layer, scope, title, body string) Record {
	return Record{Layer: layer, Scope: scope, Title: title, Body: body}
}

func runtimeLogLines(t *testing.T, root string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".memory", "runtime.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

// No-git write: the record survives, the commit failure is observable, and
// nothing lands in ops/.
func TestWriteRecordNoGit(t *testing.T) {
	root := copyFixtureHome(t)
	opsBefore := treeHash(t, filepath.Join(root, "ops"))
	if isGitRepo(root) {
		t.Fatalf("fixture root must not be a git repo for this test")
	}

	res, err := WriteRecord(root, writeFixture("lesson", "global", "persist without git",
		"record bytes must survive a missing git"))
	if err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	if res.Committed {
		t.Errorf("Committed = true, want false without a git repo")
	}
	if !strings.Contains(res.CommitReason, "not a git repository") {
		t.Errorf("CommitReason = %q, want it to name the missing repo", res.CommitReason)
	}

	if _, err := os.Stat(res.Path); err != nil {
		t.Fatalf("record file missing after write: %v", err)
	}
	if !strings.HasPrefix(res.Rel, "lessons/") || !strings.HasSuffix(res.Rel, ".md") {
		t.Errorf("Rel = %q, want lessons/<created>-<slug>.md", res.Rel)
	}
	back, err := ReadRecord(res.Path)
	if err != nil {
		t.Fatalf("ReadRecord back: %v", err)
	}
	wantID := codec.MakeID("lesson", "global", "persist without git", "record bytes must survive a missing git\n")
	if back.ID != wantID {
		t.Errorf("id = %q, want %q", back.ID, wantID)
	}
	if back.Front["status"] != "active" || back.Front["sensitivity"] != "internal" {
		t.Errorf("defaults not stamped: status=%#v sensitivity=%#v", back.Front["status"], back.Front["sensitivity"])
	}
	if _, ok := back.Front["schema_version"]; ok {
		t.Errorf("schema_version must not be stamped (Python writes none): %#v", back.Front["schema_version"])
	}

	// Exactly one runtime-log line per failed commit (NFR-OPS-01).
	lines := runtimeLogLines(t, root)
	if len(lines) != 1 {
		t.Fatalf("runtime log lines = %d, want 1: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], res.Rel) || !strings.Contains(lines[0], "not a git repository") {
		t.Errorf("runtime log line = %q, want rel + reason", lines[0])
	}

	// A second failed write appends exactly one more line.
	if _, err := WriteRecord(root, writeFixture("lesson", "global", "second record", "second body")); err != nil {
		t.Fatalf("second WriteRecord: %v", err)
	}
	if lines = runtimeLogLines(t, root); len(lines) != 2 {
		t.Errorf("runtime log lines = %d, want 2", len(lines))
	}

	// No ops/ ledger line: the append-only layer is never touched by a
	// record write (the fixture home ships ops/ledger.md — it must stay
	// byte-identical across the writes above).
	if opsAfter := treeHash(t, filepath.Join(root, "ops")); opsAfter != opsBefore {
		t.Errorf("ops/ changed by a record write")
	}
}

// Git binary missing entirely (gitBin points nowhere): still persisted,
// still observable.
func TestWriteRecordGitAbsent(t *testing.T) {
	root := copyFixtureHome(t)
	gitInit(t, root) // the repo exists; only the binary is missing
	withGitBin(t, filepath.Join(t.TempDir(), "definitely-not-git"))

	res, err := WriteRecord(root, writeFixture("lesson", "global", "git absent", "body for absent git"))
	if err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	if res.Committed || !strings.Contains(res.CommitReason, "git unavailable") {
		t.Errorf("result = %+v, want committed=false with 'git unavailable' reason", res)
	}
	if len(runtimeLogLines(t, root)) != 1 {
		t.Errorf("runtime log lines = %d, want 1", len(runtimeLogLines(t, root)))
	}
}

// A git repo whose commit fails: record persisted, committed=false with the
// git stderr surfaced, one runtime-log line.
func TestWriteRecordBrokenGit(t *testing.T) {
	root := copyFixtureHome(t)
	gitInit(t, root)

	fake := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'boom: simulated git failure' >&2\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	withGitBin(t, fake)

	res, err := WriteRecord(root, writeFixture("lesson", "global", "broken git", "body while git is broken"))
	if err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	if res.Committed {
		t.Fatalf("Committed = true despite failing git")
	}
	if !strings.Contains(res.CommitReason, "boom: simulated git failure") {
		t.Errorf("CommitReason = %q, want git stderr surfaced", res.CommitReason)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Errorf("record must persist despite commit failure: %v", err)
	}
	if lines := runtimeLogLines(t, root); len(lines) != 1 || !strings.Contains(lines[0], res.Rel) {
		t.Errorf("runtime log = %v, want one line naming %s", lines, res.Rel)
	}
}

// Healthy local git: committed=true, no reason, no runtime log, the commit
// contains exactly the record file.
func TestWriteRecordCommits(t *testing.T) {
	root := copyFixtureHome(t)
	gitInit(t, root)

	res, err := WriteRecord(root, writeFixture("rule", "global", "committed rule", "body of committed rule"))
	if err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	if !res.Committed || res.CommitReason != "" {
		t.Fatalf("result = %+v, want Committed=true with empty reason", res)
	}
	if lines := runtimeLogLines(t, root); len(lines) != 0 {
		t.Errorf("runtime log = %v, want none on a clean commit", lines)
	}

	out, err := gitOut(t, root, "log", "-1", "--pretty=%s")
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if strings.TrimSpace(out) != "rule(global): committed rule" {
		t.Errorf("commit subject = %q", strings.TrimSpace(out))
	}
	files, err := gitOut(t, root, "ls-files")
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	if !strings.Contains(files, res.Rel) {
		t.Errorf("record %s not staged by the commit; ls-files:\n%s", res.Rel, files)
	}
}

// Refusals: nothing may be written when the record is invalid or unsafe.
func TestWriteRecordRefusals(t *testing.T) {
	root := copyFixtureHome(t)

	cases := []struct {
		name  string
		rec   Record
		want  string
		noops bool // record count must stay 0
	}{
		{"unknown layer", writeFixture("nope", "global", "some title", "some body"), "unknown layer", true},
		{"append-only layer", writeFixture("ops", "global", "ledger entry", "ledger body"), "append-only", true},
		{"empty body", writeFixture("lesson", "global", "title only", "   "), "empty", true},
		{"missing scope", Record{Layer: "lesson", Title: "no scope", Body: "body"}, `"scope"`, true},
		{"secret in body", writeFixture("lesson", "global", "leaky title", "api_key = abcdefabcdefabcdef"), "secret", true},
		{"absolute path in body", writeFixture("lesson", "global", "pathy title", "stored under /Users/alice/x"), "absolute path", true},
		{"pii in body", writeFixture("lesson", "global", "maily title", "reach me at bob@example.org"), "personal data", true},
		{"bad status", Record{Layer: "lesson", Scope: "global", Title: "bad status", Body: "body", Front: codec.Frontmatter{"status": "zombie"}}, "status", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := WriteRecord(root, tc.rec)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("WriteRecord err = %v, want it to contain %q", err, tc.want)
			}
			recs, err := Scan(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(recs) != 3 {
				t.Errorf("refusal must not write anything; records = %d (%v)", len(recs), recs)
			}
		})
	}

	// Duplicate id: the second write of the same fact is refused.
	first, err := WriteRecord(root, writeFixture("lesson", "global", "original fact", "one body"))
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	firstRec, err := ReadRecord(first.Path)
	if err != nil {
		t.Fatalf("read back first write: %v", err)
	}
	_, err = WriteRecord(root, Record{
		Layer: "lesson", Scope: "global", Title: "same fact again",
		Body:  "one body",
		Front: codec.Frontmatter{"id": firstRec.ID},
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate write err = %v, want 'already exists'", err)
	}
	if recs, _ := Scan(root); len(recs) != 4 {
		t.Errorf("records = %d, want 4 (no duplicate written)", len(recs))
	}
}

// Derived title: no title given → first body line becomes title and feeds
// the id (S4 parity).
func TestWriteRecordDerivedTitle(t *testing.T) {
	root := copyFixtureHome(t)
	res, err := WriteRecord(root, Record{Layer: "lesson", Scope: "global", Body: "derived title line\nmore body\n"})
	if err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	back, err := ReadRecord(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Title != "derived title line" {
		t.Errorf("title = %q, want derived first body line", back.Title)
	}
	wantID := codec.MakeID("lesson", "global", "derived title line", "derived title line\nmore body\n")
	if back.ID != wantID {
		t.Errorf("id = %q, want %q (derived title must feed the id formula)", back.ID, wantID)
	}
}

// ---------------------------------------------------------------------------
// flock
// ---------------------------------------------------------------------------

func TestFlockAcquireRelease(t *testing.T) {
	root := t.TempDir()
	unlock, err := lockRoot(root)
	if err != nil {
		t.Fatalf("lockRoot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".memory", ".lock")); err != nil {
		t.Errorf("lock file missing: %v", err)
	}
	unlock()

	// Sequential re-acquisition must work after release.
	unlock2, err := lockRoot(root)
	if err != nil {
		t.Fatalf("re-lock after unlock: %v", err)
	}
	unlock2()
}

// ---------------------------------------------------------------------------
// AC-P1-10 · no network on the default path
// ---------------------------------------------------------------------------

// netDeps returns the dial-capable packages found in a `go list -deps`
// output. net/url is allowed (pure parsing, no sockets).
func netDeps(deps []string) []string {
	exact := map[string]bool{
		"net": true, "net/http": true, "net/smtp": true, "net/rpc": true,
		"net/rpc/jsonrpc": true, "net/mail": true, "net/textproto": true,
		"net/cgi": true, "crypto/tls": true,
	}
	var hits []string
	for _, d := range deps {
		if exact[d] || (strings.HasPrefix(d, "net/") && d != "net/url") {
			hits = append(hits, d)
		}
	}
	return hits
}

func runGoList(t *testing.T, dir, target string) []string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go binary unavailable: %v", err)
	}
	cmd := exec.Command(goBin, "list", "-deps", target)
	cmd.Dir = dir
	// Hard-offline: no module downloads, no toolchain fetches.
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOTOOLCHAIN=local")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list -deps %s: %v\n%s", target, err, ee.Stderr)
		}
		t.Fatalf("go list -deps %s: %v", target, err)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// The store package's dependency graph must contain no dial-capable
// package: store read/write/scan/commit is local-only by construction.
func TestNoNetworkDeps(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if hits := netDeps(runGoList(t, repoRoot, "./internal/store")); len(hits) != 0 {
		t.Errorf("internal/store gained network-capable dependencies: %v", hits)
	}
}

// Discriminating control (AC-P1-10): the same detector must flag a package
// that does import a networking package — otherwise the gate above proves
// nothing.
func TestNoNetworkDetectorDiscriminates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module nnctl\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nimport _ \"net/http\"\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hits := netDeps(runGoList(t, dir, "."))
	found := false
	for _, h := range hits {
		if h == "net" || h == "net/http" {
			found = true
		}
	}
	if !found {
		t.Errorf("detector failed to flag a control package importing net/http (hits: %v)", hits)
	}
}

// ---------------------------------------------------------------------------
// helpers: git
// ---------------------------------------------------------------------------

func gitInit(t *testing.T, root string) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git unavailable: %v", err)
	}
	if out, err := exec.Command(git, "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}

func gitOut(t *testing.T, root string, args ...string) (string, error) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git unavailable: %v", err)
	}
	out, err := exec.Command(git, append([]string{"-C", root}, args...)...).Output()
	return string(out), err
}
