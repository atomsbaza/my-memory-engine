// P1 (KB-patterns) tests: content_sha256 idempotent ingest, body
// normalization, the verify tamper warning and the staleness report.
//
// All fixtures are synthetic (provenance: test-generated); no real corpus
// is used. Not parallel: shared process clock seam.
package domain

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atomsbaza/my-memory-engine/internal/codec"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// TestNormalizeBody pins the canonicalisation contract: NFC, LF endings,
// per-line trailing spaces, blank-run collapse (3+ -> 1) and overall trim —
//
//	and nothing else changes.
func TestNormalizeBody(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"nfc composes e+acute", "caf\u0065\u0301", "caf\u00e9"},
		{"precomposed untouched", "caf\u00e9", "caf\u00e9"},
		{"crlf to lf", "a\r\nb\r\n", "a\nb"},
		{"bare cr to lf", "a\rb\n", "a\nb"},
		{"trailing spaces per line", "a  \nb\t\n", "a\nb"},
		{"leading trim", "\n\n  hello\n", "hello"},
		{"inner single blank survives", "a\n\nb", "a\n\nb"},
		{"two blank lines survive", "a\n\n\nb", "a\n\n\nb"},
		{"three blank lines collapse to one", "a\n\n\n\nb", "a\n\nb"},
		{"nine blank lines collapse to one", "a\n" + strings.Repeat("\n", 8) + "b", "a\n\nb"},
		{"blank line of spaces counts blank", "a\n   \n   \n   \nb", "a\n\nb"},
		{"trailing blank lines trimmed", "a\n\n\n", "a"},
	}
	for _, c := range cases {
		if got := NormalizeBody(c.in); got != c.want {
			t.Errorf("%s: NormalizeBody(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestContentSHA256WhitespaceInvariance: only presentation differences may
// hash equal.
func TestContentSHA256WhitespaceInvariance(t *testing.T) {
	base := "**Rule.** Verify on disk.\n**NOT.** Never trust memory."
	variants := []string{
		base,
		"**Rule.** Verify on disk.  \n**NOT.** Never trust memory.", // trailing spaces
		"**Rule.** Verify on disk.\r\n**NOT.** Never trust memory.", // CRLF
		"  \n" + base + "\n\n\n",                                    // edge whitespace
		"**Rule.** Verify on disk.\n**NOT.** Never trust memory.",   // exact copy
	}
	first := ContentSHA256(variants[0])
	for i, v := range variants {
		if got := ContentSHA256(v); got != first {
			t.Errorf("variant %d hashes differently (%s != %s)", i, got[:12], first[:12])
		}
	}
	if ContentSHA256(strings.Replace(base, "disk.\n", "disk.\n\n\n\n", 1)) == first {
		t.Error("a collapsed blank run must change the hash (3+ blank lines -> one)")
	}
	if ContentSHA256(base+"\nextra line") == first {
		t.Error("a real content change hashed equal")
	}
	if ContentSHA256(base+"\nnew word") == first {
		t.Error("a word change hashed equal")
	}
}

// TestValidContentSHA: 64 lowercase hex, nothing else.
func TestValidContentSHA(t *testing.T) {
	for _, ok := range []string{
		strings.Repeat("a", 64),
		"0123456789abcdef" + strings.Repeat("0", 48),
	} {
		if !ValidContentSHA(ok) {
			t.Errorf("ValidContentSHA(%q...) = false, want true", ok[:12])
		}
	}
	for _, bad := range []string{
		"", strings.Repeat("A", 64), strings.Repeat("g", 64),
		strings.Repeat("a", 63), strings.Repeat("a", 65),
	} {
		if ValidContentSHA(bad) {
			t.Errorf("ValidContentSHA(len %d) = true, want false", len(bad))
		}
	}
}

// addBody writes a marker-bodied record through domain.Add.
func addRecord(t *testing.T, root, layer, scope, title, body string) (AddResult, error) {
	t.Helper()
	return Add(root, store.Record{Layer: layer, Scope: scope, Body: body}, AddOptions{
		Title: title, Tags: []string{"p1-test"}, Source: "agent:test",
	})
}

// TestAddIdempotentIdenticalBody: re-adding identical content in the same
// layer+scope is a success (err == nil, Idempotent == true), writes no file
// and the message names the existing record.
func TestAddIdempotentIdenticalBody(t *testing.T) {
	home := copyHome(t)
	gitInit(t, home)
	body := "**Rule.** Idempotent ingest test body.\n**NOT.** Re-add twice" + "\n" + provenanceMarker
	res1, err := addRecord(t, home, "lesson", "global", "idempotent ingest test", body)
	if err != nil || res1.Idempotent {
		t.Fatalf("first add: err=%v idempotent=%v", err, res1.Idempotent)
	}
	if !strings.Contains(res1.Path, home) {
		t.Fatalf("first add wrote outside home: %s", res1.Path)
	}
	before := countRecordFiles(t, home)

	res2, err := addRecord(t, home, "lesson", "global", "any title", body)
	if err != nil {
		t.Fatalf("re-add identical content refused: %v", err)
	}
	if !res2.Idempotent {
		t.Fatalf("re-add not idempotent: %+v", res2)
	}
	if res2.ID != res1.ID || res2.Rel != res1.Rel {
		t.Errorf("idempotent result names the wrong twin: %s (%s), want %s (%s)",
			res2.ID, res2.Rel, res1.ID, res1.Rel)
	}
	if res2.Message == "" || !strings.Contains(res2.Message, "content ซ้ำเป๊ะ (sha256 ") ||
		!strings.Contains(res2.Message, res1.ID) || !strings.Contains(res2.Message, res1.Rel) {
		t.Errorf("message = %q, want sha + existing id + file", res2.Message)
	}
	if got := countRecordFiles(t, home); got != before {
		t.Errorf("idempotent re-add changed the file count: %d -> %d", before, got)
	}
}

// TestAddIdempotentWhitespaceOnlyDifferences: normalization makes CRLF /
// trailing-space / over-blank variants of the same body collide.
func TestAddIdempotentWhitespaceOnlyDifferences(t *testing.T) {
	home := copyHome(t)
	gitInit(t, home)
	body := "**Rule.** Normalization twin test.\n**NOT.** Ignore whitespace" + "\n" + provenanceMarker
	if _, err := addRecord(t, home, "lesson", "global", "normalization twin test", body); err != nil {
		t.Fatalf("first add: %v", err)
	}
	before := countRecordFiles(t, home)
	variants := []string{
		strings.ReplaceAll(body, "\n", "\r\n"),
		strings.ReplaceAll(body, ".\n", ".  \n"),
		"\n\n\n" + body + "\n\n\n\n", // edge blanks trim away
	}
	for i, v := range variants {
		res, err := addRecord(t, home, "lesson", "global", fmt.Sprintf("variant %d", i), v)
		if err != nil {
			t.Fatalf("variant %d refused: %v", i, err)
		}
		if !res.Idempotent {
			t.Errorf("variant %d was written instead of dedup-ed: %+v", i, res)
		}
	}
	if got := countRecordFiles(t, home); got != before {
		t.Errorf("whitespace variants changed the file count: %d -> %d", before, got)
	}
}

// TestAddDifferentContentWrites: a real body change must still write a new
// record (and carry a well-formed content_sha256).
func TestAddDifferentContentWrites(t *testing.T) {
	home := copyHome(t)
	gitInit(t, home)
	body := "**Rule.** Body change twin test.\n**NOT.** Differ on purpose" + "\n" + provenanceMarker
	res1, err := addRecord(t, home, "lesson", "global", "body change twin test", body)
	if err != nil {
		t.Fatalf("first add: %v", err)
	}
	res2, err := addRecord(t, home, "lesson", "global", "body change twin test",
		body+"\nอีก fact หนึ่งที่ต่างกันโดยสิ้นเชิง: xcode simulator ci pipeline regression numbers differ")
	if err != nil {
		t.Fatalf("changed body refused: %v", err)
	}
	if res2.Idempotent {
		t.Fatalf("changed body wrongly dedup-ed onto %s", res2.ID)
	}
	if res2.ID == res1.ID {
		t.Error("changed body landed on the same id")
	}
	data, rerr := os.ReadFile(res2.Path)
	if rerr != nil {
		t.Fatalf("read written record: %v", rerr)
	}
	fm, _, perr := codec.ParseFront(string(data))
	if perr != nil {
		t.Fatalf("parse written record: %v", perr)
	}
	stored, _ := fm["content_sha256"].(string)
	if !ValidContentSHA(stored) {
		t.Errorf("written record has no well-formed content_sha256: %q", stored)
	}
	if stored != ContentSHA256(strings.TrimSpace(body+"\nอีก fact หนึ่งที่ต่างกันโดยสิ้นเชิง: xcode simulator ci pipeline regression numbers differ")+"\n") {
		// trimmed body + trailing newline is what Add stores
		t.Errorf("stored content_sha256 does not match ContentSHA256(body)")
	}
}

// TestAddSameContentDifferentScope: cross-scope duplication is legitimate,
// so it writes and only warns.
func TestAddSameContentDifferentScope(t *testing.T) {
	home := copyHome(t)
	gitInit(t, home)
	body := "**Rule.** Cross scope twin test.\n**NOT.** Duplicating on purpose" + "\n" + provenanceMarker
	res1, err := addRecord(t, home, "lesson", "global", "cross scope twin test", body)
	if err != nil {
		t.Fatalf("first add: %v", err)
	}
	res2, err := addRecord(t, home, "lesson", "project:Alpha", "cross scope twin test", body)
	if err != nil {
		t.Fatalf("cross-scope add refused: %v", err)
	}
	if res2.Idempotent {
		t.Fatal("cross-scope add was silently dedup-ed")
	}
	warned := false
	for _, w := range res2.Warnings {
		if strings.Contains(w, res1.ID) && strings.Contains(w, "content_sha256") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("cross-scope duplication warnings = %v, want one naming %s", res2.Warnings, res1.ID)
	}
}

// TestVerifyContentSHATamperWarning: a stale/mismatched content_sha256 warns;
// a fresh record (hash matches) does not.
func TestVerifyContentSHATamperWarning(t *testing.T) {
	home := copyHome(t)
	gitInit(t, home)
	body := "**Rule.** Verify tamper test.\n**NOT.** Edit after write" + "\n" + provenanceMarker
	res, err := addRecord(t, home, "lesson", "global", "verify tamper test", body)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	_, warnings, err := Verify(home)
	if err != nil {
		t.Fatalf("verify after clean add: %v", err)
	}
	if mentionsSHAMismatch(warnings) {
		t.Fatalf("fresh record warned about content_sha256: %v", warnings)
	}

	data, rerr := os.ReadFile(res.Path)
	if rerr != nil {
		t.Fatalf("read record: %v", rerr)
	}
	if werr := os.WriteFile(res.Path, []byte(strings.Replace(string(data), "Edit after write", "Edited after write", 1)), 0o644); werr != nil {
		t.Fatalf("tamper: %v", werr)
	}
	_, warnings, err = Verify(home)
	if err != nil {
		t.Fatalf("verify after tamper: %v", err)
	}
	if !mentionsSHAMismatch(warnings) {
		t.Errorf("tampered body produced no content_sha256 warning: %v", warnings)
	}
}

func mentionsSHAMismatch(warnings []string) bool {
	for _, w := range warnings {
		if strings.Contains(w, "content_sha256 ไม่ตรง") {
			return true
		}
	}
	return false
}

func countRecordFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".md") && !strings.Contains(filepath.ToSlash(p), "/_archive/") {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return n
}

// ---------------------------------------------------------------------------
// review --stale (StaleReport)
// ---------------------------------------------------------------------------

// staleHome is a bare home (default schema) where the clock is pinned: one
// rule unused 120 days, one lesson 30 days, one lesson with no last_used
// (falls back to a 100-day-old created), one archived 200-day record, one
// fresh record, plus two 30-day records with different layers/ids to pin the
// tie-break (taxonomy layer order, then id).
func staleHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	pin(t, 0) // today at the process-clock seam

	write := func(rel, layer, id, title, created, lastUsed, status string) {
		t.Helper()
		fm := codec.Frontmatter{
			"id": id, "title": title, "layer": layer, "scope": "global", "tags": []string{"p1-test"},
			"confidence": 0.9, "decay": 1.0, "source": "agent:test",
			"created": created, "expires": "none", "status": status, "sensitivity": "internal",
		}
		if lastUsed != "" {
			fm["last_used"] = lastUsed
		}
		content := codec.DumpFront(fm) + "\n" + title + "\n" + provenanceMarker + "\n"
		path := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("rules/old-rule.aaaa0011.md", "rule", "rule.aaaa0011", "old unused rule",
		"2026-01-01", pinDate(t, -120), "active")
	write("lessons/new-lesson.bbbb0011.md", "lesson", "lesson.bbbb0011", "recent lesson",
		"2026-01-01", pinDate(t, -30), "active")
	write("lessons/nolast-lesson.bbbb0012.md", "lesson", "lesson.bbbb0012", "no last_used falls back",
		pinDate(t, -100), "", "active")
	write("lessons/archived-lesson.bbbb0013.md", "lesson", "lesson.bbbb0013", "archived so invisible",
		"2026-01-01", pinDate(t, -200), "archived")
	write("lessons/fresh-lesson.bbbb0014.md", "lesson", "lesson.bbbb0014", "used today",
		"2026-01-01", pinDate(t, 0), "active")
	write("decisions/tie-decision.e0e00011.md", "decision", "decision.e0e00011", "tie age decision",
		"2026-01-01", pinDate(t, -30), "active")
	write("lessons/tie-lesson.bbbb0015.md", "lesson", "lesson.bbbb0015", "tie age lesson later id",
		"2026-01-01", pinDate(t, -30), "active")
	return home
}

func pin(t *testing.T, days int) {
	t.Helper()
	prev := timeNow
	t.Cleanup(func() { timeNow = prev })
	base := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	timeNow = func() time.Time { return base.AddDate(0, 0, days) }
}

func pinDate(t *testing.T, days int) string {
	t.Helper()
	return timeNow().AddDate(0, 0, days).Format("2006-01-02")
}

func TestStaleReport(t *testing.T) {
	for _, c := range []struct {
		name       string
		days       int
		wantIDs    []string
		wantAbsent []string
	}{
		{"default 90 picks every active stalno", 90, []string{"rule.aaaa0011", "lesson.bbbb0012"}, nil},
		{"90 excludes the 30-day", 90, nil, []string{"lesson.bbbb0011", "lesson.bbbb0014"}},
		{"archived never listed", 90, nil, []string{"lesson.bbbb0013"}},
		{"days 20 includes the 30-day records", 20, []string{"lesson.bbbb0011", "rule.aaaa0011", "lesson.bbbb0012", "lesson.bbbb0015", "decision.e0e00011"},
			[]string{"lesson.bbbb0014"}},
		{"days 0 lists every active record", 0,
			[]string{"rule.aaaa0011", "lesson.bbbb0011", "lesson.bbbb0012", "lesson.bbbb0015", "decision.e0e00011", "lesson.bbbb0014"},
			[]string{"lesson.bbbb0013"}},
	} {
		home := staleHome(t)
		entries, err := StaleReport(home, c.days)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		ids := map[string]bool{}
		for _, e := range entries {
			ids[e.ID] = true
		}
		for _, want := range c.wantIDs {
			if !ids[want] {
				t.Errorf("%s: %s missing from report: %v", c.name, want, ids)
			}
		}
		for _, nope := range c.wantAbsent {
			if ids[nope] {
				t.Errorf("%s: %s must not be listed", c.name, nope)
			}
		}
	}
}

// TestStaleReportOrdering: days desc, ties by taxonomy layer order (rule
// before lesson) then id.
func TestStaleReportOrdering(t *testing.T) {
	home := staleHome(t)
	entries, err := StaleReport(home, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 6 {
		t.Fatalf("expected the 6 stale-fixture records, got %d: %v", len(entries), ids(entries))
	}
	// days desc; the 30-day tie breaks lesson before decision (taxonomy
	// layer order), then id ascending inside the layer.
	want := []string{
		"rule.aaaa0011", "lesson.bbbb0012", "lesson.bbbb0011",
		"lesson.bbbb0015", "decision.e0e00011", "lesson.bbbb0014",
	}
	for i, id := range want {
		if entries[i].ID != id {
			t.Fatalf("order[%d] = %s, want %s (full: %v)", i, entries[i].ID, id, ids(entries))
		}
	}
	if entries[0].Days != 120 || entries[1].Days != 100 || entries[2].Days != 30 || entries[5].Days != 0 {
		t.Errorf("day counts wrong: %v", ids(entries))
	}
	// Retention fields are part of the report contract.
	for _, e := range entries {
		if e.Retention <= 0 || e.Decay != 1.0 || e.File == "" || e.Title == "" {
			t.Errorf("entry %s underfilled: %+v", e.ID, e)
		}
	}
}

func ids(entries []StaleEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, fmt.Sprintf("%s(%d)", e.ID, e.Days))
	}
	return out
}
