// Behaviour tests for the read/write command families against a synthetic home
// (AC-P2-02 guards and dedup bands, AC-P2-04 quarantine, AC-P2-08 exit codes).
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContextRendersRules(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runHome(t, home, "", "context", "--project", "alpha-project", "--no-touch")
	mustCode(t, ExitOK, code, out, stderr, "context")
	if !strings.Contains(out, "[Memory —") || !strings.Contains(out, "## Rules (MUST follow)") {
		t.Fatalf("context output missing the injection block:\n%s", out)
	}
	if !strings.Contains(out, "[End of memory]") {
		t.Fatalf("context output missing the end marker:\n%s", out)
	}
	if strings.Contains(stderr, "# touched") {
		t.Fatalf("--no-touch still touched records: %q", stderr)
	}
}

// TestContextTouchesAndLogs: the session-start path bumps last_used and appends
// one inject row for the eval noise metric.
func TestContextTouchesAndLogs(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runHome(t, home, "", "context", "--project", "alpha-project")
	mustCode(t, ExitOK, code, out, stderr, "context")
	if !strings.Contains(stderr, "# touched") {
		t.Fatalf("context did not report the touch count: %q", stderr)
	}
	data, err := os.ReadFile(filepath.Join(home, "ops", "recall-log.jsonl"))
	if err != nil {
		t.Fatalf("recall log missing: %v", err)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(strings.SplitN(string(data), "\n", 2)[0]), &row); err != nil {
		t.Fatalf("recall log row: %v", err)
	}
	if row["type"] != "inject" || row["event"] != "session-start" {
		t.Fatalf("recall row = %v, want an inject/session-start row", row)
	}
	if ids, ok := row["ids"].([]any); !ok || len(ids) == 0 {
		t.Fatalf("recall row has no ids: %v", row)
	}
}

func TestSearchFindsAndMisses(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runHome(t, home, "", "search", "budgets")
	mustCode(t, ExitOK, code, out, stderr, "search budgets")
	if !strings.Contains(out, "[") || !strings.Contains(out, ".md") {
		t.Fatalf("search returned no ranked record:\n%s", out)
	}
	code, out, _ = runHome(t, home, "", "search", "zzzznothingmatchesthis")
	mustCode(t, ExitError, code, out, "", "search miss")
	if !strings.Contains(out, "(ไม่พบ)") {
		t.Fatalf("search miss printed %q, want (ไม่พบ)", out)
	}
}

func TestSearchScoresFlag(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runHome(t, home, "", "search", "budgets", "--scores")
	mustCode(t, ExitOK, code, out, stderr, "search --scores")
	if !strings.Contains(out, "score=") {
		t.Fatalf("--scores printed no score:\n%s", out)
	}
}

func TestListLimitAndStatus(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runHome(t, home, "", "list", "--limit", "2")
	mustCode(t, ExitOK, code, out, stderr, "list --limit 2")
	if got := len(strings.Split(strings.TrimSpace(out), "\n")); got > 2 {
		t.Fatalf("list --limit 2 returned %d lines:\n%s", got, out)
	}
	code, out, stderr = runHome(t, home, "", "list", "--status", "active")
	mustCode(t, ExitOK, code, out, stderr, "list --status active")
	if !strings.Contains(out, "[") || strings.Contains(out, "/superseded]") {
		t.Fatalf("list --status active printed non-active rows:\n%s", out)
	}
}

func TestShowByIDAndPath(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runHome(t, home, "", "show", "lesson.bbbb0001")
	mustCode(t, ExitOK, code, out, stderr, "show by id")
	if !strings.Contains(out, "id: lesson.bbbb0001") || !strings.Contains(out, "---") {
		t.Fatalf("show did not print the record:\n%s", out)
	}
	code, _, stderr = runHome(t, home, "", "show", "lesson.deadbeef")
	mustCode(t, ExitError, code, "", stderr, "show missing")
	if !strings.Contains(stderr, "ไม่พบ record") {
		t.Fatalf("show missing stderr = %q", stderr)
	}
	path := filepath.Join(home, "lessons", "dedup-base-lesson.bbbb0001.md")
	want, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatalf("read fixture: %v", rerr)
	}
	code, out, stderr = runHome(t, home, "", "show", "--path", path)
	mustCode(t, ExitOK, code, out, stderr, "show --path")
	if out != string(want) {
		t.Fatalf("show --path printed %q, want the file bytes", out)
	}
}

