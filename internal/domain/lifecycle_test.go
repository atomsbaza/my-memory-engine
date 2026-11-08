// P2.1 lifecycle tests: supersede chains, cite counters, the review-only
// quarantine escape, and touch. Synthetic fixtures only.
package domain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atomsbaza/my-memory-engine/internal/store"
)

func TestSupersedeChain(t *testing.T) {
	root := copyHome(t)
	l1 := mustAdd(t, root, []string{"chain", "first", "generation"}, AddOptions{Title: "chain first"})
	l2 := mustAdd(t, root, []string{"chain", "second", "generation"}, AddOptions{Title: "chain second"})
	l3 := mustAdd(t, root, []string{"chain", "third", "generation"}, AddOptions{Title: "chain third"})

	if err := Supersede(root, l1.ID, l2.ID); err != nil {
		t.Fatalf("supersede l1 -> l2: %v", err)
	}
	if err := Supersede(root, l2.ID, l3.ID); err != nil {
		t.Fatalf("supersede l2 -> l3: %v", err)
	}

	got1 := recByID(t, root, l1.ID)
	got2 := recByID(t, root, l2.ID)
	got3 := recByID(t, root, l3.ID)
	if got1.Status != "superseded" || got2.Status != "superseded" {
		t.Fatalf("statuses = %q/%q, want superseded/superseded", got1.Status, got2.Status)
	}
	if got3.Status != "active" {
		t.Fatalf("newest status = %q, want active", got3.Status)
	}
	if s := strField(got2.Front, "supersedes"); s != l1.ID {
		t.Fatalf("l2 supersedes = %q, want %q", s, l1.ID)
	}
	if s := strField(got3.Front, "supersedes"); s != l2.ID {
		t.Fatalf("l3 supersedes = %q, want %q", s, l2.ID)
	}



	if err := Supersede(root, "lesson.deadbeef", l3.ID); err == nil ||
		!strings.Contains(err.Error(), "ไม่พบ old record") {
		t.Fatalf("missing old must fail loudly, got %v", err)
	}
	if err := Supersede(root, l1.ID, "lesson.deadbeef"); err == nil ||
		!strings.Contains(err.Error(), "ไม่พบ new record") {
		t.Fatalf("missing new must fail loudly, got %v", err)
	}
}

func TestCiteIncrementsUses(t *testing.T) {
	root := copyHome(t)
	id := "lesson.bbbb0001"

	full, err := Cite(root, id)
	if err != nil {
		t.Fatalf("cite: %v", err)
	}
	if full != "lesson.bbbb0001" {
		t.Fatalf("cite returned %q, want the full id", full)
	}
	rec := recByID(t, root, full)
	if rec.Uses != 1 {
		t.Fatalf("uses = %d, want 1", rec.Uses)
	}
	if got := strField(rec.Front, "last_used"); got != today() {
		t.Fatalf("last_used = %q, want today %q", got, today())
	}

	if _, err := Cite(root, full); err != nil {
		t.Fatalf("second cite: %v", err)
	}
	if rec := recByID(t, root, full); rec.Uses != 2 {
		t.Fatalf("uses after second cite = %d, want 2", rec.Uses)
	}

	// Ambiguous prefix and unknown id are loud failures.
	if _, err := Cite(root, "bbbb"); err == nil || !strings.Contains(err.Error(), "id ไม่ชัด") {
		t.Fatalf("ambiguous prefix must fail, got %v", err)
	}
	if _, err := Cite(root, "ffff0001"); err == nil || !strings.Contains(err.Error(), "ตรง 0") {
		t.Fatalf("unknown id must fail, got %v", err)
	}
}

