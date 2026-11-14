// Behaviour tests for the --mentions edge opt-in (KB P1): bare id
// references in bodies become `mention` edges in `mem links` and `mem
// graph` — off by default, additive when asked. The synthetic mentions
// fixture record (testdata/links/home/lessons/mentions-holder-lesson.6b6c0001.md)
// covers duplicates collapsing, bracketed markers staying body-refs,
// self-references dropping and dangling mentions.
package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestLinksWithoutMentionsByteIdentical: default links output never changes.
func TestLinksWithoutMentionsByteIdentical(t *testing.T) {
	home := linksHome(t)
	codeA, outA, _ := runHome(t, home, "", "links", "lesson.6b6c0001")
	codeB, outB, _ := runHome(t, home, "", "links", "lesson.6b6c0001")
	mustCode(t, ExitOK, codeA, outA, outB, "links default run 1")
	mustCode(t, ExitOK, codeB, outB, outA, "links default run 2")
	if outA != outB {
		t.Error("links output not deterministic between runs")
	}
	if strings.Contains(outA, "  mention ") {
		t.Errorf("mention edges present without --mentions:\n%s", outA)
	}
	// The bracketed [id: …] markers show as body-ref, and the bare ids
	// stay invisible by default.
	if !strings.Contains(outA, row("body-ref", "decision.2b3c0001")) {
		t.Errorf("body-ref marker missing:\n%s", outA)
	}
}

func TestLinksMentions(t *testing.T) {
	home := linksHome(t)
	code, out, stderr := runHome(t, home, "", "links", "lesson.6b6c0001", "--mentions")
	mustCode(t, ExitOK, code, out, stderr, "links --mentions")

	// Mention rows: one per target with line number; duplicates collapse.
	for _, want := range []string{
		"(line 2, +2 more)", // lesson.1a2b0001 twice on body line 2, once more below
		"(line 7)",          // lesson.5e6f0002
		"DANGLING (not found)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	for _, mention := range []string{
		row("mention", "lesson.1a2b0001"),
		row("mention", "lesson.5e6f0002"),
		row("mention", "lesson.9e9e1111"),
	} {
		if !strings.Contains(out, mention) {
			t.Errorf("mention row missing: %q\n%s", mention, out)
		}
	}
	if !strings.Contains(out, "OUTBOUND (this → others)") {
		t.Errorf("outbound section missing:\n%s", out)
	}
	// Self-mention and the bracketed marker must not produce mention edges.
	for _, no := range []string{
		row("mention", "lesson.6b6c0001"),
		row("mention", "decision.2b3c0001"),
	} {
		if strings.Contains(out, no) {
			t.Errorf("unexpected mention row %q:\n%s", no, out)
		}
	}
	// Inbound: the mentioned record sees the inverted mention edge.
	code, outIn, errb := runHome(t, home, "", "links", "lesson.1a2b0001", "--mentions")
	mustCode(t, ExitOK, code, outIn, errb, "links --mentions inbound")
	if !strings.Contains(outIn, "INBOUND (others → this)") ||
		!strings.Contains(outIn, row("mention", "lesson.6b6c0001")) {
		t.Errorf("inbound mention missing on the target:\n%s", outIn)
	}

	// Default run never shows the mention kind.
	code, outD, _ := runHome(t, home, "", "links", "lesson.1a2b0001")
	mustCode(t, ExitOK, code, outD, "", "links default again")
	if strings.Contains(outD, "  mention ") {
		t.Errorf("default output grew mention rows:\n%s", outD)
	}
	if strings.Contains(out, "  mention ") && outD == out {
		t.Error("--mentions changed nothing (edge extraction ineffective)")
	}
}

