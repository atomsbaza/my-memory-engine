// P2.1 eval tests: golden run metrics, baseline save/gate, regression
// FAIL control, and loud golden/baseline parse errors. The fixture is a
// synthetic home; real-corpus parity (S5) is a private gate, never CI.
package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atomsbaza/my-memory-engine/internal/domain"
	"github.com/atomsbaza/my-memory-engine/internal/index"
)

var evalFixture = filepath.Join("..", "..", "testdata", "eval", "home")

func copyEvalHome(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	if err := copyEvalTree(t, evalFixture, dst); err != nil {
		t.Fatal(err)
	}
	return dst
}

func copyEvalTree(t *testing.T, src, dst string) error {
	t.Helper()
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func rebuildIndex(t *testing.T, root string) {
	t.Helper()
	if err := domain.ReindexRoot(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(index.IndexPath(root)); err != nil {
		t.Fatal(err)
	}
}

func TestEvalRunMetricsAndBaseline(t *testing.T) {
	root := copyEvalHome(t)
	rebuildIndex(t, root)

	// No baseline yet + non-empty golden: fail-closed.
	res, err := Run(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "FAIL" || len(res.Fails) != 1 || !strings.Contains(res.Fails[0], "ยังไม่มี eval/baseline.json") {
		t.Fatalf("fail-closed verdict = %q fails=%v", res.Verdict, res.Fails)
	}

	// Save baseline: perfect retrieval on the fixture.
	res, err = Run(root, Options{SaveBaseline: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "SAVED" || !res.Saved {
		t.Fatalf("save verdict = %q", res.Verdict)
	}
	if res.Metrics.Recall5 != 1.0 || res.Metrics.MRR != 1.0 {
		t.Fatalf("fixture metrics = recall %v mrr %v, want 1/1", res.Metrics.Recall5, res.Metrics.MRR)
	}
	if res.Metrics.NPairs != 3 || res.Metrics.Stale != 0 {
		t.Fatalf("n_pairs = %d stale = %d, want 3/0", res.Metrics.NPairs, res.Metrics.Stale)
	}
	if _, err := os.Stat(filepath.Join(root, "eval", "baseline.json")); err != nil {
		t.Fatalf("baseline must be written: %v", err)
	}

	// Gate against the fresh baseline: PASS with identical metrics.
	res, err = Run(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "PASS" || len(res.Fails) != 0 {
		t.Fatalf("verdict = %q fails = %v", res.Verdict, res.Fails)
	}
	// Sweep: every expect id scored ≥ 7 at the 5.0-threshold sweep.
	if res.Metrics.SweepGE["7.0"] != 3 {
		t.Fatalf("sweep >=7.0 = %d, want 3 (metrics %v)", res.Metrics.SweepGE["7.0"], res.Metrics)
	}
}

func TestEvalRegressionFailsGate(t *testing.T) {
	root := copyEvalHome(t)
	rebuildIndex(t, root)
	if _, err := Run(root, Options{SaveBaseline: true}); err != nil {
		t.Fatal(err)
	}

	// Degrade one lesson so its query no longer matches: recall drops to
	// 2/3 ≈ 0.6667 — more than 0.05 under the 1.0 baseline.
	//
	// The control has to remove the title overlap too. score() gives layer 3.0
	// plus 4.0 per title token, and the fixture title shares "go" with the
	// query, so a body-only rewrite still lands at 7.9 — above the 6.0 hook
	// threshold. Changing the title as well is what actually removes the
	// record from retrieval, which is the regression this test must detect.
	path := filepath.Join(root, "lessons", "go-concurrency-lesson.face0001.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := strings.ReplaceAll(string(data), "title: go concurrency cancellation", "title: unrelated placeholder entry")
	rewritten = strings.ReplaceAll(rewritten,
		"Use context cancellation to stop goroutine fan-out; always check ctx.Err before writing results.",
		"Placeholder text with completely unrelated vocabulary inside.")
	rewritten = strings.ReplaceAll(rewritten,
		"**NOT.** ห้ามปล่อย goroutine ค้างโดยไม่มี path ออก",
		"**NOT.** placeholder anti-pattern.")
	if rewritten == string(data) {
		t.Fatal("control rewrite did not change the fixture")
	}
	if err := os.WriteFile(path, []byte(rewritten), 0o644); err != nil {
		t.Fatal(err)
	}
	rebuildIndex(t, root)

	res, err := Run(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "FAIL" {
		t.Fatalf("regression verdict = %q, want FAIL (metrics %v)", res.Verdict, res.Metrics)
	}
	if !strings.Contains(strings.Join(res.Fails, " · "), "recall@5") {
		t.Fatalf("fails should name the recall regression: %v", res.Fails)
	}
	if res.Metrics.Recall5 > 0.67 {
		t.Fatalf("degraded recall = %v, want ≈ 0.6667", res.Metrics.Recall5)
	}
}

// TestEvalAbstentionPairs: expect:[] pairs must surface nothing at the hook
// threshold — precision 1.0, excluded from n_pairs/stale/recall math, and the
// saved baseline carries the new counts.
func TestEvalAbstentionPairs(t *testing.T) {
	root := copyEvalHome(t)
	rebuildIndex(t, root)

	res, err := Run(root, Options{SaveBaseline: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Metrics.AbstentionTotal != 2 || res.Metrics.AbstentionPass != 2 {
		t.Fatalf("abstention = %d/%d, want 2/2 (metrics %v)", res.Metrics.AbstentionPass, res.Metrics.AbstentionTotal, res.Metrics)
	}
	if res.Metrics.AbstentionPrecision != 1.0 {
		t.Fatalf("abstention precision = %v, want 1.0", res.Metrics.AbstentionPrecision)
	}
	// Abstention pairs stay out of the retrieval math: same 3 live pairs.
	if res.Metrics.NPairs != 3 || res.Metrics.Stale != 0 {
		t.Fatalf("n_pairs = %d stale = %d, want 3/0 (metrics %v)", res.Metrics.NPairs, res.Metrics.Stale, res.Metrics)
	}
	if res.Metrics.Recall5 != 1.0 || res.Metrics.MRR != 1.0 {
		t.Fatalf("recall math changed: recall %v mrr %v", res.Metrics.Recall5, res.Metrics.MRR)
	}

	// The gate run against that baseline passes with the abstention counts.
	res, err = Run(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "PASS" || len(res.Fails) != 0 {
		t.Fatalf("verdict = %q fails = %v", res.Verdict, res.Fails)
	}
	bm, err := LoadBaseline(root)
	if err != nil {
		t.Fatal(err)
	}
	if bm.Metrics.AbstentionTotal != 2 || bm.Metrics.AbstentionPrecision != 1.0 {
		t.Fatalf("baseline abstention = %d/%v, want 2/1.0", bm.Metrics.AbstentionTotal, bm.Metrics.AbstentionPrecision)
	}
}

// TestEvalAbstentionFalseMatchFails: the discriminating negative control — an
// abstention pair whose query DOES match a record must fail the whole verdict,
// even though recall/MRR are perfect.
func TestEvalAbstentionFalseMatchFails(t *testing.T) {
	root := copyEvalHome(t)
	rebuildIndex(t, root)
	if _, err := Run(root, Options{SaveBaseline: true}); err != nil {
		t.Fatal(err)
	}

	// Rewrite one abstention pair into a query that hits lesson.face0001.
	golden := filepath.Join(root, "eval", "golden.jsonl")
	data, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := strings.ReplaceAll(string(data),
		"quantum chromodynamics lattice gauge renormalization group flow",
		"how to stop goroutine fan-out in go")
	if rewritten == string(data) {
		t.Fatal("control rewrite did not change the golden file")
	}
	if err := os.WriteFile(golden, []byte(rewritten), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Run(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "FAIL" {
		t.Fatalf("verdict = %q, want FAIL (metrics %v)", res.Verdict, res.Metrics)
	}
	if !strings.Contains(strings.Join(res.Fails, " · "), "abstention") {
		t.Fatalf("fails should name the abstention miss: %v", res.Fails)
	}
	if res.Metrics.AbstentionPass != 1 || res.Metrics.AbstentionTotal != 2 {
		t.Fatalf("abstention = %d/%d, want 1/2 (metrics %v)", res.Metrics.AbstentionPass, res.Metrics.AbstentionTotal, res.Metrics)
	}
	// Recall/MRR math untouched by the abstention failure.
	if res.Metrics.Recall5 != 1.0 || res.Metrics.MRR != 1.0 {
		t.Fatalf("recall math changed: recall %v mrr %v", res.Metrics.Recall5, res.Metrics.MRR)
	}
}

// TestEvalLegacyBaselineWithoutAbstention: a baseline saved before the
// abstention fields still loads and still gates (missing abstention = 0 pairs).
func TestEvalLegacyBaselineWithoutAbstention(t *testing.T) {
	root := copyEvalHome(t)
	rebuildIndex(t, root)
	if _, err := Run(root, Options{SaveBaseline: true}); err != nil {
		t.Fatal(err)
	}
	// Strip the abstention fields the way an old baseline looks.
	bp := filepath.Join(root, "eval", "baseline.json")
	data, err := os.ReadFile(bp)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	m := doc["metrics"].(map[string]any)
	delete(m, "abstention_pass")
	delete(m, "abstention_total")
	delete(m, "abstention_precision")
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bp, append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Run(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "PASS" || len(res.Fails) != 0 {
		t.Fatalf("legacy baseline verdict = %q fails = %v", res.Verdict, res.Fails)
	}
}

func TestEvalStalePairsSkipped(t *testing.T) {
	root := copyEvalHome(t)
	// Supersede one golden target: its pair goes stale (no live expect).
	path := filepath.Join(root, "lessons", "git-rebase-lesson.face0002.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(data), "status: active", "status: superseded")), 0o644); err != nil {
		t.Fatal(err)
	}
	rebuildIndex(t, root)

	res, err := Run(root, Options{SaveBaseline: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Metrics.Stale != 1 || res.Metrics.NPairs != 2 {
		t.Fatalf("stale = %d n = %d, want 1/2 (metrics %v)", res.Metrics.Stale, res.Metrics.NPairs, res.Metrics)
	}
	if res.Metrics.Recall5 != 1.0 {
		t.Fatalf("live pairs still perfect, recall = %v", res.Metrics.Recall5)
	}
}

func TestEvalEmptyGolden(t *testing.T) {
	root := copyEvalHome(t)
	if err := os.Remove(filepath.Join(root, "eval", "golden.jsonl")); err != nil {
		t.Fatal(err)
	}
	res, err := Run(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "EMPTY" {
		t.Fatalf("empty golden verdict = %q, want EMPTY", res.Verdict)
	}
}

func TestLoadGoldenLoudErrors(t *testing.T) {
	root := copyEvalHome(t)
	golden := filepath.Join(root, "eval", "golden.jsonl")

	cases := []struct {
		name, content, wantErr string
	}{
		{"bad json", "{\"query\": \"q\"\n", "บรรทัด 1"},
		{"expect not a list", "{\"query\": \"q\", \"expect\": \"lesson.face0001\"}\n", "บรรทัด 1"},
		{"blank query", "{\"query\": \" \", \"expect\": [\"lesson.face0001\"]}\n", "บรรทัด 1"},
		{"empty id", "{\"query\": \"q\", \"expect\": [\"\"]}\n", "บรรทัด 1"},
		{"second line", "{\"query\": \"ok\", \"expect\": [\"lesson.face0001\"]}\nnot json\n", "บรรทัด 2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := os.WriteFile(golden, []byte(c.content), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadGolden(root)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want %q", err, c.wantErr)
			}
		})
	}
}

func TestParseWeights(t *testing.T) {
	base := domain.DefaultScoring
	got, err := ParseWeights("w_conf=0.5,threshold_hook=7", base)
	if err != nil {
		t.Fatal(err)
	}
	if got.WConf != 0.5 || got.ThresholdHook != 7 {
		t.Fatalf("weights = %+v", got)
	}
	if _, err := ParseWeights("typo=1", base); err == nil || !strings.Contains(err.Error(), "ไม่รู้จัก key") {
		t.Fatalf("unknown key err = %v", err)
	}
	if _, err := ParseWeights("w_conf=high", base); err == nil || !strings.Contains(err.Error(), "ไม่ใช่ตัวเลข") {
		t.Fatalf("bad value err = %v", err)
	}
	if _, err := ParseWeights("w_conf", base); err == nil || !strings.Contains(err.Error(), "รูปแบบผิด") {
		t.Fatalf("bad format err = %v", err)
	}
}

func TestBaselineShapeErrors(t *testing.T) {
	root := copyEvalHome(t)
	bp := filepath.Join(root, "eval", "baseline.json")
	for _, content := range []string{"[]", `{"metrics": null}`, "{ broken"} {
		if err := os.WriteFile(bp, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadBaseline(root); err == nil {
			t.Errorf("baseline %q must fail loudly", content)
		}
	}
}

func TestEvalNoiseFromLog(t *testing.T) {
	// Inject rows for a,b,c; cite a; echo b in the next prompt of the same
	// session; c is pure noise → noise = 1/3.
	rows := []logRow{
		{"t": "2026-11-08T10:00:00", "type": "inject", "session": "s1",
			"ids": []any{"lesson.aaaaaaaa", "lesson.bbbbbbbb", "lesson.cccccccc"}},
		{"t": "2026-11-08T10:05:00", "type": "cite", "id": "lesson.aaaaaaaa"},
		{"t": "2026-11-08T10:06:00", "type": "inject", "session": "s1",
			"ids": []any{"lesson.dddddddd"}, "prompt_head": "re [id: lesson.bbbbbbbb] thanks"},
	}
	noise, injected, cited, echoed := evalNoise(rows)
	if noise == nil || *noise != 1.0/2.0 {
		t.Fatalf("noise = %v, want 1/2", *noise)
	}
	if len(injected) != 4 || len(cited) != 1 || len(echoed) != 1 {
		t.Fatalf("sets: injected=%v cited=%v echoed=%v", injected, cited, echoed)
	}

	// No inject rows → n/a (nil), never a crash.
	if noise, _, _, _ := evalNoise([]logRow{{"type": "cite", "id": "x"}}); noise != nil {
		t.Fatalf("noise without injects = %v, want nil", noise)
	}
}