func TestReviewOnlyQuarantineEscape(t *testing.T) {
	root := copyHome(t)
	gitInit(t, root)
	const qid = "lesson.bbbb0004"

	// Quarantine exclusion: the record never surfaces in selection while
	// quarantined, even for a query naming its content.
	recs := nil2Records(t, root)
	sel := Select(recs, "external content quarantine screened", "", []string{"lesson"}, 10)
	for _, r := range sel {
		if r.ID == qid {
			t.Fatal("quarantined record leaked into selection")
		}
	}

	// Review requires evidence.
	if err := Review(root, qid, nil); err == nil || !strings.Contains(err.Error(), "review ต้องมี --evidence") {
		t.Fatalf("review without evidence must fail, got %v", err)
	}
	// Non-quarantine records cannot be reviewed.
	if err := Review(root, "lesson.bbbb0001", []string{"reviewed: x"}); err == nil ||
		!strings.Contains(err.Error(), "ไม่ได้อยู่ใน quarantine") {
		t.Fatalf("review of non-quarantine must fail, got %v", err)
	}

	if err := Review(root, qid, []string{"reviewed: fixture-screen passed"}); err != nil {
		t.Fatalf("review: %v", err)
	}
	rec := recByID(t, root, qid)
	if rec.Sensitivity != "internal" {
		t.Fatalf("sensitivity = %q, want internal", rec.Sensitivity)
	}
	ev := evidenceStrings(rec.Front)
	if len(ev) != 1 || ev[0] != "reviewed: fixture-screen passed" {
		t.Fatalf("evidence = %v, want the screening note attached", ev)
	}

	// Control: after review the record is selectable.
	sel2 := Select(nil2Records(t, root), "external content quarantine screened", "", []string{"lesson"}, 10)
	found := false
	for _, r := range sel2 {
		if r.ID == qid {
			found = true
		}
	}
	if !found {
		t.Fatal("reviewed record must surface in selection")
	}

	// The review commit is durable: the unquarantine commit exists.
	out, err := execCommand(root, "log", "--oneline")
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if !strings.Contains(out, "unquarantine "+qid) {
		t.Fatalf("want unquarantine commit, log:\n%s", out)
	}
}

func TestReviewCommitFailureStillPersists(t *testing.T) {
	root := copyHome(t) // no git repo → commit fails after the rewrite
	const qid = "lesson.bbbb0004"
	err := Review(root, qid, []string{"reviewed: fixture-screen"})
	if err == nil || !strings.Contains(err.Error(), "commit failed") {
		t.Fatalf("non-repo review must report the commit failure, got %v", err)
	}
	// The unquarantine itself persisted — the failure is observable, not lost.
	rec := recByID(t, root, qid)
	if rec.Sensitivity != "internal" {
		t.Fatalf("sensitivity = %q, want internal despite commit failure", rec.Sensitivity)
	}
}

func TestTouchBumpsLastUsed(t *testing.T) {
	root := copyHome(t)
	rec := recByID(t, root, "lesson.bbbb0001")

	n, err := Touch(root, []store.Record{rec})
	if err != nil {
		t.Fatalf("touch: %v", err)
	}
	if n != 1 {
		t.Fatalf("touched %d, want 1", n)
	}
	if got := strField(recByID(t, root, rec.ID).Front, "last_used"); got != today() {
		t.Fatalf("last_used = %q, want %q", got, today())
	}
	// Touching an already-today record is a no-op.
	n, err = Touch(root, []store.Record{recByID(t, root, rec.ID)})
	if err != nil || n != 0 {
		t.Fatalf("second touch: n=%d err=%v, want 0/nil", n, err)
	}
}

func TestReindexAfterMutation(t *testing.T) {
	root := copyHome(t)
	res := mustAdd(t, root, []string{"index", "refresh", "after", "write"}, AddOptions{Title: "index refresh lesson"})

	// Add rebuilds the derived index: the new record is searchable and the
	// index count matches the tree.
	ix, err := openIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	ids, serr := ix.Search("index refresh", 10)
	if serr != nil {
		t.Fatal(serr)
	}
	found := false
	for _, id := range ids {
		if id == res.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("new record %s missing from derived index (ids=%v)", res.ID, ids)
	}
	n, cerr := ix.Count()
	if cerr != nil {
		t.Fatal(cerr)
	}
	recs, _ := store.ScanWithReport(root)
	if n != len(recs.Records) {
		t.Fatalf("index count = %d, want %d", n, len(recs.Records))
	}
	if _, err := os.Stat(filepath.Join(root, ".memory")); err != nil {
		t.Fatalf("index directory must exist: %v", err)
	}
}
