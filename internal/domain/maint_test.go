// P2 maintenance-operation tests: the archival scheduler (retention threshold,
// move target), lesson promotion (strict vs force, guards, id collisions) and
// handoff compaction (age/group filters, the folded knowledge record).
//
// Synthetic fixtures only; the clock is pinned so retention and age arithmetic
// do not drift with the wall clock.
package domain

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/atomsbaza/my-memory-engine/internal/codec"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

func TestArchiveMovesLowRetentionRecords(t *testing.T) {
	root := copyHome(t)
	pinClock(t, "2026-11-08T12:00:00")

	recs, err := store.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	var active, decaying []string
	for _, r := range recs {
		if r.Status != "active" {
			continue
		}
		active = append(active, r.ID)
		if r.Layer != "rule" {
			decaying = append(decaying, r.ID)
		}
	}
	sort.Strings(decaying)

	dry, err := Archive(root, ArchiveOptions{Threshold: 0.15, DryRun: true})
	if err != nil {
		t.Fatalf("archive dry run: %v", err)
	}
	if dry.Active != len(active) {
		t.Fatalf("active = %d, want %d", dry.Active, len(active))
	}
	var got []string
	for _, c := range dry.Candidates {
		got = append(got, c.ID)
		if c.Layer == "rule" {
			t.Errorf("rule %s must not be an archive candidate (rules have an infinite half-life)", c.ID)
		}
		if c.Score >= 0.15 {
			t.Errorf("%s: score %.3f is not below the threshold", c.ID, c.Score)
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(decaying, ",") {
		t.Fatalf("candidates = %v, want every active non-rule record %v", got, decaying)
	}
	// Ascending (score, id) — the baseline's candidate order.
	for i := 1; i < len(dry.Candidates); i++ {
		a, b := dry.Candidates[i-1], dry.Candidates[i]
		if a.Score > b.Score || (a.Score == b.Score && a.ID > b.ID) {
			t.Fatalf("candidates out of order at %d: %+v then %+v", i, a, b)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "_archive")); !os.IsNotExist(err) {
		t.Fatalf("dry run created _archive/ (err=%v)", err)
	}

	res, err := Archive(root, ArchiveOptions{Threshold: 0.15})
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if len(res.Moved) != len(dry.Candidates) {
		t.Fatalf("moved %d, want %d", len(res.Moved), len(dry.Candidates))
	}
	for _, m := range res.Moved {
		if r, ok := findRec(t, root, m.ID); ok {
			t.Fatalf("%s still visible to the scanner at %s", m.ID, r.Rel)
		}
		data, rerr := os.ReadFile(filepath.Join(root, filepath.FromSlash(m.Dest)))
		if rerr != nil {
			t.Fatalf("%s not on disk at %s: %v", m.ID, m.Dest, rerr)
		}
		for _, want := range []string{"status: archived", "archived_at: 2026-11-08"} {
			if !strings.Contains(string(data), want) {
				t.Errorf("%s archived file missing %q:\n%s", m.ID, want, data)
			}
		}
		if !strings.HasPrefix(m.Dest, "_archive/"+layerOf(m.ID)+"/") {
			t.Errorf("%s landed at %s, want _archive/<layer>/", m.ID, m.Dest)
		}
	}
}

func TestPromoteStrictVersusForce(t *testing.T) {
	root := copyHome(t)
	pinClock(t, "2026-11-08T12:00:00")

	writeRecFile(t, root, "lesson", "lesson.cafe0001", "proven lesson",
		"**Rule.** keep the disk as the source of truth.", func(fm codec.Frontmatter) {
			fm["uses"] = 3
			fm["successes"] = 3
		})
	writeRecFile(t, root, "lesson", "lesson.cafe0002", "unproven lesson",
		"**Rule.** this one has not earned promotion yet.", func(fm codec.Frontmatter) {
			fm["uses"] = 3
			fm["successes"] = 1
		})

	strict, err := Promote(root, PromoteOptions{DryRun: true})
	if err != nil {
		t.Fatalf("promote dry run: %v", err)
	}
	if len(strict.Candidates) != 1 || strict.Candidates[0].ID != "lesson.cafe0001" {
		t.Fatalf("strict candidates = %+v, want only lesson.cafe0001", strict.Candidates)
	}
	if strict.Mode != "strict (successes==uses>0)" {
		t.Errorf("mode = %q", strict.Mode)
	}
	if len(strict.Promoted) != 0 {
		t.Fatalf("dry run promoted %d records", len(strict.Promoted))
	}

	force, err := Promote(root, PromoteOptions{Force: true, DryRun: true})
	if err != nil {
		t.Fatalf("promote force dry run: %v", err)
	}
	if len(force.Candidates) != 2 {
		t.Fatalf("force candidates = %d, want 2 (uses only)", len(force.Candidates))
	}

	res, err := Promote(root, PromoteOptions{})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if len(res.Promoted) != 1 {
		t.Fatalf("promoted %d lessons, want 1", len(res.Promoted))
	}
	move := res.Promoted[0]
	if move.From != "lesson.cafe0001" {
		t.Fatalf("promoted %s, want lesson.cafe0001", move.From)
	}
	rule, ok := findRec(t, root, move.To)
	if !ok {
		t.Fatalf("rule %s is not on disk (%s)", move.To, move.Rel)
	}
	if rule.Layer != "rule" || strField(rule.Front, "promoted_from") != move.From {
		t.Fatalf("rule front-matter = %v", rule.Front)
	}
	if !strings.HasPrefix(move.To, "rule.") || !strings.HasPrefix(move.Rel, "rules/") {
		t.Fatalf("promotion landed at %s (%s)", move.To, move.Rel)
	}
	old, ok := findRec(t, root, move.From)
	if !ok {
		t.Fatalf("lesson %s vanished", move.From)
	}
	if old.Status != "promoted" || strField(old.Front, "promoted_to") != move.To {
		t.Fatalf("lesson status = %q promoted_to = %q", old.Status, strField(old.Front, "promoted_to"))
	}
	if other, ok := findRec(t, root, "lesson.cafe0002"); !ok || other.Status != "active" {
		t.Fatalf("unproven lesson must stay active (ok=%v status=%q)", ok, other.Status)
	}
}

func TestPromoteRefusesBeforeWriting(t *testing.T) {
	t.Run("guard", func(t *testing.T) {
		root := copyHome(t)
		pinClock(t, "2026-11-08T12:00:00")
		writeRecFile(t, root, "lesson", "lesson.cafe0003", "leaky lesson",
			"**Rule.** wire it up.\n**Evidence.** password: abcdefghijklmnop1234", func(fm codec.Frontmatter) {
				fm["uses"] = 3
				fm["successes"] = 3
			})
		before := ruleCount(t, root)
		_, err := Promote(root, PromoteOptions{})
		wantDomainErr(t, err, 2, "secret")
		if got := ruleCount(t, root); got != before {
			t.Fatalf("rules on disk = %d, want %d — the refusal must precede the write", got, before)
		}
		if r, ok := findRec(t, root, "lesson.cafe0003"); !ok || r.Status != "active" {
			t.Fatalf("lesson must stay active after a refused run")
		}
	})

	t.Run("duplicate planned id", func(t *testing.T) {
		root := copyHome(t)
		pinClock(t, "2026-11-08T12:00:00")
		// Two lessons with identical title and body but different ids: the
		// derived rule id is the same for both, which the baseline cannot see
		// (it only checks ids already on disk). writeRecFile names the file
		// after the title, so the copy is written by hand.
		rel := writeRecFile(t, root, "lesson", "lesson.cafe0004", "same fact",
			"**Rule.** one fact, one record.", func(fm codec.Frontmatter) {
				fm["uses"] = 3
				fm["successes"] = 3
			})
		data, rerr := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if rerr != nil {
			t.Fatalf("read %s: %v", rel, rerr)
		}
		dup := strings.Replace(string(data), "id: lesson.cafe0004", "id: lesson.cafe0005", 1)
		if dup == string(data) {
			t.Fatalf("fixture rewrite did not change the id:\n%s", data)
		}
		if werr := os.WriteFile(filepath.Join(root, "lessons", "same-fact-copy.md"), []byte(dup), 0o644); werr != nil {
			t.Fatalf("write duplicate lesson: %v", werr)
		}
		before := ruleCount(t, root)
		_, err := Promote(root, PromoteOptions{})
		wantDomainErr(t, err, 3, "ซ้ำกับ")
		if got := ruleCount(t, root); got != before {
			t.Fatalf("rules on disk = %d, want %d — nothing may be written", got, before)
		}
	})
}

func TestCompactFoldsOldHandoffsPerScope(t *testing.T) {
	root := copyHome(t)
	pinClock(t, "2026-11-08T12:00:00")

	// The fixture already carries two old handoffs in project:Alpha; two more
	// push that scope to four while project:Beta stays below the group minimum.
	for i, id := range []string{"handoff.cafe0001", "handoff.cafe0002"} {
		writeRecFile(t, root, "handoff", id, "old handoff "+id[len(id)-4:], "**State.** shipped round "+string(rune('1'+i)),
			func(fm codec.Frontmatter) {
				fm["scope"] = "project:Alpha"
				fm["expires"] = "2999-01-01"
			})
	}
	writeRecFile(t, root, "handoff", "handoff.cafe0003", "beta handoff", "**State.** beta work",
		func(fm codec.Frontmatter) {
			fm["scope"] = "project:Beta"
			fm["expires"] = "2999-01-01"
		})

	// Too high a group minimum selects nothing.
	none, err := Compact(root, CompactOptions{MinGroup: 9, DryRun: true})
	if err != nil {
		t.Fatalf("compact dry run: %v", err)
	}
	if len(none.Groups) != 0 {
		t.Fatalf("groups with min-group 9 = %+v, want none", none.Groups)
	}

	dry, err := Compact(root, CompactOptions{DryRun: true})
	if err != nil {
		t.Fatalf("compact dry run: %v", err)
	}
	if len(dry.Groups) != 1 || dry.Groups[0].Scope != "project:Alpha" {
		t.Fatalf("groups = %+v, want only project:Alpha", dry.Groups)
	}
	if len(dry.Groups[0].IDs) != 4 {
		t.Fatalf("project:Alpha ids = %v, want the 4 old handoffs", dry.Groups[0].IDs)
	}
	for i := 1; i < len(dry.Groups[0].IDs); i++ {
		if dry.Groups[0].IDs[i-1] > dry.Groups[0].IDs[i] {
			t.Fatalf("ids not sorted: %v", dry.Groups[0].IDs)
		}
	}

	res, err := Compact(root, CompactOptions{})
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if len(res.Written) != 1 {
		t.Fatalf("written %d records, want 1", len(res.Written))
	}
	w := res.Written[0]
	kn, ok := findRec(t, root, w.ID)
	if !ok {
		t.Fatalf("knowledge %s is not on disk (%s)", w.ID, w.Rel)
	}
	if kn.Layer != "knowledge" || kn.Scope != "project:Alpha" {
		t.Fatalf("knowledge front-matter = %v", kn.Front)
	}
	if !strings.HasPrefix(w.Rel, "knowledge/") {
		t.Fatalf("knowledge landed at %s", w.Rel)
	}
	for _, id := range w.IDs {
		if !strings.Contains(kn.Body, "[id: "+id+"]") {
			t.Errorf("knowledge body lost %s:\n%s", id, kn.Body)
		}
		if h, ok := findRec(t, root, id); !ok || h.Status != "compacted" ||
			strField(h.Front, "compacted_into") != w.ID {
			t.Errorf("handoff %s not marked compacted (ok=%v, status=%q)", id, ok, h.Status)
		}
	}
	if b, ok := findRec(t, root, "handoff.cafe0003"); !ok || b.Status != "active" {
		t.Errorf("project:Beta handoff must stay active")
	}
}

// findRec returns the scannable record with id, and whether it is visible.
func findRec(t *testing.T, root, id string) (store.Record, bool) {
	t.Helper()
	recs, err := store.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.ID == id {
			return r, true
		}
	}
	return store.Record{}, false
}

// ruleCount counts the rule records currently visible to the scanner.
func ruleCount(t *testing.T, root string) int {
	t.Helper()
	recs, err := store.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range recs {
		if r.Layer == "rule" {
			n++
		}
	}
	return n
}

// layerOf reads the layer prefix off a record id ("lesson.abc" -> "lesson").
func layerOf(id string) string {
	if i := strings.IndexByte(id, '.'); i > 0 {
		return id[:i]
	}
	return id
}
