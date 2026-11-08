// Selection — the Python select_from / select / pick_for_query port with
// RRF fusion (FTS + lexical vector + scored rank) and the query-token
// threshold gate with its fallbacks.
package domain

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/atomsbaza/my-memory-engine/internal/schema"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// RRF constants (Python RRF_K / RRF_W_*).
const (
	rrfK        = 60.0
	rrfWFTS     = 1.0
	rrfWVector  = 1.2
	rrfWScored  = 6.0
	selectLimit = 50 // fts/vec rank list length inside selection
)

// SelectOptions mirrors Python select_from parameters. Zero values follow
// the Python defaults: active-only, no quarantine, limit 10.
type SelectOptions struct {
	Scope             string
	Layers            []string
	Query             string
	Limit             int
	IncludeInactive   bool // Python active_only=True default
	IncludeQuarantine bool // Python include_quarantine=False default

	// Scoring weights; nil → DefaultScoring. A pointer to a zero Scoring is
	// an explicit zero-weights choice (Python N2: `{}` must not silently
	// become the defaults).
	Scoring *schema.Scoring

	// FTS provides derived-index ranks, fts_ids(query, limit). nil → no FTS
	// ranks (Python with no index file). Errors propagate — the engine does
	// not swallow retrieval failures.
	FTS func(query string, limit int) ([]string, error)
	// FTSIDs and VecIDs are precomputed fts/vector rank lists (Python
	// pre_fts/pre_vec); when set they replace FTS / the internal lexical
	// computation, so multi-layer selection queries the ranks once.
	FTSIDs []string
	VecIDs []string

	// Breakdown, when non-nil, receives the RRF fusion internals of this call
	// (Python select(..., breakdown=info)) so debug surfaces (memory_trace,
	// search --scores) print the very numbers the ranking used. It is filled
	// only when the query has tokens — the same condition that enables RRF.
	Breakdown *Breakdown
}

// RRFParams holds the fusion constants SelectFrom uses: every fused list
// contributes weight/(K+rank) (Python RRF_K / RRF_W_*).
type RRFParams struct {
	K       float64
	WFTS    float64
	WVector float64
	WScored float64
}

// RRF returns the fusion constants of the RRF step.
func RRF() RRFParams {
	return RRFParams{K: rrfK, WFTS: rrfWFTS, WVector: rrfWVector, WScored: rrfWScored}
}

// RankInfo is a record's 1-based rank in each fused list; 0 means the record
// is absent from that list (Python's missing ranks key).
type RankInfo struct {
	FTS    int
	Vec    int
	Scored int
}

// Breakdown is one SelectFrom call's RRF fusion (Python rrf_breakdown):
// the score each candidate contributed and its rank in each list.
type Breakdown struct {
	RRF   map[string]float64
	Ranks map[string]RankInfo
}

// fill writes the fusion result into b (Python rrf_breakdown's ranks dict).
func (b *Breakdown) fill(fts, vec, scored []string, rrf map[string]float64) {
	b.RRF = rrf
	b.Ranks = make(map[string]RankInfo, len(fts)+len(vec))
	for i, id := range fts {
		r := b.Ranks[id]
		r.FTS = i + 1
		b.Ranks[id] = r
	}
	for i, id := range vec {
		r := b.Ranks[id]
		r.Vec = i + 1
		b.Ranks[id] = r
	}
	for i, id := range scored {
		r := b.Ranks[id]
		r.Scored = i + 1
		b.Ranks[id] = r
	}
}

func (o *SelectOptions) weights() schema.Scoring {
	if o.Scoring != nil {
		return *o.Scoring
	}
	return DefaultScoring
}

// Select is the plain retrieval entry point over already-scanned records:
// active-only, quarantine excluded, default scoring, no FTS ranks.
func Select(recs []store.Record, query, scope string, layers []string, limit int) []store.Record {
	out, err := SelectFrom(recs, SelectOptions{
		Scope:  scope,
		Layers: layers,
		Query:  query,
		Limit:  limit,
	})
	if err != nil {
		// No FTS provider is wired, so SelectFrom cannot fail; keep the
		// simple signature honest rather than hiding a impossible error.
		panic(err)
	}
	return out
}

