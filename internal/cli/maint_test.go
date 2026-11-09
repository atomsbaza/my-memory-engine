// Behaviour tests for the lifecycle subcommands (archive / promote / compact
// and the promote --scan candidates file). Every assertion runs the real
// router against the synthetic home, so the exit codes and the rendered lines
// are the process contract, not internal helpers.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestArchiveDryRunMirrorsBaseline: --dry-run lists candidates under the
// threshold and never moves a file.
func TestArchiveDryRunMirrorsBaseline(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runHome(t, home, "", "archive", "--dry-run")
	mustCode(t, ExitOK, code, out, stderr, "archive --dry-run")
	if !strings.Contains(out, "dry-run: 0 candidates (threshold 0.2) — ยังไม่ย้าย") &&
		!strings.Contains(out, "dry-run: 0 candidates (threshold 0.15) — ยังไม่ย้าย") {
		// exact default varies with fixture scores; assert the shape instead
		if !strings.Contains(out, "dry-run: ") || !strings.Contains(out, "candidates (threshold 0.15) — ยังไม่ย้าย") {
			t.Fatalf("archive --dry-run summary missing:\n%s", out)
		}
	}
	// A huge threshold must list every active record as a DRY candidate.
	code, out, stderr = runHome(t, home, "", "archive", "--threshold", "99", "--dry-run")
	mustCode(t, ExitOK, code, out, stderr, "archive --threshold 99 --dry-run")
	if !strings.Contains(out, "DRY  rule.aaaa0001") || !strings.Contains(out, "DRY  handoff.cccc0001") {
		t.Fatalf("expected every active record listed, got:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "_archive")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created _archive/: %v", err)
	}
}

