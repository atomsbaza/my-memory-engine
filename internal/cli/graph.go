// `mem graph` — the read-only graph exporter: how the whole corpus connects,
// as one self-contained artifact the owner can open offline. Two formats:
// `--format json` (stable schema for tooling) and the default HTML viewer
// (force-directed canvas, no external requests, safe from file://).
// Like `mem links` it never writes to the memory home: no touch, no log
// event, no git. The output file is the only side effect.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// graphNode is the JSON shape of one node (spec §JSON schema). Dangling
// ghost nodes carry only the id, degree and the dangling flag.
type graphNode struct {
	ID       string  `json:"id"`
	Layer    string  `json:"layer"`
	Scope    string  `json:"scope"`
	Status   string  `json:"status"`
	Title    string  `json:"title"`
	File     string  `json:"file"`
	Decay    float64 `json:"decay"`
	Degree   int     `json:"degree"`
	Dangling bool    `json:"dangling"`
}

// graphEdge is one deduplicated directed edge.
type graphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

// graphCounts summarises the payload for humans and for the viewer header.
type graphCounts struct {
	Records  int `json:"records"`
	Edges    int `json:"edges"`
	Dangling int `json:"dangling"`
}

// graphPayload is the single JSON document of `--format json` and the
// document embedded in the HTML viewer.
type graphPayload struct {
	GeneratedAt string      `json:"generated_at"`
	Counts      graphCounts `json:"counts"`
	Nodes       []graphNode `json:"nodes"`
	Edges       []graphEdge `json:"edges"`
}

// graphOptions carries the filters the payload builder applies.
type graphOptions struct {
	scope           string
	layers          []string
	related         bool
	includeDangling bool
}

// cmdGraph implements
// `mem graph [--format html|json] [-o FILE] [--scope S] [--layer L[,L...]] [--related] [--include-dangling]`.
func cmdGraph(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	format := fs.String("format", "html", "รูปแบบเอาต์พุต: html (viewer แบบ self-contained) | json")
	dest := fs.String("o", "", "ไฟล์ปลายทาง (default: ./mem-graph.html หรือ ./mem-graph.json; - = stdout)")
	scope := fs.String("scope", "", "กรอง scope (exact + global + repo:* เหมือน mem context)")
	layer := fs.String("layer", "", "กรอง layer คั่นด้วยจุลภาค (ซ้ำได้)")
	related := fs.Bool("related", false, "เพิ่ม implicit edges: same scope + shared tags (แสดงเป็นเส้นจาง)")
	includeDangling := fs.Bool("include-dangling", true, "แสดง ghost node ของ id ที่ถูกอ้างแต่ไม่มี record (เส้นประ)")
	args, code, stop := e.parseArgs(c, fs, argv)
	if stop {
		return code
	}
	if len(args) != 0 {
		return e.usagef("graph ไม่รับ positional argument: %s", args[0])
	}
	if *format != "html" && *format != "json" {
		return e.usagef("--format ต้องเป็น html หรือ json ไม่ใช่ %s", *format)
	}

	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	recs, err := e.scan(root)
	if err != nil {
		return e.fail(err)
	}

	var layers []string
	for _, l := range strings.Split(*layer, ",") {
		if l = strings.TrimSpace(l); l != "" {
			layers = append(layers, l)
		}
	}
	payload := buildGraphPayload(recs, graphOptions{
		scope:           *scope,
		layers:          layers,
		related:         *related,
		includeDangling: *includeDangling,
	})

	target := *dest
	if target == "" {
		target = "mem-graph." + *format
	}

	if target == "-" {
		if err := writeGraph(e.Stdout, *format, payload); err != nil {
			return e.fail(err)
		}
		return ExitOK
	}
	warnIfInsideEngineRepo(e, target)
	if err := writeFileTarget(target, *format, payload); err != nil {
		return e.fail(err)
	}
	fmt.Fprintf(e.Stdout, "wrote %s  (%d records, %d edges, %d dangling)\n",
		target, payload.Counts.Records, payload.Counts.Edges, payload.Counts.Dangling)
	return ExitOK
}

