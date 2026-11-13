// Behaviour tests for `mem links`: every assertion runs the real router
// against the synthetic testdata/links/home (never the real ~/my-memory), so
// the exit codes, the greppable text shape and the JSON document are the
// process contract.
package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// linksHome copies the synthetic links fixture into a temp dir.
func linksHome(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	copyTree(t, "../../testdata/links/home", dst)
	return dst
}

// row renders the aligned kind+id prefix of one edge row, so assertions do
// not hard-code the column padding.
func row(kind, id string) string {
	return fmt.Sprintf("%-15s%-19s", kind, id)
}

// TestLinksSupersedeChain: depth 1 shows the direct supersede edge both ways;
// the header carries the record identity fields.
func TestLinksSupersedeChain(t *testing.T) {
	home := linksHome(t)
	code, out, stderr := runHome(t, home, "", "links", "lesson.1a2b0001")
	mustCode(t, ExitOK, code, out, stderr, "links head")
	for _, want := range []string{
		"record  lesson.1a2b0001  [lesson/global]  status=active",
		"file    lessons/chain-head-lesson.1a2b0001.md",
		"OUTBOUND (this → others)",
		row("supersedes", "lesson.1a2b0002"),
		"INBOUND (others → this)",
		row("superseded-by", "lesson.0a1b0001"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// Depth 1 must not leak the transitive tail.
	if strings.Contains(out, "lesson.1a2b0003") {
		t.Errorf("depth 1 expanded the chain:\n%s", out)
	}
	// DANGLING must only appear for missing ids.
	if strings.Contains(out, "DANGLING") {
		t.Errorf("existing edge marked dangling:\n%s", out)
	}
}

// TestLinksDepthExpandsSupersedeChain: --depth 2 walks the chain transitively
// and marks the extra hop; the cap is a usage error.
func TestLinksDepthExpandsSupersedeChain(t *testing.T) {
	home := linksHome(t)
	code, out, stderr := runHome(t, home, "", "links", "lesson.1a2b0001", "--depth", "2")
	mustCode(t, ExitOK, code, out, stderr, "links --depth 2")
	if !strings.Contains(out, "lesson.1a2b0003") || !strings.Contains(out, "(depth 2)") {
		t.Fatalf("depth 2 did not reach the tail:\n%s", out)
	}
	code, out, stderr = runHome(t, home, "", "links", "lesson.1a2b0001", "--depth", "11")
	mustCode(t, ExitGuard, code, out, stderr, "links --depth 11")
	if out != "" {
		t.Errorf("--depth 11 wrote to stdout: %q", out)
	}
	code, out, stderr = runHome(t, home, "", "links", "lesson.1a2b0001", "--depth", "0")
	mustCode(t, ExitGuard, code, out, stderr, "links --depth 0")
}

// TestLinksEvidenceAndBodyRefs: evidence ids resolve, free-text evidence is
// ignored, body markers collapse duplicates and dangling ids are explicit.
func TestLinksEvidenceAndBodyRefs(t *testing.T) {
	home := linksHome(t)
	code, out, stderr := runHome(t, home, "", "links", "decision.2b3c0001")
	mustCode(t, ExitOK, code, out, stderr, "links decision")
	if !strings.Contains(out, row("evidence", "lesson.1a2b0001")) {
		t.Errorf("outbound evidence edge missing:\n%s", out)
	}
	if strings.Contains(out, "synthetic note") {
		t.Errorf("free-text evidence treated as an id:\n%s", out)
	}
	if !strings.Contains(out, row("body-ref", "knowledge.3c4d0001")) {
		t.Errorf("inbound body-ref edge missing:\n%s", out)
	}

	code, out, stderr = runHome(t, home, "", "links", "knowledge.3c4d0001")
	mustCode(t, ExitOK, code, out, stderr, "links knowledge")
	if !strings.Contains(out, row("body-ref", "decision.2b3c0001")) {
		t.Errorf("outbound body-ref edge missing:\n%s", out)
	}
	if !strings.Contains(out, "(line 2, +1 more)") {
		t.Errorf("duplicate body refs did not collapse with +K:\n%s", out)
	}
	if c := strings.Count(out, row("body-ref", "decision.2b3c0001")); c != 1 {
		t.Errorf("collapsed row appears %d times, want 1:\n%s", c, out)
	}
	if !strings.Contains(out, row("body-ref", "lesson.9d9d9999")+"DANGLING (not found)") {
		t.Errorf("dangling marker missing:\n%s", out)
	}
}

// TestLinksPromotedAndCompacted: the lifecycle pointers invert into
// promoted-to / compacted-into inbound edges.
func TestLinksPromotedAndCompacted(t *testing.T) {
	home := linksHome(t)
	code, out, stderr := runHome(t, home, "", "links", "lesson.7a8b0001")
	mustCode(t, ExitOK, code, out, stderr, "links promoted lesson")
	if !strings.Contains(out, row("promoted-to", "rule.6f7a0001")) {
		t.Errorf("inbound promoted-to edge missing:\n%s", out)
	}

	code, out, stderr = runHome(t, home, "", "links", "knowledge.9b0c0001")
	mustCode(t, ExitOK, code, out, stderr, "links compacted knowledge")
	if !strings.Contains(out, row("compacted-into", "handoff.8a9b0001")) {
		t.Errorf("inbound compacted-into edge missing:\n%s", out)
	}

	code, out, stderr = runHome(t, home, "", "links", "handoff.8a9b0001")
	mustCode(t, ExitOK, code, out, stderr, "links compacting handoff")
	if !strings.Contains(out, row("compacted-into", "knowledge.9b0c0001")) {
		t.Errorf("outbound compacted-into edge on the handoff missing:\n%s", out)
	}
}

// TestLinksRelated: --related adds the same-scope and shared-tags blocks; the
// strongest tag overlap sorts first.
func TestLinksRelated(t *testing.T) {
	home := linksHome(t)
	code, out, stderr := runHome(t, home, "", "links", "lesson.1a2b0001", "--related")
	mustCode(t, ExitOK, code, out, stderr, "links --related")
	if !strings.Contains(out, "RELATED (--related)") || !strings.Contains(out, "same-scope") || !strings.Contains(out, "shared-tags") {
		t.Fatalf("related section missing:\n%s", out)
	}
	if !strings.Contains(out, "lesson.5e6f0001") || !strings.Contains(out, "(overlap 0.67)") {
		t.Errorf("shared-tags ranking wrong (want 5e6f0001 overlap 0.67):\n%s", out)
	}
	// Without --related the section is absent.
	code, out, stderr = runHome(t, home, "", "links", "lesson.1a2b0001")
	mustCode(t, ExitOK, code, out, stderr, "links plain")
	if strings.Contains(out, "RELATED") {
		t.Errorf("related section shown without --related:\n%s", out)
	}
}

// TestLinksPrefixResolution: a unique >=6-char prefix resolves; a short query
// and an ambiguous prefix are usage errors; an unknown id exits 1.
func TestLinksPrefixResolution(t *testing.T) {
	home := linksHome(t)
	code, out, stderr := runHome(t, home, "", "links", "decision.2b3c00")
	mustCode(t, ExitOK, code, out, stderr, "unique prefix")
	if !strings.Contains(out, "record  decision.2b3c0001") {
		t.Errorf("prefix resolved to the wrong record:\n%s", out)
	}

	code, out, stderr = runHome(t, home, "", "links", "les")
	mustCode(t, ExitGuard, code, out, stderr, "short prefix")
	if out != "" {
		t.Errorf("short prefix wrote to stdout: %q", out)
	}

	code, out, stderr = runHome(t, home, "", "links", "lesson.4d5e")
	mustCode(t, ExitGuard, code, out, stderr, "ambiguous prefix")
	if out != "" {
		t.Errorf("ambiguous prefix wrote to stdout: %q", out)
	}
	for _, want := range []string{"lesson.4d5e0001", "lesson.4d5e0002"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("candidates missing %q:\n%s", want, stderr)
		}
	}

	code, out, stderr = runHome(t, home, "", "links", "lesson.ffff9999")
	mustCode(t, ExitError, code, out, stderr, "unknown id")
	if out != "" || !strings.Contains(stderr, "ไม่พบ") {
		t.Errorf("unknown id: stdout %q stderr %q", out, stderr)
	}
}

// TestLinksJSON: the --json document carries the record block, both edge
// lists with exists/file/other_refs, the dangling flag and — only with
// --related — the related block. Output stays a single document on stdout.
func TestLinksJSON(t *testing.T) {
	home := linksHome(t)
	code, out, stderr := runHome(t, home, "", "links", "knowledge.3c4d0001", "--json", "--related")
	mustCode(t, ExitOK, code, out, stderr, "links --json")

	var doc struct {
		Record struct {
			ID     string `json:"id"`
			Layer  string `json:"layer"`
			Scope  string `json:"scope"`
			Status string `json:"status"`
			Title  string `json:"title"`
			File   string `json:"file"`
		} `json:"record"`
		Outbound []struct {
			Kind      string `json:"kind"`
			ID        string `json:"id"`
			Exists    bool   `json:"exists"`
			File      string `json:"file"`
			Line      int    `json:"line"`
			More      int    `json:"more"`
			Depth     int    `json:"depth"`
			OtherRefs int    `json:"other_refs"`
		} `json:"outbound"`
		Inbound []struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"inbound"`
		Related *struct {
			SameScope  []string `json:"same_scope"`
			SharedTags []struct {
				ID      string  `json:"id"`
				Overlap float64 `json:"overlap"`
			} `json:"shared_tags"`
		} `json:"related"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not a single JSON document: %v\n%s", err, out)
	}
	if doc.Record.ID != "knowledge.3c4d0001" || doc.Record.Layer != "knowledge" || doc.Record.File == "" {
		t.Errorf("record block wrong: %+v", doc.Record)
	}
	var dup, dangling bool
	for _, r := range doc.Outbound {
		switch r.ID {
		case "decision.2b3c0001":
			dup = r.Kind == "body-ref" && r.Exists && r.Line == 2 && r.More == 1 && r.File != "" && r.OtherRefs > 0
		case "lesson.9d9d9999":
			dangling = r.Kind == "body-ref" && !r.Exists
		}
	}
	if !dup || !dangling {
		t.Errorf("outbound rows wrong (dup=%v dangling=%v): %+v", dup, dangling, doc.Outbound)
	}
	if len(doc.Inbound) != 0 {
		t.Errorf("knowledge.3c4d0001 should have no inbound edges: %+v", doc.Inbound)
	}
	if doc.Related == nil || len(doc.Related.SameScope) == 0 || len(doc.Related.SharedTags) == 0 {
		t.Errorf("related block missing or empty: %+v", doc.Related)
	}

	// Without --related the key is absent, and a usage error keeps stdout
	// pure (single-document contract).
	code, out, stderr = runHome(t, home, "", "links", "knowledge.3c4d0001", "--json")
	mustCode(t, ExitOK, code, out, stderr, "links --json plain")
	if strings.Contains(out, "related") {
		t.Errorf("related present without --related:\n%s", out)
	}
}

// TestLinksIsReadOnly: two runs render byte-identical output and never touch
// the record files (no last_used bump, no log, no git).
func TestLinksIsReadOnly(t *testing.T) {
	home := fixtureHome(t)
	writeFile(t, home, "lessons/links-probe-lesson.a1b2c3d4.md",
		"---\nid: lesson.a1b2c3d4\ntitle: read-only probe\nlayer: lesson\nscope: global\ntags: [probe]\n"+
			"created: 2026-01-01\nlast_used: 2026-01-01\nexpires: none\nstatus: active\nsensitivity: internal\n---\n\n"+
			"Probe body.\n")
	git(t, home, "add", "-A")
	git(t, home, "commit", "-q", "-m", "probe baseline")
	c1, out1, _ := runHome(t, home, "", "links", "lesson.a1b2c3d4")
	c2, out2, _ := runHome(t, home, "", "links", "lesson.a1b2c3d4")
	mustCode(t, ExitOK, c1, out1, "", "first run")
	mustCode(t, ExitOK, c2, out2, "", "second run")
	if out1 != out2 {
		t.Errorf("output not stable across runs:\n%s\n---\n%s", out1, out2)
	}
	if strings.Contains(out1, "last_used=2026-11") {
		t.Errorf("links bumped last_used:\n%s", out1)
	}
	if status := git(t, home, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("links dirtied the repo:\n%s", status)
	}
}