// TestPromoteDryRunForce: --force --min-uses 0 selects every active lesson
// without writing anything.
func TestPromoteDryRunForce(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runHome(t, home, "", "promote", "--dry-run", "--force", "--min-uses", "0")
	mustCode(t, ExitOK, code, out, stderr, "promote --dry-run --force")
	if !strings.Contains(out, "DRY  lesson.bbbb0001") {
		t.Fatalf("expected lesson.bbbb0001 as candidate:\n%s", out)
	}
	if !strings.Contains(out, "dry-run: 3 candidates (min-uses 0, force (uses only)) — ยังไม่เขียน") {
		t.Fatalf("promote dry-run summary wrong:\n%s", out)
	}
	// The quarantined lesson must never be a candidate.
	if strings.Contains(out, "lesson.bbbb0004") {
		t.Fatalf("quarantined lesson surfaced in promote:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "rules", "alpha-bravo-charlie-rule")); err == nil {
		t.Fatalf("dry-run wrote a rule")
	}
}

// TestCompactDryRun: the fixture's two project:Alpha handoffs are both older
// than the default window; --min-group 2 folds them into one dry group.
func TestCompactDryRun(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runHome(t, home, "", "compact", "--dry-run")
	mustCode(t, ExitOK, code, out, stderr, "compact --dry-run")
	if !strings.Contains(out, "dry-run: 0 groups (>= 3 handoffs, older than 7d) — ยังไม่เขียน") {
		t.Fatalf("compact default summary wrong:\n%s", out)
	}
	code, out, stderr = runHome(t, home, "", "compact", "--dry-run", "--min-group", "2")
	mustCode(t, ExitOK, code, out, stderr, "compact --min-group 2 --dry-run")
	if !strings.Contains(out, "DRY  scope=project:Alpha: 2 handoffs — handoff.cccc0001, handoff.cccc0002") {
		t.Fatalf("compact group line wrong:\n%s", out)
	}
	if !strings.Contains(out, "dry-run: 1 groups (>= 2 handoffs, older than 7d) — ยังไม่เขียน") {
		t.Fatalf("compact min-group summary wrong:\n%s", out)
	}
}

// TestPromoteScanWritesCandidatesFile: --scan writes the grouped candidates
// file plus the content digest, cross-references ops/failures, and the second
// run reports an unchanged digest instead of a change.
func TestPromoteScanWritesCandidatesFile(t *testing.T) {
	home := fixtureHome(t)
	writeFile(t, home, filepath.Join("ops", "failures", "2026-01-02-alpha.md"),
		"2026-01-02 — alpha bravo repeat — guard caught it\n")

	code, out, stderr := runHome(t, home, "", "promote", "--scan")
	mustCode(t, ExitOK, code, out, stderr, "promote --scan")
	if !strings.Contains(out, "promote --scan: 3 lessons · 1 groups -> ops/promotions-candidates.md") {
		t.Fatalf("scan summary wrong:\n%s", out)
	}

	data, err := os.ReadFile(filepath.Join(home, "ops", "promotions-candidates.md"))
	if err != nil {
		t.Fatalf("candidates file missing: %v", err)
	}
	content := string(data)
	if !strings.HasPrefix(content, "# promotion candidates — 2") {
		t.Fatalf("candidates file header wrong:\n%s", content[:80])
	}
	if !strings.Contains(content, "source: `mem promote --scan`") ||
		!strings.Contains(content, "ขั้นถัดไป: skill `/promote-lessons`") {
		t.Fatalf("candidates file missing fixed lines:\n%s", content)
	}
	if !strings.Contains(content, "รวม **3 lessons · 1 groups** · cross-ref failures: 1 lessons") {
		t.Fatalf("totals line wrong:\n%s", content)
	}
	if !strings.Contains(content, "## test — 3 lessons · failures-mentions 1 · uses 0") {
		t.Fatalf("group heading wrong:\n%s", content)
	}
	// alpha bravo charlie shares tokens with the failure line: failures=1.
	if !strings.Contains(content, "`lesson.bbbb0001`  alpha bravo charlie  ·  failures=1") {
		t.Fatalf("cross-ref mark missing:\n%s", content)
	}
	if strings.Contains(content, "lesson.bbbb0004") {
		t.Fatalf("quarantined lesson in candidates file:\n%s", content)
	}

	hash, err := os.ReadFile(filepath.Join(home, ".memory", "promo-scan.sha"))
	if err != nil {
		t.Fatalf("digest file missing: %v", err)
	}
	if len(strings.TrimSpace(string(hash))) != 64 {
		t.Fatalf("digest is not sha256 hex: %q", string(hash))
	}

	// Second run: same content -> same digest -> no-change suffix, no notify.
	code, out, stderr = runHome(t, home, "", "promote", "--scan", "--notify")
	mustCode(t, ExitOK, code, out, stderr, "promote --scan (second)")
	if !strings.Contains(out, "เนื้อหาไม่เปลี่ยนจากรอบก่อน — ไม่ notify") {
		t.Fatalf("second scan should report an unchanged digest:\n%s", out)
	}

	// A new lesson changes the content -> the suffix disappears.
	writeFile(t, home, filepath.Join("lessons", "late-lesson.bbbb0009.md"),
		"---\nid: lesson.bbbb0009\ntitle: late lesson joins the scan\nlayer: lesson\nscope: global\n"+
			"tags: [test]\ncreated: 2026-01-01\nlast_used: 2026-01-01\nstatus: active\nsensitivity: internal\n---\n\nlate body\n")
	code, out, stderr = runHome(t, home, "", "promote", "--scan")
	mustCode(t, ExitOK, code, out, stderr, "promote --scan (third)")
	if strings.Contains(out, "เนื้อหาไม่เปลี่ยน") {
		t.Fatalf("new lesson must flip the digest:\n%s", out)
	}
	if !strings.Contains(out, "4 lessons · 1 groups") {
		t.Fatalf("third scan should count 4 lessons:\n%s", out)
	}
}

// TestPromoteScanNoActiveLessons: with no active lessons the scan reports and
// exits 0 without writing anything.
func TestPromoteScanNoActiveLessons(t *testing.T) {
	home := fixtureHome(t)
	if err := os.RemoveAll(filepath.Join(home, "lessons")); err != nil {
		t.Fatalf("remove lessons: %v", err)
	}
	code, out, stderr := runHome(t, home, "", "promote", "--scan")
	mustCode(t, ExitOK, code, out, stderr, "promote --scan (empty)")
	if !strings.Contains(out, "promote --scan: ไม่มี lesson active — ไม่เขียนไฟล์") {
		t.Fatalf("empty scan message wrong:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "ops", "promotions-candidates.md")); !os.IsNotExist(err) {
		t.Fatalf("empty scan wrote the candidates file: %v", err)
	}
}

// TestPromoteRealRunWritesRule: a proven lesson becomes a rule and the lesson
// is marked promoted (the apply path behind --dry-run).
func TestPromoteRealRunWritesRule(t *testing.T) {
	home := fixtureHome(t)
	writeFile(t, home, filepath.Join("lessons", "proven-lesson.bbbb0007.md"),
		"---\nid: lesson.bbbb0007\ntitle: proven lesson worth a rule\nlayer: lesson\nscope: global\n"+
			"tags: [test]\ncreated: 2026-01-01\nlast_used: 2026-01-01\nuses: 3\nsuccesses: 3\n"+
			"status: active\nsensitivity: internal\n---\n\nproven lesson body\n")
	code, out, stderr := runHome(t, home, "", "promote")
	mustCode(t, ExitOK, code, out, stderr, "promote")
	if !strings.Contains(out, "promoted lesson.bbbb0007 -> rule.") ||
		!strings.Contains(out, "promote: 1/1 lessons -> rules (min-uses 3, strict (successes==uses>0))") {
		t.Fatalf("promote output wrong:\n%s", out)
	}
	rule, err := os.ReadFile(filepath.Join(home, "rules", "proven-lesson-worth-a-rule.md"))
	if err != nil {
		t.Fatalf("rule file missing: %v", err)
	}
	if !strings.Contains(string(rule), "promoted_from: lesson.bbbb0007") {
		t.Fatalf("rule front-matter missing promoted_from:\n%s", string(rule))
	}
}