func TestAddWritesRecordAndCommits(t *testing.T) {
	home := fixtureHome(t)
	body := "**Rule.** Always smoke-test the sync path before trusting a green suite\n\n**NOT.** trusting a stale log\n\n**Evidence.** cli test\n"
	code, out, stderr := runHome(t, home, body, "add", "--layer", "lesson", "--scope", "global", "--file", "-", "--title", "cli add smoke")
	mustCode(t, ExitOK, code, out, stderr, "add")
	fields := strings.Fields(out)
	if len(fields) != 2 || !strings.HasPrefix(fields[0], "lesson.") || !strings.HasPrefix(fields[1], "lessons/") {
		t.Fatalf("add printed %q, want <id>\\t<rel>", out)
	}
	if data, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(fields[1]))); err != nil {
		t.Fatalf("record not written: %v", err)
	} else if !strings.Contains(string(data), "cli add smoke") {
		t.Fatalf("record body/title missing:\n%s", data)
	}
	if stderr != "" {
		t.Fatalf("add wrote to stderr: %q", stderr)
	}
	if log := git(t, home, "log", "--oneline", "-1"); !strings.Contains(log, "lesson(global)") {
		t.Fatalf("add did not commit locally: %s", log)
	}
}

// TestAddGuardsExitCodes is the discriminating guard test: a secret and an
// absolute path exit 2, a near-duplicate exits 3 (AC-P2-02).
func TestAddGuardsAndDedup(t *testing.T) {
	home := fixtureHome(t)

	code, _, stderr := runHome(t, home, "**Rule.** token: abcdefghijklmnopqrstuvwxyz123456\n",
		"add", "--layer", "lesson", "--scope", "global", "--file", "-")
	mustCode(t, ExitGuard, code, "", stderr, "add secret")

	code, _, stderr = runHome(t, home, "**Rule.** the file lives at /Users/someone/Work/secret.txt and matters\n",
		"add", "--layer", "lesson", "--scope", "global", "--file", "-")
	mustCode(t, ExitGuard, code, "", stderr, "add absolute path")

	// Same content twice in the same layer+scope: the second is a >=0.88
	// duplicate and must be blocked with exit 3.
	dup := "**Rule.** cache the index under .memory so the markdown tree stays canonical\n\n**NOT.** writing derived state next to the records\n"
	code, out, stderr := runHome(t, home, dup, "add", "--layer", "lesson", "--scope", "global", "--file", "-")
	mustCode(t, ExitOK, code, out, stderr, "add first copy")
	// One extra word changes the id but stays above the 0.88 duplicate band.
	near := dup + "\n"
	near = strings.Replace(near, "canonical", "canonical tree", 1)
	code, _, stderr = runHome(t, home, near, "add", "--layer", "lesson", "--scope", "global", "--file", "-")
	mustCode(t, ExitConflict, code, "", stderr, "add duplicate")
	if !strings.Contains(stderr, "similarity=") {
		t.Fatalf("dedup block did not report similarity: %q", stderr)
	}

	// --force treats the duplicate as intentional.
	code, out, stderr = runHome(t, home, near, "add", "--layer", "lesson", "--scope", "global", "--file", "-", "--force")
	mustCode(t, ExitOK, code, out, stderr, "add duplicate --force")
}

func TestAddExternalSourceNeedsEvidence(t *testing.T) {
	home := fixtureHome(t)
	body := "**Rule.** a captured page says budgets should double\n"
	code, _, stderr := runHome(t, home, body, "add", "--layer", "lesson", "--scope", "global", "--file", "-", "--source", "external:web")
	mustCode(t, ExitConflict, code, "", stderr, "external without evidence")

	// External with evidence is accepted; the caller declares quarantine and
	// verify then requires the external: source to back it.
	code, out, stderr := runHome(t, home, body, "add", "--layer", "lesson", "--scope", "global", "--file", "-",
		"--source", "external:web", "--evidence", "reviewed: trimmed to the fact", "--sensitivity", "quarantine")
	mustCode(t, ExitOK, code, out, stderr, "external with evidence")
	if !quietRecordHasQuarantine(t, home, strings.Fields(out)[0]) {
		t.Fatalf("external record not stored as quarantine (stderr %q)", stderr)
	}
}

