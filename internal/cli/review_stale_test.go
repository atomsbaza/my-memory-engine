// Behaviour tests for the --stale side of `mem review` (KB P1): the
// read-only staleness report. The quarantine exit keeps its own contract —
// --stale must not touch it (regression asserted here too).
package cli

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// freshRecord adds a record used today through the real add path, so the
// stale report has a live negative control.
func freshRecord(t *testing.T, home string) string {
	t.Helper()
	body := "fresh record for the staleness test\n" +
		"used today so it must never appear in a 90-day report\n" +
		"(synthetic fixture — provenance: test-generated, not corpus-derived)"
	code, out, stderr := runHome(t, home, body, "add", "--layer", "lesson", "--scope", "global",
		"--title", "fresh for stale test", "--tag", "test", "--file", "-")
	mustCode(t, ExitOK, code, out, stderr, "add fresh record")
	return strings.Split(out, "\t")[0]
}

var staleDaysRe = regexp.MustCompile(`days=(\d+)`)

func TestReviewStaleReport(t *testing.T) {
	home := fixtureHome(t)
	fresh := freshRecord(t, home)

	// Default threshold: every fixture active record is old, the fresh one
	// is not. Exit 0 even with a non-empty report.
	code, out, stderr := runHome(t, home, "", "review", "--stale")
	mustCode(t, ExitOK, code, out, stderr, "review --stale")
	if !strings.Contains(out, "stale: 10 record(s) ไม่ถูกใช้ ≥ 90 วัน") {
		t.Errorf("header wrong (want count 10 + threshold 90):\n%s", out)
	}
	if strings.Contains(out, fresh) {
		t.Errorf("fresh record %s listed as stale:\n%s", fresh, out)
	}
	rows := 0
	prevDays := -1
	for _, line := range strings.Split(out, "\n") {
		m := staleDaysRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		rows++
	}
	if rows != 10 {
		t.Errorf("report rows = %d, want 10:\n%s", rows, out)
	}
	if !strings.Contains(out, "days=") {
		t.Error("no day counts in the report")
	}

	// days are non-increasing top to bottom (sorted report).
	for _, line := range strings.Split(out, "\n") {
		if m := staleDaysRe.FindStringSubmatch(line); m != nil {
			d := atoi(t, m[1])
			if prevDays >= 0 && d > prevDays {
				t.Errorf("days not sorted desc: %d after %d", d, prevDays)
			}
			prevDays = d
		}
	}

	// --days 0 lists every active record, fresh included (age ≥ 0).
	code, out, stderr = runHome(t, home, "", "review", "--stale", "--days", "0")
	mustCode(t, ExitOK, code, out, stderr, "review --stale --days 0")
	if !strings.Contains(out, "stale: 11 record(s) ไม่ถูกใช้ ≥ 0 วัน") {
		t.Errorf("days-0 header wrong:\n%s", out)
	}
	if !strings.Contains(out, fresh) {
		t.Errorf("days-0 must list every active record incl. %s:\n%s", fresh, out)
	}

	// --limit trims the rows, not the header total.
	code, out, stderr = runHome(t, home, "", "review", "--stale", "--limit", "2")
	mustCode(t, ExitOK, code, out, stderr, "review --stale --limit 2")
	if !strings.Contains(out, "stale: 10 record(s)") {
		t.Errorf("limit header should keep the total:\n%s", out)
	}
	rows = 0
	for _, line := range strings.Split(out, "\n") {
		if staleDaysRe.MatchString(line) {
			rows++
		}
	}
	if rows != 2 {
		t.Errorf("limit 2 produced %d rows:\n%s", rows, out)
	}
}

