// Behaviour tests for `mem graph`: every assertion runs the real router
// against the synthetic testdata/links/home fixture (never the real
// ~/my-memory), so the JSON schema, the offline HTML guarantee and the
// filters are the process contract.
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// graphJSON mirrors the stable `--format json` schema (and the document
// embedded in the HTML viewer).
type graphJSON struct {
	GeneratedAt string `json:"generated_at"`
	Counts      struct {
		Records  int `json:"records"`
		Edges    int `json:"edges"`
		Dangling int `json:"dangling"`
	} `json:"counts"`
	Nodes []struct {
		ID       string  `json:"id"`
		Layer    string  `json:"layer"`
		Scope    string  `json:"scope"`
		Status   string  `json:"status"`
		Title    string  `json:"title"`
		File     string  `json:"file"`
		Decay    float64 `json:"decay"`
		Degree   int     `json:"degree"`
		Dangling bool    `json:"dangling"`
	} `json:"nodes"`
	Edges []struct {
		From string `json:"from"`
		To   string `json:"to"`
		Kind string `json:"kind"`
	} `json:"edges"`
}

// runGraphJSON runs `mem graph --format json -o -` and parses the document.
func runGraphJSON(t *testing.T, home string, argv ...string) graphJSON {
	t.Helper()
	code, out, stderr := runHome(t, home, "", append([]string{"graph", "--format", "json", "-o", "-"}, argv...)...)
	mustCode(t, ExitOK, code, out, stderr, "graph --format json")
	var doc graphJSON
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not a single JSON document: %v\n%s", err, out)
	}
	return doc
}

func graphHasEdge(doc graphJSON, from, to, kind string) bool {
	for _, e := range doc.Edges {
		if e.From == from && e.To == to && e.Kind == kind {
			return true
		}
	}
	return false
}

func graphEdgesBetween(doc graphJSON, a, b string) int {
	n := 0
	for _, e := range doc.Edges {
		if (e.From == a && e.To == b) || (e.From == b && e.To == a) {
			n++
		}
	}
	return n
}

func graphKinds(doc graphJSON, prefix string) int {
	n := 0
	for _, e := range doc.Edges {
		if strings.HasPrefix(e.Kind, prefix) {
			n++
		}
	}
	return n
}