func TestLinksMentionsJSON(t *testing.T) {
	home := linksHome(t)
	code, out, stderr := runHome(t, home, "", "links", "lesson.6b6c0001", "--mentions", "--json")
	mustCode(t, ExitOK, code, out, stderr, "links --mentions --json")

	var payload struct {
		Outbound []struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
			Line int    `json:"line"`
			More int    `json:"more"`
		} `json:"outbound"`
		Inbound []struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"inbound"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out)
	}
	mentions := map[string]bool{}
	for _, r := range payload.Outbound {
		if r.Kind == "mention" {
			mentions[r.ID] = true
			if r.Line == 0 {
				t.Errorf("mention row for %s lacks line number", r.ID)
			}
		}
	}
	if !mentions["lesson.1a2b0001"] || !mentions["lesson.5e6f0002"] || !mentions["lesson.9e9e1111"] {
		t.Errorf("mention targets wrong: %v", mentions)
	}
	if mentions["lesson.6b6c0001"] || mentions["decision.2b3c0001"] {
		t.Errorf("self-mention or bracketed marker leaked: %v", mentions)
	}
	code, outIn, stderrIn := runHome(t, home, "", "links", "lesson.1a2b0001", "--mentions", "--json")
	mustCode(t, ExitOK, code, outIn, stderrIn, "links --mentions --json (target)")
	if err := json.Unmarshal([]byte(outIn), &payload); err != nil {
		t.Fatalf("bad target JSON: %v\n%s", err, outIn)
	}
	for _, r := range payload.Inbound {
		if r.Kind == "mention" && r.ID == "lesson.6b6c0001" {
			return // found
		}
	}
	t.Errorf("inbound mention row missing from JSON:\n%s", outIn)
}

// TestGraphMentionsDefaultUnchanged: graph payload without --mentions is
// byte-identical between runs and carries no mention kind; --mentions adds
// exactly the three fixture mention edges (one to a dangling ghost node).
func TestGraphMentionsDefaultUnchanged(t *testing.T) {
	home := linksHome(t)
	codeA, outA, _ := runHome(t, home, "", "graph", "--format", "json", "-o", "-")
	codeB, outB, _ := runHome(t, home, "", "graph", "--format", "json", "-o", "-")
	mustCode(t, ExitOK, codeA, outA, outB, "graph default run 1")
	mustCode(t, ExitOK, codeB, outB, outA, "graph default run 2")
	if outA != outB {
		t.Error("graph JSON not byte-stable")
	}
	def := runGraphJSON(t, home)
	for _, e := range def.Edges {
		if e.Kind == "mention" {
			t.Errorf("default payload grew a mention edge: %+v", e)
		}
	}

	p := runGraphJSON(t, home, "--mentions")
	mentionEdges := 0
	for _, e := range p.Edges {
		if e.Kind == "mention" {
			mentionEdges++
		}
	}
	// 1a2b0001, 5e6f0002 and the dangling 9e9e1111 from the mentions
	// holder, plus rule.6f7a0001 mentioned bare in the promoted lesson's
	// body. The self-mention and the bracketed marker must contribute none.
	if mentionEdges != 4 {
		t.Errorf("mention edges = %d, want 4 (doc: %d records / %d edges)",
			mentionEdges, p.Counts.Records, p.Counts.Edges)
	}
	for _, want := range [][3]string{
		{"lesson.6b6c0001", "lesson.1a2b0001"},
		{"lesson.6b6c0001", "lesson.5e6f0002"},
		{"lesson.6b6c0001", "lesson.9e9e1111"},
		{"lesson.7a8b0001", "rule.6f7a0001"},
	} {
		if !graphHasEdge(p, want[0], want[1], "mention") {
			t.Errorf("mention edge %s -> %s missing", want[0], want[1])
		}
	}
	if graphHasEdge(p, "lesson.6b6c0001", "lesson.6b6c0001", "mention") ||
		graphHasEdge(p, "lesson.6b6c0001", "decision.2b3c0001", "mention") {
		t.Error("self-mention or bracketed marker produced a mention edge")
	}
	// The dangling mention target appears as one extra ghost node.
	ghost := false
	for _, n := range p.Nodes {
		if n.ID == "lesson.9e9e1111" && n.Dangling {
			ghost = true
		}
	}
	if !ghost {
		t.Errorf("dangling mention target lesson.9e9e1111 not a ghost node")
	}
	if p.Counts.Dangling != def.Counts.Dangling+1 {
		t.Errorf("dangling count = %d, want default %d + 1", p.Counts.Dangling, def.Counts.Dangling)
	}
	// Default counts untouched by the mentions fixture logic.
	if p.Counts.Records != def.Counts.Records {
		t.Errorf("record count changed with --mentions: %d vs %d", p.Counts.Records, def.Counts.Records)
	}
}