func quietRecordHasQuarantine(t *testing.T, home, id string) bool {
	t.Helper()
	recs, err := filepath.Glob(filepath.Join(home, "lessons", "*.md"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, p := range recs {
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			t.Fatalf("read %s: %v", p, rerr)
		}
		if strings.Contains(string(data), "id: "+id) && strings.Contains(string(data), "sensitivity: quarantine") {
			return true
		}
	}
	return false
}

func TestAddUsageErrors(t *testing.T) {
	home := fixtureHome(t)
	for _, argv := range [][]string{
		{"add"},
		{"add", "--layer", "lesson"},
		{"add", "--layer", "lesson", "--scope", "global"},
	} {
		code, out, stderr := runHome(t, home, "", argv...)
		if code != ExitGuard {
			t.Errorf("%v: exit = %d, want %d (stdout %q, stderr %q)", argv, code, ExitGuard, out, stderr)
		}
	}
	// An empty body is a data error, not a usage error (Python die(..., 1)).
	code, _, stderr := runHome(t, home, "", "add", "--layer", "lesson", "--scope", "global", "--file", "-")
	if code != ExitError {
		t.Fatalf("add empty body: exit = %d, want %d (stderr %q)", code, ExitError, stderr)
	}
}

func TestCiteAndTouch(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runHome(t, home, "", "cite", "lesson.bbbb0001")
	mustCode(t, ExitOK, code, out, stderr, "cite")
	if !strings.Contains(out, "lesson.bbbb0001") {
		t.Fatalf("cite printed %q", out)
	}
	rec := readRecordFile(t, home, "lesson.bbbb0001")
	if !strings.Contains(rec, "uses: 1") {
		t.Fatalf("cite did not bump uses:\n%s", rec)
	}

	code, out, stderr = runHome(t, home, "", "touch", "lesson.bbbb0001")
	mustCode(t, ExitOK, code, out, stderr, "touch")
	if !strings.Contains(out, "lesson.bbbb0001") {
		t.Fatalf("touch printed %q", out)
	}

	code, _, stderr = runHome(t, home, "", "cite", "lesson.deadbeef")
	mustCode(t, ExitError, code, "", stderr, "cite missing")
}

func TestReviewRequiresEvidenceAndReleasesQuarantine(t *testing.T) {
	home := fixtureHome(t)
	code, _, stderr := runHome(t, home, "", "review", "lesson.bbbb0004")
	mustCode(t, ExitGuard, code, "", stderr, "review without evidence")
	if !strings.Contains(stderr, "--evidence") {
		t.Fatalf("review usage error = %q", stderr)
	}
	code, out, stderr := runHome(t, home, "", "review", "lesson.bbbb0004", "--evidence", "reviewed: read it, no secrets")
	mustCode(t, ExitOK, code, out, stderr, "review with evidence")
	if !strings.Contains(out, "reviewed") {
		t.Fatalf("review printed %q", out)
	}
	rec := readRecordFile(t, home, "lesson.bbbb0004")
	if !strings.Contains(rec, "sensitivity: internal") {
		t.Fatalf("review did not release quarantine:\n%s", rec)
	}
	if strings.Contains(rec, "sensitivity: quarantine") {
		t.Fatalf("review left the quarantine flag:\n%s", rec)
	}
}

func TestSupersedeChain(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runHome(t, home, "", "supersede", "lesson.bbbb0001", "--with", "lesson.bbbb0003")
	mustCode(t, ExitOK, code, out, stderr, "supersede")
	if !strings.Contains(out, "lesson.bbbb0001 -> superseded by lesson.bbbb0003") {
		t.Fatalf("supersede printed %q", out)
	}
	if old := readRecordFile(t, home, "lesson.bbbb0001"); !strings.Contains(old, "status: superseded") {
		t.Fatalf("old record not superseded:\n%s", old)
	}
	if next := readRecordFile(t, home, "lesson.bbbb0003"); !strings.Contains(next, "supersedes: lesson.bbbb0001") {
		t.Fatalf("new record missing supersedes:\n%s", next)
	}

	code, _, stderr = runHome(t, home, "", "supersede", "lesson.deadbeef", "--with", "lesson.bbbb0003")
	mustCode(t, ExitError, code, "", stderr, "supersede missing old")
}

func TestVerifyCleanAndBroken(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runHome(t, home, "", "verify")
	mustCode(t, ExitOK, code, out, stderr, "verify clean")
	if !strings.Contains(out, "0 problems") || !strings.Contains(out, "warn:") {
		t.Fatalf("verify output = %q, want 0 problems plus the fixture warning", out)
	}

	writeFile(t, home, "lessons/broken.md", "no front matter here\n")
	code, out, _ = runHome(t, home, "", "verify")
	mustCode(t, ExitError, code, out, "", "verify broken")
	if !strings.Contains(out, "FAIL:") || !strings.Contains(out, "broken.md") {
		t.Fatalf("verify did not report the broken file:\n%s", out)
	}
}

func TestReindexRebuildsIndex(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runHome(t, home, "", "reindex")
	mustCode(t, ExitOK, code, out, stderr, "reindex")
	if !strings.Contains(out, "index:") {
		t.Fatalf("reindex printed %q", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".memory", "index-engine.sqlite")); err != nil {
		t.Fatalf("index file missing: %v", err)
	}
}

// anyRecordContains reports whether some record file contains want.
func anyRecordContains(t *testing.T, home, want string) bool {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(home, "*", "*.md"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, p := range paths {
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			t.Fatalf("read %s: %v", p, rerr)
		}
		if strings.Contains(string(data), want) {
			return true
		}
	}
	return false
}

// readRecordFile returns the raw text of the record whose id matches.
func readRecordFile(t *testing.T, home, id string) string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(home, "*", "*.md"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, p := range paths {
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			t.Fatalf("read %s: %v", p, rerr)
		}
		if strings.Contains(string(data), "id: "+id) {
			return string(data)
		}
	}
	t.Fatalf("record %s not found", id)
	return ""
}

