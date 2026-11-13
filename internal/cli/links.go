// `mem links` — the read-only link explorer: how one record connects to the
// rest of the corpus (outbound/inbound supersede chains, evidence ids, body
// [id: …] markers, --related by scope/tags). It never writes: no touch, no
// log event, no git.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// linksIDRe is the record-id shape every link source must match before it is
// treated as a reference (free-text evidence like "reviewed: …" is ignored).
var linksIDRe = regexp.MustCompile(`^[a-z]+\.[0-9a-f]{8,16}$`)

// linksBodyRe finds `[id: <layer>.<hex8-16>]` markers in a body.
var linksBodyRe = regexp.MustCompile(`\[id: ([a-z]+\.[0-9a-f]{8,16})\]`)

// maxLinksDepth caps --depth: the supersede chain is walked transitively, so
// an accidental cycle must not be able to ask for unbounded work.
const maxLinksDepth = 10

// linkRef is one edge from a record to another id.
type linkRef struct {
	Kind  string // supersedes | superseded-by | evidence | body-ref | promoted-from | compacted-into
	ID    string
	Line  int // body-ref only: 1-based line of the first marker
	More  int // body-ref only: collapsed duplicate markers
	Depth int // supersede-chain expansion (1 = direct edge)
}

// linksNode is one scanned record plus its direct outbound edges and the
// reverse edges pointing at it.
type linksNode struct {
	rec store.Record
	out []linkRef
	in  []linkRef
}

// linkRow is the JSON shape of one edge (spec §JSON output).
type linkRow struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Exists    bool   `json:"exists"`
	File      string `json:"file,omitempty"`
	Title     string `json:"title,omitempty"`
	Line      int    `json:"line,omitempty"`
	More      int    `json:"more,omitempty"`
	Depth     int    `json:"depth,omitempty"`
	OtherRefs int    `json:"other_refs,omitempty"`
}

// linksPayload is the single JSON document `--json` prints.
type linksPayload struct {
	Record struct {
		ID     string `json:"id"`
		Layer  string `json:"layer"`
		Scope  string `json:"scope"`
		Status string `json:"status"`
		Title  string `json:"title"`
		File   string `json:"file"`
	} `json:"record"`
	Outbound []linkRow `json:"outbound"`
	Inbound  []linkRow `json:"inbound"`
	Related  *struct {
		SameScope  []string          `json:"same_scope"`
		SharedTags []linksSharedTags `json:"shared_tags"`
	} `json:"related,omitempty"`
}

type linksSharedTags struct {
	ID      string  `json:"id"`
	Overlap float64 `json:"overlap"`
}