// buildGraphPayload turns one scan into the deterministic graph document:
// nodes sorted by id, edges deduplicated per (from, to, kind) and sorted by
// (from, to, kind). The generated_at stamp is derived from the corpus (the
// latest last_used/created date), never from the wall clock, so two runs over
// the same corpus are byte-identical.
func buildGraphPayload(recs []store.Record, o graphOptions) *graphPayload {
	exists := make(map[string]bool, len(recs))
	for _, r := range recs {
		exists[r.ID] = true
	}

	layerSet := map[string]bool{}
	for _, l := range o.layers {
		layerSet[l] = true
	}
	kept := make([]store.Record, 0, len(recs))
	for _, r := range recs {
		if o.scope != "" && r.Scope != o.scope && r.Scope != "global" && !strings.HasPrefix(r.Scope, "repo:") {
			continue
		}
		if len(layerSet) > 0 && !layerSet[r.Layer] {
			continue
		}
		kept = append(kept, r)
	}
	keptIDs := make(map[string]bool, len(kept))
	for _, r := range kept {
		keptIDs[r.ID] = true
	}

	type edgeKey struct{ from, to, kind string }
	edgeSet := map[edgeKey]bool{}
	dangling := map[string]bool{}

	addEdge := func(from, to, kind string) {
		if keptIDs[from] && keptIDs[to] {
			edgeSet[edgeKey{from, to, kind}] = true
		}
	}
	for _, r := range kept {
		for _, ref := range linksOutbound(r) {
			if !exists[ref.ID] {
				// Referenced but missing from the whole corpus (not merely
				// filtered out): a dangling ghost node plus its edge.
				if o.includeDangling {
					dangling[ref.ID] = true
					edgeSet[edgeKey{r.ID, ref.ID, graphKindOf(ref.Kind)}] = true
				}
				continue
			}
			// "B superseded_by A" is the same fact as "A supersedes B":
			// canonicalise onto the supersedes direction so dual
			// front-matter fields collapse into one edge.
			if ref.Kind == "superseded-by" {
				addEdge(ref.ID, r.ID, "supersedes")
			} else {
				addEdge(r.ID, ref.ID, graphKindOf(ref.Kind))
			}
		}
	}
	if o.related {
		for _, r := range kept {
			rel := relatedRecords(kept, r)
			for _, id := range rel.SameScope {
				addEdge(r.ID, id, "related-scope")
			}
			for _, s := range rel.SharedTags {
				addEdge(r.ID, s.ID, "related-tags")
			}
		}
	}

	nodes := make([]graphNode, 0, len(kept)+len(dangling))
	for _, r := range kept {
		nodes = append(nodes, graphNode{
			ID: r.ID, Layer: r.Layer, Scope: r.Scope, Status: r.Status,
			Title: r.Title, File: r.Rel, Decay: r.Decay,
		})
	}
	for id := range dangling {
		nodes = append(nodes, graphNode{ID: id, Dangling: true})
	}

	edges := make([]graphEdge, 0, len(edgeSet))
	degree := map[string]int{}
	for k := range edgeSet {
		edges = append(edges, graphEdge{From: k.from, To: k.to, Kind: k.kind})
		degree[k.from]++
		degree[k.to]++
	}
	for i := range nodes {
		nodes[i].Degree = degree[nodes[i].ID]
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		if edges[i].To != edges[j].To {
			return edges[i].To < edges[j].To
		}
		return edges[i].Kind < edges[j].Kind
	})

	return &graphPayload{
		GeneratedAt: graphStamp(recs),
		Counts: graphCounts{
			Records:  len(kept),
			Edges:    len(edges),
			Dangling: len(dangling),
		},
		Nodes: nodes,
		Edges: edges,
	}
}

// graphKindOf maps a links kind onto the graph edge kind vocabulary.
func graphKindOf(kind string) string {
	if kind == "superseded-by" {
		return "supersedes"
	}
	return kind // supersedes, evidence, body-ref, promoted-from, compacted-into
}

// graphStamp derives the deterministic "generated_at" stamp: the latest
// last_used/created date in the corpus, formatted RFC3339. It changes only
// when the memory changes, so repeated exports are byte-stable.
func graphStamp(recs []store.Record) string {
	latest := ""
	for _, r := range recs {
		for _, v := range []string{stringField(r.Front, "last_used"), r.Created} {
			if len(v) == 10 && v > latest {
				latest = v
			}
		}
	}
	if latest == "" {
		return "1970-01-01T00:00:00Z"
	}
	return latest + "T00:00:00Z"
}

// writeGraph renders the payload in the requested format to w.
func writeGraph(w io.Writer, format string, p *graphPayload) error {
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(p)
	}
	_, err := io.WriteString(w, renderGraphHTML(p))
	return err
}

// writeFileTarget writes the graph to a real file.
func writeFileTarget(target, format string, p *graphPayload) error {
	f, err := os.Create(target)
	if err != nil {
		return err
	}
	if err := writeGraph(f, format, p); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// warnIfInsideEngineRepo is the soft guard: a generated graph carries
// private titles and metadata, so writing it inside a source repo (detected
// by a go.mod plus .github/workflows in the target's ancestry) earns a loud
// warning on stderr. The write still happens — the owner may be testing
// intentionally.
func warnIfInsideEngineRepo(e *Env, target string) {
	abs, err := filepath.Abs(target)
	if err != nil {
		return
	}
	for dir := filepath.Dir(abs); ; dir = filepath.Dir(dir) {
		if statJoin(dir, "go.mod") && statJoin(dir, filepath.Join(".github", "workflows")) {
			fmt.Fprintf(e.Stderr,
				"mem: WARNING: %s อยู่ใน source repo (%s) — กราฟมี private memory metadata ห้าม commit หรือแชร์ (.gitignore ควรกันไว้)\n",
				target, dir)
			return
		}
		if parent := filepath.Dir(dir); parent == dir {
			return
		}
	}
}

// statJoin reports whether path joined with name exists (file or dir).
func statJoin(path, name string) bool {
	_, err := os.Stat(filepath.Join(path, name))
	return err == nil
}