// TestGraphJSONSchemaAndDedup: the document carries every node (sorted by
// id), every deduplicated edge kind of the fixture, the dangling ghost and
// corpus-derived stamps — and two runs are byte-identical.
func TestGraphJSONSchemaAndDedup(t *testing.T) {
	home := linksHome(t)
	_, out1, _ := runHome(t, home, "", "graph", "--format", "json", "-o", "-")
	_, out2, _ := runHome(t, home, "", "graph", "--format", "json", "-o", "-")
	if out1 != out2 {
		t.Fatalf("graph JSON not deterministic across runs")
	}
	doc := runGraphJSON(t, home)

	if doc.Counts.Records != 14 {
		t.Errorf("counts.records = %d, want 14 (links fixture)", doc.Counts.Records)
	}
	if doc.Counts.Dangling != 1 {
		t.Errorf("counts.dangling = %d, want 1 (lesson.9d9d9999)", doc.Counts.Dangling)
	}
	if doc.GeneratedAt == "" || !strings.HasSuffix(doc.GeneratedAt, "T00:00:00Z") {
		t.Errorf("generated_at not an RFC3339 corpus-derived stamp: %q", doc.GeneratedAt)
	}

	// Nodes sorted by id; the ghost carries only id/degree/dangling.
	for i := 1; i < len(doc.Nodes); i++ {
		if doc.Nodes[i-1].ID >= doc.Nodes[i].ID {
			t.Fatalf("nodes not sorted by id: %s >= %s", doc.Nodes[i-1].ID, doc.Nodes[i].ID)
		}
	}
	var ghost, real bool
	for _, n := range doc.Nodes {
		switch n.ID {
		case "lesson.9d9d9999":
			ghost = n.Dangling && n.Layer == "" && n.File == "" && n.Degree > 0
		case "lesson.1a2b0001":
			real = n.Layer == "lesson" && n.Scope == "global" && n.Status == "active" &&
				n.Title != "" && n.File != "" && n.Decay > 0 && n.Dangling == false && n.Degree > 0
		}
	}
	if !ghost {
		t.Errorf("dangling ghost node wrong: %+v", doc.Nodes)
	}
	if !real {
		t.Errorf("real node fields wrong: %+v", doc.Nodes)
	}

	// Edges sorted by (from, to, kind).
	for i := 1; i < len(doc.Edges); i++ {
		a, b := doc.Edges[i-1], doc.Edges[i]
		if a.From > b.From || (a.From == b.From && a.To > b.To) ||
			(a.From == b.From && a.To == b.To && a.Kind >= b.Kind) {
			t.Fatalf("edges not sorted: %+v then %+v", a, b)
		}
	}

	// One edge per (from, to, kind): the dual supersedes/superseded_by
	// front-matter pair and the repeated body marker must each collapse.
	for _, want := range []struct{ from, to, kind string }{
		{"lesson.1a2b0001", "lesson.1a2b0002", "supersedes"},
		{"lesson.1a2b0002", "lesson.1a2b0003", "supersedes"},
		// superseded-old's `superseded_by` canonicalises onto this edge.
		{"lesson.1a2b0001", "lesson.0a1b0001", "supersedes"},
		{"decision.2b3c0001", "lesson.1a2b0001", "evidence"},
		{"knowledge.3c4d0001", "decision.2b3c0001", "body-ref"},
		{"knowledge.3c4d0001", "lesson.9d9d9999", "body-ref"},
		{"rule.6f7a0001", "lesson.7a8b0001", "promoted-from"},
		{"handoff.8a9b0001", "knowledge.9b0c0001", "compacted-into"},
	} {
		if !graphHasEdge(doc, want.from, want.to, want.kind) {
			t.Errorf("edge missing: %s -> %s (%s)", want.from, want.to, want.kind)
		}
	}
	if n := graphEdgesBetween(doc, "lesson.1a2b0001", "lesson.0a1b0001"); n != 1 {
		t.Errorf("supersedes/superseded_by pair produced %d edges, want 1", n)
	}
	if n := graphEdgesBetween(doc, "knowledge.3c4d0001", "decision.2b3c0001"); n != 1 {
		t.Errorf("repeated body marker produced %d edges, want 1", n)
	}
	if doc.Counts.Edges != 8 {
		t.Errorf("counts.edges = %d, want 8", doc.Counts.Edges)
	}
	if graphKinds(doc, "related") != 0 {
		t.Errorf("related edges present without --related")
	}
}

// TestGraphRelated: --related adds implicit same-scope and shared-tag edges
// on top of the explicit ones.
func TestGraphRelated(t *testing.T) {
	home := linksHome(t)
	doc := runGraphJSON(t, home, "--related")
	if scope, tags := graphKinds(doc, "related-scope"), graphKinds(doc, "related-tags"); scope == 0 || tags == 0 {
		t.Errorf("--related missing implicit edges (scope=%d tags=%d)", scope, tags)
	}
	if doc.Counts.Edges <= 8 {
		t.Errorf("--related did not grow the edge count: %d", doc.Counts.Edges)
	}
	// Every related-scope edge connects two records of the same scope.
	scopes := map[string]string{}
	for _, n := range doc.Nodes {
		scopes[n.ID] = n.Scope
	}
	for _, e := range doc.Edges {
		if e.Kind != "related-scope" {
			continue
		}
		if scopes[e.From] != scopes[e.To] {
			t.Errorf("related-scope edge crosses scopes (%s vs %s): %+v",
				scopes[e.From], scopes[e.To], e)
		}
	}
}

// TestGraphDanglingToggle: --include-dangling=false drops the ghost node,
// its edge and the count.
func TestGraphDanglingToggle(t *testing.T) {
	home := linksHome(t)
	doc := runGraphJSON(t, home, "--include-dangling=false")
	if doc.Counts.Dangling != 0 {
		t.Errorf("counts.dangling = %d, want 0", doc.Counts.Dangling)
	}
	for _, n := range doc.Nodes {
		if n.Dangling {
			t.Errorf("ghost node present: %+v", n)
		}
	}
	if graphEdgesBetween(doc, "knowledge.3c4d0001", "lesson.9d9d9999") != 0 {
		t.Errorf("edge to the dropped ghost still present")
	}
}