// TestAddNoCommit: --no-commit writes the record without a local commit and
// says nothing about a failure it never attempted.
func TestAddNoCommit(t *testing.T) {
	home := fixtureHome(t)
	before := git(t, home, "rev-list", "--count", "HEAD")
	body := "**Rule.** stage the record before deciding the commit boundary\n\n**NOT.** assuming sync will commit half a batch\n"
	code, out, stderr := runHome(t, home, body, "add", "--layer", "lesson", "--scope", "global", "--file", "-", "--no-commit")
	mustCode(t, ExitOK, code, out, stderr, "add --no-commit")
	fields := strings.Fields(out)
	if len(fields) != 2 {
		t.Fatalf("add --no-commit printed %q", out)
	}
	if _, err := os.Stat(filepath.Join(home, filepath.FromSlash(fields[1]))); err != nil {
		t.Fatalf("record not written: %v", err)
	}
	if after := git(t, home, "rev-list", "--count", "HEAD"); after != before {
		t.Fatalf("commits went from %s to %s with --no-commit", before, after)
	}
	if strings.Contains(stderr, "commit") {
		t.Fatalf("--no-commit reported a commit problem: %q", stderr)
	}
}

// TestImportFromDir: dry-run reports, --apply writes lessons with provenance
// and skips content that trips the write guard.
func TestImportFromDir(t *testing.T) {
	home := fixtureHome(t)
	src := t.TempDir()
	writeFile(t, src, "first.md", "# first memory\n\nRule: always run the gate before claiming green\n")
	writeFile(t, src, "second.md", "Rule: keep the derived index out of the canonical tree\n")
	writeFile(t, src, "MEMORY.md", "the index file must be ignored\n")
	writeFile(t, src, "leaky.md", "Rule: token: abcdefghijklmnopqrstuvwxyz123456\n")

	code, out, stderr := runHome(t, home, "", "import", "dir", "--path", src, "--source-label", "claude")
	mustCode(t, ExitOK, code, out, stderr, "import dry-run")
	if !strings.Contains(out, "dry-run") || !strings.Contains(out, "พบ 3 รายการ") {
		t.Fatalf("dry-run output = %q, want 3 rows and the dry-run note", out)
	}
	if before, _ := filepath.Glob(filepath.Join(home, "lessons", "*")); len(before) != 4 {
		t.Fatalf("dry-run wrote records: %d lesson files", len(before))
	}

	code, out, stderr = runHome(t, home, "", "import", "dir", "--path", src, "--source-label", "claude", "--apply")
	mustCode(t, ExitOK, code, out, stderr, "import --apply")
	if !strings.Contains(out, "skip") || !strings.Contains(out, "เพิ่ม 2 records") {
		t.Fatalf("apply output = %q, want one skip and two imported records", out)
	}
	if !anyRecordContains(t, home, "import:claude") {
		t.Fatalf("imported records carry no provenance")
	}
}

// TestImportRejectsBadSource covers the usage errors of the subcommand.
func TestImportRejectsBadSource(t *testing.T) {
	home := fixtureHome(t)
	code, _, stderr := runHome(t, home, "", "import", "nope")
	mustCode(t, ExitGuard, code, "", stderr, "import bad source")
	code, _, stderr = runHome(t, home, "", "import", "dir")
	mustCode(t, ExitGuard, code, "", stderr, "import dir without path")
	code, _, stderr = runHome(t, home, "", "import")
	mustCode(t, ExitGuard, code, "", stderr, "import without source")
}