// cmdLinks implements `mem links <id|prefix> [--depth N] [--related] [--json]`.
func cmdLinks(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	depth := fs.Int("depth", 1, "ขยาย supersede chain แบบทอดทอง (1-10)")
	related := fs.Bool("related", false, "แสดง same-scope และ shared-tags")
	asJSON := fs.Bool("json", false, "พิมพ์เป็น JSON document เดียว")
	args, code, stop := e.parseArgs(c, fs, argv)
	if stop {
		return code
	}
	if code, stop := e.needArgs(len(args), 1, "<id>"); stop {
		return code
	}
	if *depth < 1 || *depth > maxLinksDepth {
		return e.usagef("--depth ต้องอยู่ระหว่าง 1 ถึง %d", maxLinksDepth)
	}

	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	recs, err := e.scan(root)
	if err != nil {
		return e.fail(err)
	}

	// Resolve like show but stricter: exact id, or a unique prefix of at
	// least 6 chars. Ambiguity is a usage error with the candidates listed.
	target, code := resolveLinksTarget(recs, args[0])
	if code != ExitOK {
		if len(args[0]) >= 6 {
			if matches := linksPrefixMatches(recs, args[0]); len(matches) > 1 {
				fmt.Fprintln(e.Stderr, "candidates:")
				for _, m := range matches {
					fmt.Fprintf(e.Stderr, "  %s  [%s/%s] %s\n", m.ID, m.Layer, m.Scope, m.Title)
				}
			} else {
				fmt.Fprintf(e.Stderr, "mem: ไม่พบ record: %s\n", args[0])
			}
		}
		return code
	}

	// Build the graph (forward edges + reverse index) in one walk.
	graph := buildLinksGraph(recs)

	node := graph.byID[target.ID]
	var outbound []linkRef
	if node != nil {
		outbound = append(outbound, node.out...)
		outbound = appendSupersedeChain(outbound, graph, node, "out", *depth)
	}
	var inbound []linkRef
	if node != nil {
		inbound = append(inbound, node.in...)
		inbound = appendSupersedeChain(inbound, graph, node, "in", *depth)
	}

	var rel *struct {
		SameScope  []string          `json:"same_scope"`
		SharedTags []linksSharedTags `json:"shared_tags"`
	}
	if *related {
		rel = relatedRecords(recs, target)
	}

	if *asJSON {
		payload := linksPayload{
			Outbound: linksRows(graph, outbound),
			Inbound:  linksRows(graph, inbound),
			Related:  rel,
		}
		payload.Record.ID = target.ID
		payload.Record.Layer = target.Layer
		payload.Record.Scope = target.Scope
		payload.Record.Status = target.Status
		payload.Record.Title = target.Title
		payload.Record.File = target.Rel
		enc := json.NewEncoder(e.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		if err := enc.Encode(payload); err != nil {
			return e.fail(err)
		}
		return ExitOK
	}

	fmt.Fprintf(e.Stdout, "record  %s  [%s/%s]  status=%s  decay=%.2f  last_used=%s\n",
		target.ID, target.Layer, target.Scope, target.Status, target.Decay, lastOf(target))
	fmt.Fprintf(e.Stdout, "title   %s\n", target.Title)
	fmt.Fprintf(e.Stdout, "file    %s\n", target.Rel)

	renderLinksSection(e.Stdout, "OUTBOUND (this → others)", graph, outbound)
	renderLinksSection(e.Stdout, "INBOUND (others → this)", graph, inbound)
	if rel != nil {
		fmt.Fprintln(e.Stdout, "\nRELATED (--related)")
		fmt.Fprintln(e.Stdout, "  same-scope")
		if len(rel.SameScope) == 0 {
			fmt.Fprintln(e.Stdout, "    (none)")
		}
		for _, id := range rel.SameScope {
			fmt.Fprintf(e.Stdout, "    %s\n", linksDescribe(graph, id))
		}
		fmt.Fprintln(e.Stdout, "  shared-tags")
		if len(rel.SharedTags) == 0 {
			fmt.Fprintln(e.Stdout, "    (none)")
		}
		for _, s := range rel.SharedTags {
			fmt.Fprintf(e.Stdout, "    %s  (overlap %.2f)\n", linksDescribe(graph, s.ID), s.Overlap)
		}
	}
	return ExitOK
}

// resolveLinksTarget returns the record for an exact id or a unique prefix
// (>= 6 chars), with the exit code to return when it cannot.
func resolveLinksTarget(recs []store.Record, q string) (store.Record, int) {
	for _, r := range recs {
		if r.ID == q {
			return r, ExitOK
		}
	}
	if len(q) < 6 {
		return store.Record{}, ExitGuard
	}
	matches := linksPrefixMatches(recs, q)
	switch len(matches) {
	case 0:
		return store.Record{}, ExitError
	case 1:
		return matches[0], ExitOK
	default:
		return store.Record{}, ExitGuard
	}
}

func linksPrefixMatches(recs []store.Record, q string) []store.Record {
	var matches []store.Record
	for _, r := range recs {
		if strings.HasPrefix(r.ID, q) {
			matches = append(matches, r)
		}
	}
	return matches
}

// linksGraph is the reverse-indexed link graph over one scan.
type linksGraph struct {
	byID  map[string]*linksNode
	order []string // scan order of every record id
}

// buildLinksGraph walks the record set once, extracting each record's direct
// outbound edges and inverting them into the inbound index.
func buildLinksGraph(recs []store.Record) *linksGraph {
	g := &linksGraph{byID: map[string]*linksNode{}}
	for i := range recs {
		r := recs[i]
		g.byID[r.ID] = &linksNode{rec: r, out: linksOutbound(r)}
		g.order = append(g.order, r.ID)
	}
	for _, id := range g.order {
		for _, ref := range g.byID[id].out {
			if t := g.byID[ref.ID]; t != nil {
				t.in = append(t.in, linkRef{
					Kind: linksInvertKind(ref.Kind), ID: id, Line: ref.Line, More: ref.More,
				})
			}
		}
	}
	return g
}

// linksOutbound extracts every direct edge of one record, in display order:
// supersede fields, lifecycle pointers, evidence ids, then body markers.
func linksOutbound(r store.Record) []linkRef {
	var out []linkRef
	add := func(kind, id string) {
		if linksIDRe.MatchString(id) {
			out = append(out, linkRef{Kind: kind, ID: id})
		}
	}
	add("supersedes", stringField(r.Front, "supersedes"))
	add("superseded-by", stringField(r.Front, "superseded_by"))
	add("promoted-from", stringField(r.Front, "promoted_from"))
	add("compacted-into", stringField(r.Front, "compacted_into"))
	if ev, ok := r.Front["evidence"].([]string); ok {
		for _, e := range ev {
			add("evidence", e)
		}
	}
	// Body markers: same-target duplicates collapse to the first line and a
	// +K count so one chatty record cannot flood the view.
	first := map[string]int{}
	count := map[string]int{}
	var order []string
	for i, line := range strings.Split(r.Body, "\n") {
		for _, m := range linksBodyRe.FindAllStringSubmatch(line, -1) {
			id := m[1]
			if _, seen := first[id]; !seen {
				first[id] = i + 1
				order = append(order, id)
			}
			count[id]++
		}
	}
	for _, id := range order {
		out = append(out, linkRef{Kind: "body-ref", ID: id, Line: first[id], More: count[id] - 1})
	}
	return out
}

// linksInvertKind maps an outbound edge kind onto the kind the target sees.
func linksInvertKind(kind string) string {
	switch kind {
	case "supersedes", "superseded-by":
		return "superseded-by"
	case "promoted-from":
		return "promoted-to"
	default:
		return kind // evidence, body-ref, compacted-into are symmetric
	}
}

// appendSupersedeChain expands only the supersede relation transitively:
// outbound follows supersedes/superseded_by forward, inbound follows records
// that point at the current id the same way. Already-visited ids are skipped,
// so a cycle terminates.
func appendSupersedeChain(dst []linkRef, g *linksGraph, node *linksNode, dir string, depth int) []linkRef {
	if node == nil || depth <= 1 {
		return dst
	}
	seen := map[string]bool{node.rec.ID: true}
	// The depth-1 rows are the direct edges already listed; the walk starts
	// from their far end so depth 2 is genuinely one hop further.
	var frontier []string
	if dir == "out" {
		for _, ref := range node.out {
			if ref.Kind == "supersedes" || ref.Kind == "superseded-by" {
				frontier = append(frontier, ref.ID)
				seen[ref.ID] = true
			}
		}
	} else {
		for _, ref := range node.in {
			if ref.Kind == "superseded-by" {
				frontier = append(frontier, ref.ID)
				seen[ref.ID] = true
			}
		}
	}
	for d := 2; d <= depth; d++ {
		var next []string
		for _, cur := range frontier {
			var ids []string
			if dir == "out" {
				if n := g.byID[cur]; n != nil {
					for _, ref := range n.out {
						if ref.Kind == "supersedes" || ref.Kind == "superseded-by" {
							ids = append(ids, ref.ID)
						}
					}
				}
			} else if n := g.byID[cur]; n != nil {
				for _, ref := range n.in {
					if ref.Kind == "superseded-by" {
						ids = append(ids, ref.ID)
					}
				}
			}
			for _, id := range ids {
				if seen[id] || id == node.rec.ID {
					continue
				}
				seen[id] = true
				next = append(next, id)
				kind := "supersedes"
				if dir == "in" {
					kind = "superseded-by"
				}
				dst = append(dst, linkRef{Kind: kind, ID: id, Depth: d})
			}
		}
		frontier = next
		if len(frontier) == 0 {
			break
		}
	}
	return dst
}

// relatedRecords picks same-scope records (scan order, excluding self, cap
// 10) and the top-10 tag-jaccard neighbours.
func relatedRecords(recs []store.Record, self store.Record) *struct {
	SameScope  []string          `json:"same_scope"`
	SharedTags []linksSharedTags `json:"shared_tags"`
} {
	rel := &struct {
		SameScope  []string          `json:"same_scope"`
		SharedTags []linksSharedTags `json:"shared_tags"`
	}{}
	for _, r := range recs {
		if r.ID == self.ID {
			continue
		}
		if r.Scope == self.Scope && len(rel.SameScope) < 10 {
			rel.SameScope = append(rel.SameScope, r.ID)
		}
	}
	selfTags := map[string]bool{}
	for _, t := range self.Tags {
		selfTags[t] = true
	}
	type scored struct {
		id    string
		score float64
	}
	var shared []scored
	for _, r := range recs {
		if r.ID == self.ID || len(selfTags) == 0 || len(r.Tags) == 0 {
			continue
		}
		union := map[string]bool{}
		for t := range selfTags {
			union[t] = true
		}
		inter := 0
		for _, t := range r.Tags {
			if selfTags[t] {
				inter++
			}
			union[t] = true
		}
		if inter == 0 {
			continue
		}
		shared = append(shared, scored{r.ID, float64(inter) / float64(len(union))})
	}
	sort.SliceStable(shared, func(i, j int) bool { return shared[i].score > shared[j].score })
	for i, s := range shared {
		if i == 10 {
			break
		}
		rel.SharedTags = append(rel.SharedTags, linksSharedTags{ID: s.id, Overlap: s.score})
	}
	return rel
}

// linksRows turns edge refs into the JSON rows, resolving existence, file,
// title and the referenced record's own link count.
func linksRows(g *linksGraph, refs []linkRef) []linkRow {
	rows := make([]linkRow, 0, len(refs))
	for _, ref := range refs {
		row := linkRow{Kind: ref.Kind, ID: ref.ID, Line: ref.Line, More: ref.More, Depth: ref.Depth}
		if n := g.byID[ref.ID]; n != nil {
			row.Exists = true
			row.File = n.rec.Rel
			row.Title = n.rec.Title
			row.OtherRefs = len(n.out) + len(n.in)
		}
		rows = append(rows, row)
	}
	return rows
}

// renderLinksSection prints one edge block in the stable, greppable text
// shape; dangling ids are shown explicitly, never dropped.
func renderLinksSection(w io.Writer, header string, g *linksGraph, refs []linkRef) {
	fmt.Fprintf(w, "\n%s\n", header)
	if len(refs) == 0 {
		fmt.Fprintln(w, "  (none)")
		return
	}
	for _, ref := range refs {
		indent := strings.Repeat("  ", 1+max(0, ref.Depth-1))
		note := ""
		if n := g.byID[ref.ID]; n != nil {
			note = fmt.Sprintf("[%s/%s]", n.rec.Layer, n.rec.Scope)
			if ref.Kind == "body-ref" {
				note += fmt.Sprintf("  (line %d", ref.Line)
				if ref.More > 0 {
					note += fmt.Sprintf(", +%d more", ref.More)
				}
				note += ")"
			} else {
				title := n.rec.Title
				if title == "" {
					title = n.rec.ID
				}
				note += "  " + title
				if ref.Depth > 1 {
					note += fmt.Sprintf("  (depth %d)", ref.Depth)
				}
			}
		} else {
			note = "DANGLING (not found)"
		}
		fmt.Fprintf(w, "%s%-15s%-19s%s\n", indent, ref.Kind, ref.ID, note)
	}
}

// linksDescribe renders "id [layer/scope] title" for the RELATED section.
func linksDescribe(g *linksGraph, id string) string {
	if n := g.byID[id]; n != nil {
		title := n.rec.Title
		if title == "" {
			title = id
		}
		return fmt.Sprintf("%s  [%s/%s]  %s", id, n.rec.Layer, n.rec.Scope, title)
	}
	return id + "  DANGLING (not found)"
}
