// Engine-only and reporting command tests: init, doctor, stats, render, eval
// (AC-P2-07 init/doctor, AC-P2-08 --json purity).
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInitScaffoldsUsableHome(t *testing.T) {
	target := filepath.Join(t.TempDir(), "fresh-home")
	code, out, stderr := runMain(t, "", "init", target)
	mustCode(t, ExitOK, code, out, stderr, "init")
	for _, rel := range []string{
		"schema/taxonomy.json", "schema/agents.json", ".gitignore", ".git",
		"rules", "projects", "lessons", "decisions", "handoffs", "knowledge", "ops", "_archive",
	} {
		if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("init did not create %s: %v", rel, err)
		}
	}

	code, out, stderr = runHome(t, target, "", "verify")
	mustCode(t, ExitOK, code, out, stderr, "verify fresh home")
	if !strings.Contains(out, "0 records · 0 problems") {
		t.Fatalf("fresh home verify = %q, want 0 problems", out)
	}
	code, out, stderr = runHome(t, target, "", "doctor")
	mustCode(t, ExitOK, code, out, stderr, "doctor fresh home")
	if !strings.Contains(out, "0 problems") {
		t.Fatalf("fresh home doctor = %q", out)
	}
}

// TestInitLeavesExistingHomeUntouched is the AC-P2-07 invariant: bytes and
// mtimes of an existing home are identical after re-running init.
func TestInitLeavesExistingHomeUntouched(t *testing.T) {
	home := fixtureHome(t)
	before := snapshotTree(t, home)

	code, out, stderr := runHome(t, home, "", "init")
	mustCode(t, ExitOK, code, out, stderr, "init over existing home")
	if !strings.Contains(out, "มีอยู่แล้ว — ไม่แตะ") {
		t.Fatalf("init did not report the untouched home:\n%s", out)
	}

	after := snapshotTree(t, home)
	for rel, want := range before {
		got, ok := after[rel]
		if !ok {
			t.Fatalf("init removed %s", rel)
		}
		if got != want {
			t.Fatalf("init changed %s: %s -> %s", rel, want, got)
		}
	}
	for rel := range after {
		if _, ok := before[rel]; !ok {
			t.Fatalf("init created %s in an existing home", rel)
		}
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// A path can vanish between ReadDir and lstat when git's
			// background bookkeeping (e.g. objects/maintenance.lock)
			// churns inside .git mid-walk — those entries are excluded
			// from the snapshot anyway. Never swallow a vanished root.
			if path != root && os.IsNotExist(err) {
				return nil
			}
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) || rel == ".git" {
			return nil
		}
		if info.IsDir() {
			out[rel] = "dir"
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		out[rel] = info.ModTime().Format(time.RFC3339Nano) + "|" + string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// TestDoctorReportsMissingIndex is the AC-P2-07 discriminating control: a
// doctor that stays quiet when the index is gone fails this criterion.
func TestDoctorReportsMissingIndex(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runHome(t, home, "", "reindex")
	mustCode(t, ExitOK, code, out, stderr, "reindex")
	code, out, stderr = runHome(t, home, "", "doctor")
	mustCode(t, ExitOK, code, out, stderr, "doctor with index")

	if err := os.Remove(filepath.Join(home, ".memory", "index-engine.sqlite")); err != nil {
		t.Fatalf("remove index: %v", err)
	}
	code, out, stderr = runHome(t, home, "", "doctor")
	mustCode(t, ExitError, code, out, stderr, "doctor without index")
	if !strings.Contains(out, "index") || !strings.Contains(out, "FAIL") {
		t.Fatalf("doctor did not report the missing index:\n%s", out)
	}
}

func TestDoctorReportsBrokenFrontMatter(t *testing.T) {
	home := fixtureHome(t)
	writeFile(t, home, "lessons/broken.md", "no front matter\n")
	code, out, _ := runHome(t, home, "", "doctor")
	mustCode(t, ExitError, code, out, "", "doctor broken file")
	if !strings.Contains(out, "broken.md") {
		t.Fatalf("doctor did not name the broken file:\n%s", out)
	}
}

// TestStatsJSONPurity: stats --json is exactly one JSON document on stdout.
func TestStatsJSONPurity(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runHome(t, home, "", "stats", "--json")
	mustCode(t, ExitOK, code, out, stderr, "stats --json")
	var payload statsPayload
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		t.Fatalf("stats --json is not a single JSON document: %v\n%s", err, out)
	}
	if dec.More() {
		t.Fatalf("stats --json emitted more than one document:\n%s", out)
	}
	if payload.Records == 0 || len(payload.ByLayer) == 0 {
		t.Fatalf("stats payload looks empty: %+v", payload)
	}
	if payload.Active == 0 {
		t.Fatalf("stats active = 0: %+v", payload)
	}
}

func TestStatsTextCounts(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runHome(t, home, "", "stats")
	mustCode(t, ExitOK, code, out, stderr, "stats")
	if !strings.Contains(out, "records ") || !strings.Contains(out, "by layer:") || !strings.Contains(out, "harness coverage") {
		t.Fatalf("stats text missing sections:\n%s", out)
	}
}

// TestRenderWritesBlockAndSkips: render writes the block only for render/
// pointer modes and reports the skip for pull/tool (AC-P2-06 render modes).
func TestRenderWritesBlockAndSkips(t *testing.T) {
	home := fixtureHome(t)
	renderFile := filepath.Join(t.TempDir(), "AGENTS.md")
	pullFile := filepath.Join(t.TempDir(), "pulled.md")
	// Built via json.Marshal, not string interpolation: Windows temp paths
	// contain backslashes that would produce invalid JSON escapes.
	agentsDoc, err := json.Marshal(map[string]any{
		"version": 2,
		"agents": []map[string]any{
			{"name": "alpha", "mode": "render", "file": renderFile, "create": true, "budget": 5400},
			{"name": "pointy", "mode": "pointer", "file": filepath.Join(t.TempDir(), "pointer.md"), "create": true},
			{"name": "pullonly", "mode": "pull", "file": pullFile, "create": false},
			{"name": "toolonly", "mode": "tool", "file": filepath.Join(t.TempDir(), "none.md"), "create": false},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, home, "schema/agents.json", string(agentsDoc))

	code, out, stderr := runHome(t, home, "", "render", "--dry-run")
	mustCode(t, ExitOK, code, out, stderr, "render --dry-run")
	if _, err := os.Stat(renderFile); err == nil {
		t.Fatalf("--dry-run wrote %s", renderFile)
	}
	if !strings.Contains(out, "skip pullonly") || !strings.Contains(out, "skip toolonly") {
		t.Fatalf("render did not report the pull/tool skips:\n%s", out)
	}

	code, out, stderr = runHome(t, home, "", "render")
	mustCode(t, ExitOK, code, out, stderr, "render")
	data, err := os.ReadFile(renderFile)
	if err != nil {
		t.Fatalf("render did not write the agent file: %v", err)
	}
	if !strings.Contains(string(data), "BEGIN my-memory") || !strings.Contains(string(data), "กฎถาวรของผู้ใช้") {
		t.Fatalf("render block missing:\n%s", data)
	}

	// Re-render must be a no-op ("ok — ไม่เปลี่ยน"), not a growing file.
	code, out, stderr = runHome(t, home, "", "render")
	mustCode(t, ExitOK, code, out, stderr, "render twice")
	if !strings.Contains(out, "ok   alpha") {
		t.Fatalf("second render changed the block again:\n%s", out)
	}
}

func TestRenderPrintMode(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runHome(t, home, "", "render", "--print")
	mustCode(t, ExitOK, code, out, stderr, "render --print")
	if out != "" && !strings.Contains(out, "BEGIN my-memory") {
		t.Fatalf("render --print printed no block:\n%s", out)
	}
}

// evalHome builds a home with a golden pair matching a fixture record.
func evalHome(t *testing.T) string {
	t.Helper()
	home := fixtureHome(t)
	writeFile(t, home, "eval/golden.jsonl",
		`{"query":"alpha bravo charlie","expect":["lesson.bbbb0001"],"origin":"authored"}`+"\n")
	return home
}

func TestEvalJSONPurityAndGate(t *testing.T) {
	home := evalHome(t)
	code, out, stderr := runHome(t, home, "", "eval", "--json")
	mustCode(t, ExitError, code, out, stderr, "eval --json without baseline")
	var payload struct {
		Metrics json.RawMessage `json:"metrics"`
		Scoring json.RawMessage `json:"scoring"`
		Verdict string          `json:"verdict"`
	}
	dec := json.NewDecoder(strings.NewReader(out))
	if err := dec.Decode(&payload); err != nil {
		t.Fatalf("eval --json is not a single JSON document: %v\n%s", err, out)
	}
	if dec.More() {
		t.Fatalf("eval --json emitted extra output:\n%s", out)
	}
	if payload.Verdict != "FAIL" {
		t.Fatalf("verdict = %q, want FAIL (no baseline is fail-closed)", payload.Verdict)
	}

	code, out, stderr = runHome(t, home, "", "eval", "--save-baseline")
	mustCode(t, ExitOK, code, out, stderr, "eval --save-baseline")
	if !strings.Contains(out, "saved baseline") {
		t.Fatalf("save-baseline output = %q", out)
	}
	if _, err := os.Stat(filepath.Join(home, "eval", "baseline.json")); err != nil {
		t.Fatalf("baseline not written: %v", err)
	}

	code, out, stderr = runHome(t, home, "", "eval", "--json")
	mustCode(t, ExitOK, code, out, stderr, "eval --json with baseline")
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("eval --json after baseline: %v\n%s", err, out)
	}
	if payload.Verdict != "PASS" {
		t.Fatalf("verdict = %q, want PASS", payload.Verdict)
	}
}

func TestEvalEmptyGolden(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runHome(t, home, "", "eval")
	mustCode(t, ExitOK, code, out, stderr, "eval without golden")
	if !strings.Contains(out, "golden set ว่าง") {
		t.Fatalf("eval output = %q", out)
	}
}
