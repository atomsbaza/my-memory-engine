// P2.1 render tests: context section budgets and markers, quarantine and
// expiry exclusion, knowledge rotation, recall-body budget cuts with Thai
// text (rune semantics), and render_block budget cutting. Synthetic only.
package domain

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/atomsbaza/my-memory-engine/internal/codec"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

func renderCtx(t *testing.T, root string, recs []store.Record, o ContextOptions) string {
	t.Helper()
	o.Root = root
	out, _, err := RenderContext(recs, o)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRenderContextSectionsAndBudgets(t *testing.T) {
	pinClock(t, "2026-11-08T10:00:00")
	root := copyHome(t)
	recs := nil2Records(t, root)

	out := renderCtx(t, root, recs, ContextOptions{Scope: "project:Alpha"})

	// Head, end marker and the expected sections in the Python order.
	for _, marker := range []string{
		"[Memory — ความจำถาวรของผู้ใช้",
		"## Rules (MUST follow)",
		"## Project: Alpha",
		"_[source: rules/*.md]_",
		"[End of memory]",
	} {
		if !strings.Contains(out, marker) {
			t.Errorf("context missing %q", marker)
		}
	}
	if pos(out, "## Rules") > pos(out, "## Project") {
		t.Errorf("section order wrong:\n%s", out)
	}

	// The fixture's total budget (1200) is spent by rules+project+lessons, so
	// the knowledge pointer block sees remain < 200 and is skipped entirely —
	// Python cmd_context's add() drops such a section. With a total budget
	// that covers every section the block appears, after the others.
	roomy := renderCtx(t, root, recs, ContextOptions{Scope: "project:Alpha", Budget: 20000})
	if pos(roomy, "## Rules") > pos(roomy, "## Project") ||
		pos(roomy, "## Project") > pos(roomy, "## Knowledge") {
		t.Errorf("section order under a roomy budget wrong:\n%s", roomy)
	}

	// The fixture rules bodies exceed the fixture rules budget (300) — the
	// section must be visibly truncated, not silently over budget.
	if !strings.Contains(out, "…[truncated]") {
		t.Errorf("rules section must be truncated at its budget:\n%s", out)
	}
	if i := strings.Index(out, "## Rules"); i >= 0 {
		section := out[i:strings.Index(out[i:], "\n\n## ")+i]
		if rlen(section) > 300+200 { // cap + block header/marker slack
			t.Errorf("rules section = %d runes, budget cap is 300", rlen(section))
		}
	}
}

func TestRenderContextBudgetSkipsSmallSections(t *testing.T) {
	pinClock(t, "2026-11-08T10:00:00")
	root := copyHome(t)
	recs := nil2Records(t, root)

	// Total 250: the rules block fits (clipped to the remaining ≥200), then
	// the next section sees remain < 200 and is skipped entirely.
	out := renderCtx(t, root, recs, ContextOptions{Scope: "project:Alpha", Budget: 250})
	if !strings.Contains(out, "## Rules") {
		t.Fatalf("rules must render inside a 250 budget:\n%s", out)
	}
	if strings.Contains(out, "## Lessons") || strings.Contains(out, "## Knowledge") {
		t.Fatalf("sections after the budget is spent must be skipped:\n%s", out)
	}
}

func TestRenderContextExclusions(t *testing.T) {
	pinClock(t, "2026-11-08T10:00:00")
	root := copyHome(t)
	recs := nil2Records(t, root)

	// Quarantine rules never enter the rules block (M5).
	out := renderCtx(t, root, recs, ContextOptions{})
	if strings.Contains(out, "unscreened external content") {
		t.Errorf("quarantined lesson body leaked into context:\n%s", out)
	}

	// The quarantined fixture is a lesson: query its content and confirm it
	// never surfaces in the Lessons section, while a clean twin does.
	out = renderCtx(t, root, recs, ContextOptions{Query: "external content quarantine screened"})
	if strings.Contains(out, "lesson.bbbb0004") {
		t.Errorf("quarantined lesson leaked into selection-backed sections:\n%s", out)
	}

	// Expired handoffs are filtered from the handoff section; the fresh one
	// stays.
	out = renderCtx(t, root, recs, ContextOptions{Scope: "project:Alpha", Query: "handoff expired stale fresh"})
	if strings.Contains(out, "handoff.cccc0002") {
		t.Errorf("expired handoff leaked into context:\n%s", out)
	}
	if !strings.Contains(out, "handoff.cccc0001") {
		t.Errorf("fresh handoff missing from context:\n%s", out)
	}
}

func TestRenderContextKnowledgeRotation(t *testing.T) {
	pinClock(t, "2026-11-08T10:00:00")
	root := t.TempDir()
	writeTaxonomyOnly(t, root)
	// 15 knowledge records with ascending last_used: the 12 oldest are the
	// rotation set — every one of them is touched (so the set floats to the
	// tail and the next pass surfaces the rest), while the newest three stay
	// out of the block and out of touched.
	for i := 1; i <= 15; i++ {
		id := fmt.Sprintf("knowledge.k%02d", i)
		writeRecFile(t, root, "knowledge", id, fmt.Sprintf("pointer %02d", i),
			fmt.Sprintf("knowledge pointer number %02d\n"+provenanceMarker, i),
			func(fm codec.Frontmatter) {
				fm["last_used"] = fmt.Sprintf("2026-01-%02d", i)
			})
	}
	recs := nil2Records(t, root)
	out, touched, err := RenderContext(recs, ContextOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	var shown []string
	for _, r := range touched {
		if r.Layer == "knowledge" {
			shown = append(shown, r.ID)
		}
	}
	if len(shown) != 12 {
		t.Fatalf("rotation touches %d knowledge records, want 12: %v", len(shown), shown)
	}
	for _, id := range shown {
		if id == "knowledge.k14" || id == "knowledge.k15" {
			t.Errorf("rotation must leave the newest records for the next pass, got %s", id)
		}
	}
	if !strings.Contains(out, "[id: knowledge.k01]") {
		t.Errorf("oldest pointer missing from the rendered block:\n%s", out)
	}
	// The rendered block is clipped at the fixture knowledge budget (300) —
	// the pointer that no longer fits is cut, not silently over budget.
	section := out[strings.Index(out, "## Knowledge"):]
	if !strings.Contains(section, "…[truncated]") {
		t.Errorf("knowledge block must be truncated at its budget:\n%s", section)
	}
	if rlen(strings.SplitN(section, "\n\n", 2)[0]) > 300+200 {
		t.Errorf("knowledge block exceeds its budget:\n%s", section)
	}
}

func TestRenderContextEmptyHome(t *testing.T) {
	root := t.TempDir()
	writeTaxonomyOnly(t, root)
	out := renderCtx(t, root, nil2Records(t, root), ContextOptions{Scope: "global"})
	if out != "(memory ว่างสำหรับ scope นี้)" {
		t.Fatalf("empty home render = %q", out)
	}
}

func TestRenderContextTouched(t *testing.T) {
	pinClock(t, "2026-11-08T10:00:00")
	root := copyHome(t)
	recs := nil2Records(t, root)
	_, touched, err := RenderContext(recs, ContextOptions{Scope: "project:Alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(touched) == 0 {
		t.Fatal("context over a populated home must touch records")
	}
	for _, r := range touched {
		if r.Layer != "project" && r.Layer != "lesson" && r.Layer != "decision" &&
			r.Layer != "handoff" && r.Layer != "knowledge" {
			t.Errorf("unexpected touched record %s (%s)", r.ID, r.Layer)
		}
	}
	// Rules are injected but not touched (Python: touched only gets project
	// + selected + knowledge records).
	for _, r := range touched {
		if r.Layer == "rule" {
			t.Errorf("rules must not be touched: %s", r.ID)
		}
	}
}

func TestRenderRecallBodyBudgetCut(t *testing.T) {
	pinClock(t, "2026-11-08T10:00:00")
	thai := mrec("lesson.thai1", "lesson", "ไทย",
		"ทดสอบการตัดงบประมาณด้วยภาษาไทย "+strings.Repeat("ข้อมูล", 30), "global", nil)
	long := mrec("lesson.long1", "lesson", "long", "word "+strings.Repeat("filler ", 120), "global", nil)
	short1 := mrec("lesson.s1", "lesson", "first", "short body one", "global", nil)
	short2 := mrec("lesson.s2", "lesson", "second", "another short body two", "global", nil)

	// Two short lines: the budget cuts inside the second line, so the
	// first line keeps its id tag while the second loses it (the ids that
	// survive the cut are exactly what eval counts).
	body := RenderRecallBody([]store.Record{short1, short2}, 60)
	if !strings.Contains(body, "[id: lesson.s1]") {
		t.Fatalf("first line keeps its id tag: %q", body)
	}
	if strings.Contains(body, "[id: lesson.s2]") {
		t.Fatalf("second line id must fall off the cut: %q", body)
	}
	if !strings.HasSuffix(body, " …") {
		t.Fatalf("budget cut must end with the ellipsis marker: %q", body)
	}
	if rlen(body) > 62 { // 60-rune clip + " …"
		t.Fatalf("body = %d runes, want ≤ 62", rlen(body))
	}

	// Generous budget: both tags survive, no cut marker.
	full := RenderRecallBody([]store.Record{short1, short2}, 1000)
	if !strings.Contains(full, "[id: lesson.s1]") || !strings.Contains(full, "[id: lesson.s2]") {
		t.Fatalf("generous budget keeps both tags: %q", full)
	}
	if strings.HasSuffix(full, " …") {
		t.Fatalf("generous budget must not cut: %q", full)
	}

	// Thai text: the cut lands on a rune boundary, never mid-byte.
	thaiBody := RenderRecallBody([]store.Record{thai}, 100)
	if !utf8.ValidString(thaiBody) {
		t.Fatalf("Thai budget cut produced invalid UTF-8: %q", tail(thaiBody, 30))
	}
	if rlen(thaiBody) > 102 {
		t.Fatalf("thai body = %d runes, want ≤ 102", rlen(thaiBody))
	}

	// 340-rune per-line cap and empty behaviour.
	capped := RenderRecallBody([]store.Record{long}, 10000)
	firstLine := strings.SplitN(capped, "\n", 2)[0]
	if rlen(firstLine) > 340+2+rlen("[id: lesson.long1]")+2 {
		t.Fatalf("per-line cap exceeded: %d runes", rlen(firstLine))
	}
	if RenderRecallBody(nil, 100) != "" {
		t.Fatal("empty pick renders empty body")
	}
}

func TestRenderBlockBudgetCut(t *testing.T) {
	pinClock(t, "2026-11-08T10:00:00")
	root := copyHome(t)
	// The room floor is max(500, budget−usage−200): make the rules text
	// long enough that even the floor cannot hold it.
	writeRecFile(t, root, "rule", "rule.aaaa0003", "long rule body",
		strings.Repeat("กฎยาวสำหรับทดสอบการตัด budget ", 30)+"\n"+provenanceMarker,
		func(fm codec.Frontmatter) { fm["confidence"] = "1.0" })
	recs := nil2Records(t, root)

	// Fixture budgets give rules 300 — after the usage header the rules
	// text cannot fit and must be cut at a line boundary with the marker.
	block := RenderBlock(root, recs, AgentDef{Name: "fixture-agent", Mode: "render"}, 300)
	if !strings.HasPrefix(block, RenderBegin+"\n") || !strings.HasSuffix(block, RenderEnd+"\n") {
		t.Fatalf("block must be fenced:\n%s", tail(block, 60))
	}
	if !strings.Contains(block, renderCutMarker) {
		t.Fatalf("over-budget rules must carry the cut marker:\n%s", tail(block, 120))
	}


	// Control: a generous budget keeps both fixture rules intact.
	full := RenderBlock(root, recs, AgentDef{Name: "fixture-agent", Mode: "render"}, 5000)
	if strings.Contains(full, renderCutMarker) {
		t.Fatalf("generous budget must not cut:\n%s", tail(full, 120))
	}
	if !strings.Contains(full, "ตรวจ artifact จริง") || !strings.Contains(full, "budget ของ block") {
		t.Fatal("both fixture rules must appear in the full block")
	}

	// Pointer mode is the fixed pointer text.
	pointer := RenderBlock(root, recs, AgentDef{Name: "p", Mode: "pointer", WriteCmd: "add --layer lesson"}, 300)
	if !strings.Contains(pointer, "## Memory — ความจำถาวรของผู้ใช้ (my-memory)") ||
		!strings.Contains(pointer, "add --layer lesson") || strings.Contains(pointer, "### กฎถาวรของผู้ใช้") {
		t.Fatalf("pointer block shape wrong:\n%s", pointer)
	}
}

func TestVerifyRenderAgentInjectChecks(t *testing.T) {
	root := copyHome(t)
	// Push the rules past the room floor so the inject block is cut.
	writeRecFile(t, root, "rule", "rule.aaaa0004", "verbose rule",
		strings.Repeat("กฎยาวเกิน budget ของ instruction block ", 30)+"\n"+provenanceMarker,
		func(fm codec.Frontmatter) { fm["confidence"] = "1.0" })
	if err := os.MkdirAll(filepath.Join(root, "schema"), 0o755); err != nil {
		t.Fatal(err)
	}
	agents := `{"version": 1, "agents": [
		{"name": "small-agent", "file": "~/notes/AGENTS.md", "mode": "render", "create": true}
	]}`
	if err := os.WriteFile(filepath.Join(root, "schema", "agents.json"), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	problems, warnings, err := Verify(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("fixture with agents must be problem-free: %v", problems)
	}
	// The fixture rules exceed the render budget → the cut warning fires.
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "inject ของ agent 'small-agent' ถูกตัด") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want inject-cut warning for small-agent, got %v", warnings)
	}

	// Control: a big per-agent budget keeps every rule → no cut warning, no
	// missing-rule warning.
	big := `{"version": 1, "agents": [
		{"name": "big-agent", "file": "~/notes/AGENTS.md", "mode": "render", "create": true, "budget": 5000}
	]}`
	if err := os.WriteFile(filepath.Join(root, "schema", "agents.json"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	_, warnings2, err := Verify(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range warnings2 {
		if strings.Contains(w, "big-agent") {
			t.Errorf("big-agent must be clean, got %q", w)
		}
	}
}

// writeTaxonomyOnly creates a minimal home with the fixture taxonomy and
// all layer directories, but no records.
func writeTaxonomyOnly(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "schema"), 0o755); err != nil {
		t.Fatal(err)
	}
	copyTree(t, filepath.Join(fixtureHome, "schema"), filepath.Join(root, "schema"))
	for _, dir := range []string{"rules", "lessons", "decisions", "handoffs", "knowledge", "projects"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func pos(s, sub string) int { return strings.Index(s, sub) }

func tail(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[len(r)-n:])
	}
	return s
}