// TestGraphFilters: --layer keeps only those layers (and edges among them);
// --scope keeps exact + global + repo:* exactly like `mem context`.
func TestGraphFilters(t *testing.T) {
	home := linksHome(t)

	doc := runGraphJSON(t, home, "--layer", "lesson,decision")
	if doc.Counts.Records == 0 || doc.Counts.Records >= 14 {
		t.Fatalf("layer filter kept %d records, want a strict subset", doc.Counts.Records)
	}
	for _, n := range doc.Nodes {
		if n.Layer != "lesson" && n.Layer != "decision" {
			t.Errorf("layer filter leaked %s (%s)", n.ID, n.Layer)
		}
	}
	if graphHasEdge(doc, "rule.6f7a0001", "lesson.7a8b0001", "promoted-from") {
		t.Errorf("edge survives when its endpoint is filtered out")
	}

	// project:Gamma: the global records stay, project:Gamma stays, a scope
	// that matches nothing still keeps every global record.
	doc = runGraphJSON(t, home, "--scope", "project:Gamma")
	if !hasGraphID(doc, "lesson.5e6f0002") {
		t.Errorf("exact scope record dropped")
	}
	if !hasGraphID(doc, "lesson.1a2b0001") {
		t.Errorf("global record dropped by a scoped view")
	}
	doc = runGraphJSON(t, home, "--scope", "project:NoSuch")
	if hasGraphID(doc, "lesson.5e6f0002") {
		t.Errorf("other project scope kept by a scoped view")
	}
	if !hasGraphID(doc, "lesson.1a2b0001") {
		t.Errorf("global record dropped by a scoped view")
	}
}

func hasGraphID(doc graphJSON, id string) bool {
	for _, n := range doc.Nodes {
		if n.ID == id {
			return true
		}
	}
	return false
}

// TestGraphEmptyGraph: a filter that matches everything away still produces
// a valid, zero-count document in both formats.
func TestGraphEmptyGraph(t *testing.T) {
	home := linksHome(t)
	doc := runGraphJSON(t, home, "--layer", "nosuchlayer")
	if doc.Counts.Records != 0 || doc.Counts.Edges != 0 || doc.Counts.Dangling != 0 ||
		len(doc.Nodes) != 0 || len(doc.Edges) != 0 {
		t.Errorf("empty graph not empty: %+v", doc.Counts)
	}

	code, out, stderr := runHome(t, home, "", "graph", "--layer", "nosuchlayer", "-o", "-")
	mustCode(t, ExitOK, code, out, stderr, "graph html empty")
	for _, want := range []string{"<!doctype html>", "records", "(no nodes)"} {
		if !strings.Contains(out, want) {
			t.Errorf("empty HTML missing %q", want)
		}
	}
}

// TestGraphHTMLOffline: the viewer is one self-contained document — embedded
// JSON payload, legend, private-data warning, text fallback, and not a single
// external URL.
func TestGraphHTMLOffline(t *testing.T) {
	home := linksHome(t)
	target := filepath.Join(t.TempDir(), "g.html")
	code, out, stderr := runHome(t, home, "", "graph", "-o", target)
	mustCode(t, ExitOK, code, out, stderr, "graph html")
	if !strings.Contains(out, "wrote "+target) {
		t.Errorf("no write confirmation on stdout: %q", out)
	}
	html, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read %s: %v", target, err)
	}
	doc := string(html)

	for _, want := range []string{
		"<!doctype html>",
		`<script type="application/json" id="graph-data">`,
		"do not commit or share",
		"aria-label",
		"<details",
		"dangling",
		"layer-toggle",
		"lesson.1a2b0001", // top-node fallback + payload
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("HTML viewer missing %q", want)
		}
	}
	// Legend labels for the layers present in the fixture.
	for _, layer := range []string{"rule", "lesson", "decision", "handoff", "knowledge"} {
		if !strings.Contains(doc, ">"+layer+" <") {
			t.Errorf("legend label missing: %s", layer)
		}
	}
	// The offline guarantee: no external requests of any kind.
	for _, banned := range []string{"http://", "https://", "//cdn", "src=\"http"} {
		if strings.Contains(doc, banned) {
			t.Errorf("HTML contains external reference %q", banned)
		}
	}
	if n1, n2 := strings.Count(doc, "<script"), strings.Count(doc, "</script>"); n1 != n2 || n1 < 2 {
		t.Errorf("unbalanced script tags: %d open vs %d close", n1, n2)
	}
	if size := len(html); size <= 20000 {
		t.Errorf("viewer suspiciously small: %d bytes (want > 20000)", size)
	}
	// The payload must parse back out of the document.
	start := strings.Index(doc, `id="graph-data">`) + len(`id="graph-data">`)
	end := strings.Index(doc[start:], "</script>")
	var embedded graphJSON
	if err := json.Unmarshal([]byte(doc[start:start+end]), &embedded); err != nil {
		t.Fatalf("embedded payload is not valid JSON: %v", err)
	}
	if embedded.Counts.Records != 14 || embedded.Counts.Dangling != 1 {
		t.Errorf("embedded payload counts wrong: %+v", embedded.Counts)
	}
}