func TestReviewStaleJSON(t *testing.T) {
	home := fixtureHome(t)
	fresh := freshRecord(t, home)

	code, out, stderr := runHome(t, home, "", "review", "--stale", "--json")
	mustCode(t, ExitOK, code, out, stderr, "review --stale --json")

	var entries []struct {
		ID        string  `json:"id"`
		Layer     string  `json:"layer"`
		Scope     string  `json:"scope"`
		Status    string  `json:"status"`
		Days      int     `json:"days"`
		Decay     float64 `json:"decay"`
		Retention float64 `json:"retention"`
		LastUsed  string  `json:"last_used"`
		Title     string  `json:"title"`
		File      string  `json:"file"`
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("not a JSON array of entries: %v\n%s", err, out)
	}
	if len(entries) != 10 {
		t.Fatalf("entries = %d, want 10 (fresh record must stay out)", len(entries))
	}
	for _, e := range entries {
		if e.ID == "" || e.Layer == "" || e.Scope == "" || e.Status != "active" ||
			e.Days < 90 || e.Retention <= 0 || e.LastUsed == "" || e.Title == "" || e.File == "" {
			t.Errorf("entry underfilled or not active: %+v", e)
		}
		if e.ID == fresh {
			t.Errorf("fresh record leaked into --json: %+v", e)
		}
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].Days > entries[i-1].Days {
			t.Errorf("entries not sorted days-desc: %d before %d", entries[i-1].Days, entries[i].Days)
		}
	}
}

func TestReviewStaleUsageErrors(t *testing.T) {
	home := fixtureHome(t)
	for _, argv := range [][]string{
		{"review", "--stale", "--days", "-1"},
		{"review", "--stale", "--limit", "-1"},
		{"review", "--stale", "lesson.bbbb0001"},
	} {
		code, _, stderr := runHome(t, home, "", argv...)
		if code != ExitGuard {
			t.Errorf("review %v = exit %d, want %d (stderr: %s)",
				argv[1:], code, ExitGuard, stderr)
		}
		if !strings.Contains(stderr, "mem: ") {
			t.Errorf("usage error must print to stderr in die() shape: %q", stderr)
		}
	}
	// An empty report is still exit 0.
	code, out, stderr := runHome(t, home, "", "review", "--stale", "--days", "100000")
	mustCode(t, ExitOK, code, out, stderr, "review --stale --days 100000")
	if !strings.Contains(out, "stale: 0 record(s)") || !strings.Contains(out, "(none)") {
		t.Errorf("empty report wrong:\n%s", out)
	}
}

// TestReviewQuarantineUnchangedByStale: the review quarantine exit keeps
// requiring <id> + --evidence and keeps writing; --stale is read-only.
func TestReviewQuarantineUnchangedByStale(t *testing.T) {
	home := fixtureHome(t)

	// No evidence -> usage error (never silently mutating).
	code, _, stderr := runHome(t, home, "", "review", "lesson.bbbb0004")
	if code != ExitGuard {
		t.Errorf("review without evidence = exit %d, want %d", code, ExitGuard)
	}
	if !strings.Contains(stderr, "--evidence") {
		t.Errorf("error must demand --evidence: %q", stderr)
	}

	// The real quarantine release still works.
	code, out, stderr := runHome(t, home, "", "review", "lesson.bbbb0004", "--evidence", "reviewed: อ่านแล้ว ปลอดภัย")
	mustCode(t, ExitOK, code, out, stderr, "review --evidence")
	if !strings.Contains(out, "lesson.bbbb0004\treviewed") {
		t.Errorf("review output shape changed:\n%s", out)
	}

	// --stale never mutates: the record shown is byte-identical before and
	// after the report.
	beforeCode, before, _ := runHome(t, home, "", "show", "lesson.bbbb0004")
	if beforeCode != ExitOK {
		t.Fatalf("show after review: %s", before)
	}
	code, outStale, stderr := runHome(t, home, "", "review", "--stale")
	mustCode(t, ExitOK, code, outStale, stderr, "review --stale after quarantine")
	afterCode, after, _ := runHome(t, home, "", "show", "lesson.bbbb0004")
	if afterCode != ExitOK {
		t.Fatalf("show after stale: %s", after)
	}
	if before != after {
		t.Errorf("--stale mutated the record:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("bad number %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n
}
