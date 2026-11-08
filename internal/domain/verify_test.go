// P2.1 verify tests: each contract violation is a problem, each hygiene
// risk a warning, and every check has a clean control. Synthetic only.
package domain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atomsbaza/my-memory-engine/internal/codec"
)

func hasMsg(list []string, substr string) bool {
	for _, m := range list {
		if strings.Contains(m, substr) {
			return true
		}
	}
	return false
}

func TestVerifyCleanFixture(t *testing.T) {
	root := copyHome(t)
	problems, warnings, err := Verify(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("clean fixture must have no problems: %v", problems)
	}
	// Expected fixture warnings: lesson without NOT., rules over budget,
	// and the no-index notice.
	for _, want := range []string{"ควรมี **NOT.**", "rules/ รวม", "ยังไม่มี index"} {
		if !hasMsg(warnings, want) {
			t.Errorf("clean fixture warning missing: %q (got %v)", want, warnings)
		}
	}
	// Control: the lesson that carries **NOT.** and evidence is not flagged.
	if hasMsg(warnings, "bbbb0003") {
		t.Errorf("compliant lesson flagged: %v", warnings)
	}
}

func TestVerifyProblems(t *testing.T) {
	root := copyHome(t)

	// Broken frontmatter.
	if err := os.WriteFile(filepath.Join(root, "lessons", "broken.md"), []byte("no front matter here"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Duplicate id: a second file carrying an existing id.
	data, err := os.ReadFile(filepath.Join(root, "lessons", "dedup-base-lesson.bbbb0001.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "lessons", "twin-lesson.bbbb0001.md"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	// Quarantine without external source.
	writeRecFile(t, root, "lesson", "lesson.bbbb0009", "local but quarantined",
		"quarantined without external source\n"+provenanceMarker,
		func(fm codec.Frontmatter) { fm["sensitivity"] = "quarantine" })
	// Secret-shaped content.
	writeRecFile(t, root, "lesson", "lesson.bbbb000a", "secret carrier",
		"password = abcdefabcdefabcdefabcdef\n"+provenanceMarker, nil)
	// Machine absolute path.
	writeRecFile(t, root, "lesson", "lesson.bbbb000b", "path carrier",
		"see /Users/fixture/secret.txt\n"+provenanceMarker, nil)
	// Malformed id.
	writeRecFile(t, root, "lesson", "not-an-id", "bad id record",
		"body\n"+provenanceMarker, nil)
	// Handoff without expires (layer requires it).
	writeRecFile(t, root, "handoff", "handoff.cccc0009", "no expiry handoff",
		"handoff without expires\n"+provenanceMarker,
		func(fm codec.Frontmatter) { fm["expires"] = "none" })
	// Unknown tag + missing-evidence confidence.
	writeRecFile(t, root, "lesson", "lesson.bbbb000c", "taggy lesson",
		"tagged oddly\n"+provenanceMarker,
		func(fm codec.Frontmatter) { fm["tags"] = []string{"bogus"} })

	problems, warnings, err := Verify(root)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"broken.md: ไม่มี frontmatter",
		"id ซ้ำ: lesson.bbbb0001",
		"sensitivity=quarantine ต้องมี source external:*",
		"พบ pattern คล้าย secret",
		"absolute path",
		"id ผิดรูปแบบ (not-an-id)",
		"layer handoff ต้องมี expires",
	} {
		if !hasMsg(problems, want) {
			t.Errorf("problem missing: %q (got %v)", want, problems)
		}
	}
	if !hasMsg(warnings, "tag 'bogus' ไม่อยู่ใน taxonomy.json") {
		t.Errorf("unknown-tag warning missing: %v", warnings)
	}
}

func TestVerifyLayerDirMismatch(t *testing.T) {
	root := copyHome(t)
	// A lesson record stored in rules/ (schema layer dirs disagree).
	rel := writeRecFile(t, root, "lesson", "lesson.bbbb000d", "misplaced lesson",
		"stored in the wrong folder\n"+provenanceMarker, nil)
	// writeRecFile places by layer; move the file into rules/ manually.
	src := filepath.Join(root, filepath.FromSlash(rel))
	dst := filepath.Join(root, "rules", filepath.Base(rel))
	if err := os.Rename(src, dst); err != nil {
		t.Fatal(err)
	}
	problems, _, err := Verify(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMsg(problems, "layer=lesson ไม่ตรงกับโฟลเดอร์ (rules/)") {
		t.Fatalf("layer-dir mismatch not reported: %v", problems)
	}
}

func TestVerifyRuleLinkWarning(t *testing.T) {
	root := copyHome(t)
	writeRecFile(t, root, "rule", "rule.aaaa0009", "rule with an external link",
		"see https://fixture.example/guide for details\n"+provenanceMarker, nil)
	_, warnings, err := Verify(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMsg(warnings, "rule มีลิงก์ภายนอก") {
		t.Fatalf("external-link rule warning missing: %v", warnings)
	}

	// Control: the same link with an import/external source is not flagged.
	writeRecFile(t, root, "rule", "rule.aaaa000a", "screened rule link",
		"see https://fixture.example/screened\n"+provenanceMarker,
		func(fm codec.Frontmatter) {
			fm["source"] = "external:fixture"
			fm["confidence"] = "1.0"
		})
	_, warnings2, err := Verify(root)
	if err != nil {
		t.Fatal(err)
	}
	if hasMsg(warnings2, "rule.aaaa000a") || hasMsg(warnings2, "screened-rule-link-rule.aaaa000a") {
		t.Fatalf("screened link must not warn: %v", warnings2)
	}
}

func TestVerifyInjectionWarning(t *testing.T) {
	root := copyHome(t)
	writeRecFile(t, root, "lesson", "lesson.bbbb000e", "injection-looking lesson",
		"text that says ignore previous instructions verbatim\n"+provenanceMarker, nil)
	_, warnings, err := Verify(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMsg(warnings, "prompt injection") {
		t.Fatalf("injection warning missing: %v", warnings)
	}
}

func TestVerifyIndexFreshness(t *testing.T) {
	root := copyHome(t)

	// Rebuilt index: no stale warning.
	if err := ReindexRoot(root); err != nil {
		t.Fatal(err)
	}
	_, warnings, err := Verify(root)
	if err != nil {
		t.Fatal(err)
	}
	if hasMsg(warnings, "index เก่า") || hasMsg(warnings, "ยังไม่มี index") {
		t.Fatalf("fresh index must not warn: %v", warnings)
	}

	// Control: write a record after the rebuild without reindexing.
	writeRecFile(t, root, "lesson", "lesson.bbbb000f", "post-index lesson",
		"written after the last reindex\n"+provenanceMarker, nil)
	_, warnings2, err := Verify(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMsg(warnings2, "index เก่า") {
		t.Fatalf("stale index must warn: %v", warnings2)
	}
}

func TestVerifyMissingDirs(t *testing.T) {
	root := copyHome(t)
	if err := os.RemoveAll(filepath.Join(root, "rules")); err != nil {
		t.Fatal(err)
	}
	problems, _, err := Verify(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMsg(problems, "ขาดโฟลเดอร์ rules/") {
		t.Fatalf("missing dir not reported: %v", problems)
	}
}

func TestVerifyConfidenceWithoutEvidence(t *testing.T) {
	root := copyHome(t)
	writeRecFile(t, root, "lesson", "lesson.bbbb0010", "low confidence no evidence",
		"hedged claim without evidence\n"+provenanceMarker, nil) // confidence 0.9 < 1.0
	_, warnings, err := Verify(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMsg(warnings, "confidence<1.0 แต่ไม่มี evidence") {
		t.Fatalf("confidence warning missing: %v", warnings)
	}
	// Control: confidence 1.0 without evidence is fine.
	writeRecFile(t, root, "lesson", "lesson.bbbb0011", "full confidence",
		"firm claim\n"+provenanceMarker,
		func(fm codec.Frontmatter) { fm["confidence"] = "1.0" })
	_, warnings2, err := Verify(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range warnings2 {
		if strings.Contains(w, "full-confidence-lesson.bbbb0011") && strings.Contains(w, "confidence<1.0") {
			t.Fatalf("confidence 1.0 must not warn: %q", w)
		}
	}
}