// TestGraphDefaultOutputFile: no -o means ./mem-graph.html (or .json for the
// json format) relative to the working directory.
func TestGraphDefaultOutputFile(t *testing.T) {
	home := linksHome(t)
	dir := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { os.Chdir(oldWd) }()

	code, out, stderr := runHome(t, home, "", "graph")
	mustCode(t, ExitOK, code, out, stderr, "graph default html")
	if _, err := os.Stat(filepath.Join(dir, "mem-graph.html")); err != nil {
		t.Errorf("default mem-graph.html not created: %v", err)
	}
	code, out, stderr = runHome(t, home, "", "graph", "--format", "json")
	mustCode(t, ExitOK, code, out, stderr, "graph default json")
	if _, err := os.Stat(filepath.Join(dir, "mem-graph.json")); err != nil {
		t.Errorf("default mem-graph.json not created: %v", err)
	}
	if !strings.Contains(out, "wrote mem-graph.json") {
		t.Errorf("json confirmation wrong: %q", out)
	}
}

// TestGraphUsage: a bad format, a positional argument and an unknown flag are
// usage errors (exit 2) that keep stdout pure.
func TestGraphUsage(t *testing.T) {
	home := linksHome(t)
	for _, argv := range [][]string{
		{"graph", "--format", "xml"},
		{"graph", "lesson.1a2b0001"},
		{"graph", "--no-such-flag"},
	} {
		code, out, stderr := runHome(t, home, "", argv...)
		mustCode(t, ExitGuard, code, out, stderr, strings.Join(argv, " "))
		if out != "" {
			t.Errorf("%s wrote to stdout: %q", strings.Join(argv, " "), out)
		}
	}
}

// TestGraphEngineRepoWarning: writing inside a source repo (go.mod +
// .github/workflows) warns loudly on stderr but still writes the file.
func TestGraphEngineRepoWarning(t *testing.T) {
	home := linksHome(t)
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.invalid/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(repo, "mem-graph.html")
	code, out, stderr := runHome(t, home, "", "graph", "-o", target)
	mustCode(t, ExitOK, code, out, stderr, "graph inside source repo")
	if !strings.Contains(stderr, "WARNING") || !strings.Contains(stderr, "private memory metadata") {
		t.Errorf("engine-repo warning missing: %q", stderr)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("warning refused the write: %v", err)
	}
}

// TestGraphIsReadOnly: the graph export never touches the memory home — no
// last_used bump, no log event, no git activity.
func TestGraphIsReadOnly(t *testing.T) {
	home := fixtureHome(t)
	writeFile(t, home, "lessons/graph-probe-lesson.b1c2d3e4.md",
		"---\nid: lesson.b1c2d3e4\ntitle: graph read-only probe\nlayer: lesson\nscope: global\ntags: [probe]\n"+
			"created: 2026-01-01\nlast_used: 2026-01-01\nexpires: none\nstatus: active\nsensitivity: internal\n---\n\n"+
			"Probe body.\n")
	git(t, home, "add", "-A")
	git(t, home, "commit", "-q", "-m", "probe baseline")

	out := filepath.Join(t.TempDir(), "probe.html")
	c1, _, _ := runHome(t, home, "", "graph", "-o", out)
	c2, _, _ := runHome(t, home, "", "graph", "-o", out)
	mustCode(t, ExitOK, c1, "", "", "first run")
	mustCode(t, ExitOK, c2, "", "", "second run")
	if status := git(t, home, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("graph dirtied the memory repo:\n%s", status)
	}
}