// SelectFrom filters, scores and ranks recs (Python select_from). recs
// should be the full record set of the home (as at every Python call
// site): the lexical vector ranks are computed over it and layer/scope
// filtering happens inside.
func SelectFrom(recs []store.Record, o SelectOptions) ([]store.Record, error) {
	limit := o.Limit
	if limit <= 0 {
		limit = 10
	}
	w := o.weights()

	// The lexical vector ranks come from the whole corpus, exactly like
	// Python vector_ids — it reads the vec/df tables, which no layer, scope,
	// status or sensitivity filter can narrow. Every filter below therefore
	// operates on a re-sliced copy, never on allRecs.
	allRecs := recs

	if len(o.Layers) > 0 {
		want := map[string]bool{}
		for _, l := range o.Layers {
			want[l] = true
		}
		filtered := recs[:0:0]
		for _, r := range recs {
			if want[r.Layer] {
				filtered = append(filtered, r)
			}
		}
		recs = filtered
	}
	if !o.IncludeQuarantine {
		filtered := recs[:0:0]
		for _, r := range recs {
			if r.Sensitivity != "quarantine" {
				filtered = append(filtered, r)
			}
		}
		recs = filtered
	}
	if !o.IncludeInactive {
		filtered := recs[:0:0]
		for _, r := range recs {
			if r.Status == "active" {
				filtered = append(filtered, r)
			}
		}
		recs = filtered
	}
	if o.Scope != "" {
		filtered := recs[:0:0]
		for _, r := range recs {
			if r.Scope == o.Scope || r.Scope == "global" || strings.HasPrefix(r.Scope, "repo:") {
				filtered = append(filtered, r)
			}
		}
		recs = filtered
	}

	qTokens := Tokens(o.Query)
	var fts50, vec50 []string
	if len(qTokens) > 0 {
		var err error
		switch {
		case o.FTSIDs != nil:
			fts50 = o.FTSIDs
		case o.FTS != nil:
			if fts50, err = o.FTS(o.Query, selectLimit); err != nil {
				return nil, err
			}
		}
		if o.VecIDs != nil {
			vec50 = o.VecIDs
		} else {
			vec50 = vectorIDs(allRecs, o.Query, selectLimit)
		}
	}

	scores := make(map[string]float64, len(recs))
	for _, r := range recs {
		scores[r.ID] = Score(r, o.Query, o.Scope, w)
	}
	ranked := make([]store.Record, len(recs))
	copy(ranked, recs)
	sort.SliceStable(ranked, func(i, j int) bool { return scores[ranked[i].ID] > scores[ranked[j].ID] })

	if len(qTokens) > 0 {
		// Python: thresh = 2.0 + LAYER_WEIGHT["handoff"] (=4.0); hits must
		// both clear it and share at least one query token with
		// title/body/tags.
		thresh := 2.0 + layerWeight["handoff"]
		var hits []store.Record
		for _, r := range ranked {
			if scores[r.ID] <= thresh {
				continue
			}
			recToks := Tokens(strField(r.Front, "title"))
			for t := range Tokens(r.Body) {
				recToks[t] = true
			}
			for t := range Tokens(strings.Join(r.Tags, " ")) {
				recToks[t] = true
			}
			if countIntersection(qTokens, recToks) > 0 {
				hits = append(hits, r)
			}
		}
		if len(hits) == 0 {
			// Fallback 1: anything the retrieval lists found.
			extra := map[string]bool{}
			for _, id := range clipList(fts50, 40) {
				extra[id] = true
			}
			for _, id := range clipList(vec50, 40) {
				extra[id] = true
			}
			for _, r := range ranked {
				if extra[r.ID] {
					hits = append(hits, r)
				}
			}
		}
		if len(hits) == 0 && o.Scope != "" {
			// Fallback 2: up to three exact-scope records.
			for _, r := range ranked {
				if r.Scope == o.Scope {
					hits = append(hits, r)
					if len(hits) == 3 {
						break
					}
				}
			}
		}

		scoredIDs := make([]string, 0, selectLimit)
		for _, r := range ranked {
			if len(scoredIDs) == selectLimit {
				break
			}
			scoredIDs = append(scoredIDs, r.ID)
		}
		rrf := rrfBreakdown(fts50, vec50, scoredIDs)
		if o.Breakdown != nil {
			o.Breakdown.fill(fts50, vec50, scoredIDs, rrf)
		}
		sort.SliceStable(hits, func(i, j int) bool {
			ri, rj := rrf[hits[i].ID], rrf[hits[j].ID]
			if ri != rj {
				return ri > rj
			}
			return scores[hits[i].ID] > scores[hits[j].ID]
		})
		ranked = hits
	}

	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked, nil
}

func clipList(ids []string, n int) []string {
	if len(ids) > n {
		return ids[:n]
	}
	return ids
}

// rrfBreakdown fuses the three rank lists (Python rrf_breakdown): FTS,
// vector and scored, each 1-based, weight / (K + rank).
func rrfBreakdown(fts, vec, scored []string) map[string]float64 {
	rrf := map[string]float64{}
	add := func(ids []string, weight float64) {
		for i, id := range ids {
			rrf[id] += weight / (rrfK + float64(i+1))
		}
	}
	add(fts, rrfWFTS)
	add(vec, rrfWVector)
	add(scored, rrfWScored)
	return rrf
}

// ---------------------------------------------------------------------------
// Lexical vector ranks — Python vector_ids (TF·IDF cosine over chunk bags;
// no semantic embeddings, by design)
// ---------------------------------------------------------------------------

type vecChunk struct {
	id string
	tf map[string]bool // Python stores tf=1 per distinct token
}

func vectorIDs(recs []store.Record, query string, limit int) []string {
	qtoks := make([]string, 0, 8)
	for t := range Tokens(query) {
		if rlen(t) > 1 {
			qtoks = append(qtoks, t)
		}
	}
	sort.Strings(qtoks)
	if len(qtoks) == 0 {
		return nil
	}

	var chunks []vecChunk
	df := map[string]int{}
	for _, r := range recs {
		for _, ch := range chunkText(r.Body, 400) {
			toks := Tokens(ch)
			if len(toks) == 0 {
				continue
			}
			chunks = append(chunks, vecChunk{id: r.ID, tf: toks})
			for t := range toks {
				df[t]++
			}
		}
	}
	if len(chunks) == 0 || len(df) == 0 {
		return nil
	}

	n := float64(len(chunks))
	idf := func(t string) float64 { return math.Log((n+1)/(float64(df[t])+1)) + 1.0 }

	qWeights := make(map[string]float64, len(qtoks))
	var qNormSq float64
	for _, t := range qtoks {
		w := idf(t)
		qWeights[t] = w
		qNormSq += w * w
	}
	qNorm := math.Sqrt(qNormSq)
	if qNorm == 0 {
		return nil
	}

	best := map[string]float64{}
	var order []string
	for _, c := range chunks {
		var dot, cNormSq float64
		for t := range c.tf {
			w := 1.0 * idf(t)
			cNormSq += w * w
			if qw, ok := qWeights[t]; ok {
				dot += w * qw
			}
		}
		if dot <= 0 || cNormSq <= 0 {
			continue
		}
		sim := dot / (qNorm * math.Sqrt(cNormSq))
		if _, seen := best[c.id]; !seen {
			order = append(order, c.id)
		}
		if sim > best[c.id] {
			best[c.id] = sim
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return best[order[i]] > best[order[j]] })
	if len(order) > limit {
		order = order[:limit]
	}
	return order
}

// ---------------------------------------------------------------------------
// chunkText — Python chunk_text (paragraphs, then sentences, rune-length)
// ---------------------------------------------------------------------------

var paraSplitRe = regexp.MustCompile(`\n\s*\n`)

func isSentenceEnd(r rune) bool {
	return r == '.' || r == '!' || r == '?' || r == '。' || r == '\n'
}

// splitSentences reimplements Python re.split(r"(?<=[.!?。\n])\s+", para):
// cut at whitespace runs that directly follow a sentence-ending rune.
func splitSentences(para string) []string {
	runes := []rune(para)
	var out []string
	start := 0
	i := 0
	for i < len(runes) {
		if isSentenceEnd(runes[i]) {
			j := i + 1
			for j < len(runes) && unicode.IsSpace(runes[j]) {
				j++
			}
			if j > i+1 {
				if s := strings.TrimSpace(string(runes[start : i+1])); s != "" {
					out = append(out, s)
				}
				start = j
				i = j
				continue
			}
		}
		i++
	}
	if s := strings.TrimSpace(string(runes[start:])); s != "" {
		out = append(out, s)
	}
	return out
}

func chunkText(text string, maxChars int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var paras []string
	for _, p := range paraSplitRe.Split(text, -1) {
		if s := strings.TrimSpace(p); s != "" {
			paras = append(paras, s)
		}
	}
	var chunks []string
	for _, para := range paras {
		if rlen(para) <= maxChars {
			chunks = append(chunks, para)
			continue
		}
		var cur string
		for _, s := range splitSentences(para) {
			if rlen(s) > maxChars {
				if cur != "" {
					chunks = append(chunks, cur)
					cur = ""
				}
				for i := 0; i < rlen(s); i += maxChars {
					chunks = append(chunks, sliceRunes(s, i, i+maxChars))
				}
				continue
			}
			if cur != "" && rlen(cur)+1+rlen(s) > maxChars {
				chunks = append(chunks, cur)
				cur = s
			} else if cur != "" {
				cur = cur + " " + s
			} else {
				cur = s
			}
		}
		if cur != "" {
			chunks = append(chunks, cur)
		}
	}
	if len(chunks) == 0 {
		return []string{clipRunes(text, maxChars)}
	}
	return chunks
}

// sliceRunes returns s[i:j] in rune indices (Python str slicing).
func sliceRunes(s string, i, j int) string {
	runes := []rune(s)
	if j > len(runes) {
		j = len(runes)
	}
	if i > j {
		i = j
	}
	return string(runes[i:j])
}

// ---------------------------------------------------------------------------
// PickForQuery — Python pick_for_query (the hook/eval retrieval path)
// ---------------------------------------------------------------------------

// ScoredRecord pairs a record with its retrieval score.
type ScoredRecord struct {
	Rec   store.Record
	Score float64
}

// RetrievalOptions mirrors the Python pick_for_query parameters: threshold
// default 6.0, limit default 5.
type RetrievalOptions struct {
	Query     string
	Scope     string
	Threshold float64
	Limit     int
	Scoring   *schema.Scoring
	FTS       func(query string, limit int) ([]string, error)
}

// PickForQuery selects across the lesson/decision/handoff layers, skipping
// expired handoffs, keeping only records scoring >= Threshold, sorted desc,
// deduplicated by id, capped at Limit. It is pure: no writes, no touches.
func PickForQuery(recs []store.Record, o RetrievalOptions) ([]ScoredRecord, error) {
	if o.Threshold == 0 {
		o.Threshold = DefaultScoring.ThresholdHook
	}
	if o.Limit <= 0 {
		o.Limit = 5
	}
	w := DefaultScoring
	if o.Scoring != nil {
		w = *o.Scoring
	}
	qTokens := Tokens(o.Query)

	var fts50 []string
	var err error
	if len(qTokens) > 0 && o.FTS != nil {
		if fts50, err = o.FTS(o.Query, selectLimit); err != nil {
			return nil, err
		}
	}
	var vec50 []string
	if len(qTokens) > 0 {
		vec50 = vectorIDs(recs, o.Query, selectLimit)
	}

	var hits []ScoredRecord
	for _, layer := range []string{"lesson", "decision", "handoff"} {
		sel, serr := SelectFrom(recs, SelectOptions{
			Scope:   o.Scope,
			Layers:  []string{layer},
			Query:   o.Query,
			Limit:   o.Limit,
			Scoring: &w,
			FTSIDs:  fts50,
			VecIDs:  vec50,
		})
		if serr != nil {
			return nil, serr
		}
		for _, r := range sel {
			if layer == "handoff" && Expired(r) {
				continue
			}
			s := Score(r, o.Query, o.Scope, w)
			if s >= o.Threshold {
				hits = append(hits, ScoredRecord{Rec: r, Score: s})
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })

	picked := make([]ScoredRecord, 0, o.Limit)
	seen := map[string]bool{}
	for _, h := range hits {
		if seen[h.Rec.ID] {
			continue
		}
		seen[h.Rec.ID] = true
		picked = append(picked, h)
		if len(picked) == o.Limit {
			break
		}
	}
	return picked, nil
}
