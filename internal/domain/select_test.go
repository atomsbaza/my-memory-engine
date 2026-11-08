// P2.1 selection tests: exact scoring arithmetic, filters, the 4.0 query
// threshold with its fallbacks, RRF fusion ordering, and pick_for_query
// with its expiry skip and threshold gate. Synthetic fixtures only.
package domain

import (
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/atomsbaza/my-memory-engine/internal/codec"
	"github.com/atomsbaza/my-memory-engine/internal/schema"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// rec returns an in-memory record (no file) with the given shape.
func mrec(id, layer, title, body, scope string, mut func(codec.Frontmatter)) store.Record {
	fm := codec.Frontmatter{
		"id": id, "title": title, "layer": layer, "scope": scope,
		"tags": []string{}, "confidence": "0.9", "decay": "1.0",
		"created": "2026-01-01", "last_used": "2020-01-01", "expires": "none",
		"status": "active", "sensitivity": "internal",
	}
	if mut != nil {
		mut(fm)
	}
	tags, _ := fm["tags"].([]string)
	return store.Record{Front: fm, Body: body, ID: id, Layer: layer, Title: title,
		Scope: scope, Status: "active", Sensitivity: "internal", Created: "2026-01-01",
		Decay: 1, Expires: "none", Tags: tags}
}

func floatEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestScoreExactArithmetic(t *testing.T) {
	pinClock(t, "2026-11-08T10:00:00")
	rec := mrec("rule.aaaa0001", "rule", "alpha rule", "alpha beta gamma body text", "global",
		func(fm codec.Frontmatter) {
			fm["tags"] = []string{"alpha"}
			fm["last_used"] = "2026-11-08"
		})

	// 6.0 layer + 4.0 title("alpha") + 3.0 tag("alpha") + 2.0 body(alpha,beta)
	// + 1.5+1.5 substring(alpha,beta) + 0.9 conf + 3.0 scope + 0.2 used today.
	want := 6.0 + 4.0 + 3.0 + 2.0 + 1.5 + 1.5 + 0.9 + 3.0 + 0.2
	if got := Score(rec, "alpha beta", "global", DefaultScoring); !floatEq(got, want) {
		t.Fatalf("Score = %v, want %v", got, want)
	}

	// Scope control: a different scope loses the +3.
	if got := Score(rec, "alpha beta", "project:Else", DefaultScoring); !floatEq(got, want-3.0) {
		t.Fatalf("Score(no scope match) = %v, want %v", got, want-3.0)
	}

	// Status control: inactive loses 10.
	inactive := mrec("rule.aaaa0001", "rule", "alpha rule", "alpha beta gamma body text", "global",
		func(fm codec.Frontmatter) {
			fm["tags"] = []string{"alpha"}
			fm["last_used"] = "2026-11-08"
			fm["status"] = "superseded"
		})
	inactive.Status = "superseded"
	if got := Score(inactive, "alpha beta", "global", DefaultScoring); !floatEq(got, want-10.0) {
		t.Fatalf("Score(inactive) = %v, want %v", got, want-10.0)
	}

	// Expiry control: −5, and decay multiplies the total.
	expiredDecayed := mrec("rule.aaaa0001", "rule", "alpha rule", "alpha beta gamma body text", "global",
		func(fm codec.Frontmatter) {
			fm["tags"] = []string{"alpha"}
			fm["last_used"] = "2026-11-08"
			fm["expires"] = "2026-11-07"
			fm["decay"] = "0.5"
		})
	expiredDecayed.Expires = "2026-11-07"
	expiredDecayed.Decay = 0.5
	if got := Score(expiredDecayed, "alpha beta", "global", DefaultScoring); !floatEq(got, (want-5.0)*0.5) {
		t.Fatalf("Score(expired, decay 0.5) = %v, want %v", got, (want-5.0)*0.5)
	}

	// Recency control: w_recency applies 0.5^(days/half_life).
	recency := mrec("lesson.bbbb0001", "lesson", "recency probe", "plain body words", "global",
		func(fm codec.Frontmatter) {
			fm["last_used"] = "2026-10-29" // 10 days before the pinned today
			fm["half_life"] = "10d"
		})
	// 3.0 layer + 0.9 conf + 1.0*0.5 recency
	if got := Score(recency, "", "", schemaScoring(1.0, 1.0)); !floatEq(got, 3.0+0.9+0.5) {
		t.Fatalf("Score(recency) = %v, want %v", got, 3.0+0.9+0.5)
	}
}

func TestSelectFilters(t *testing.T) {
	pinClock(t, "2026-11-08T10:00:00")
	recs := []store.Record{
		mrec("lesson.n1", "lesson", "alpha one", "alpha body one", "global", nil),
		mrec("lesson.n2", "lesson", "alpha two", "alpha body two", "project:Alpha", nil),
		mrec("lesson.n3", "lesson", "alpha three", "alpha body three", "project:Other", nil),
		mrec("lesson.n4", "lesson", "repo note", "alpha body repo", "repo:owner/repo", nil),
		mrec("lesson.n5", "lesson", "inactive twin", "alpha body five", "global",
			func(fm codec.Frontmatter) { fm["status"] = "superseded" }),
		mrec("lesson.n6", "lesson", "quarantined twin", "alpha body six", "global",
			func(fm codec.Frontmatter) { fm["sensitivity"] = "quarantine" }),
		mrec("decision.d1", "decision", "alpha decision", "alpha decision body", "global", nil),
	}
	q := "alpha"
	recs[4].Status = "superseded"
	recs[5].Sensitivity = "quarantine"

	// Scope filter keeps global + exact + repo:*; drops project:Other,
	// inactive and quarantined records.
	got := Select(recs, q, "project:Alpha", nil, 10)
	ids := idList(got)
	for _, want := range []string{"lesson.n1", "lesson.n2", "lesson.n4"} {
		if !containsID(ids, want) {
			t.Errorf("scope select missing %s (got %v)", want, ids)
		}
	}
	for _, banned := range []string{"lesson.n3", "lesson.n5", "lesson.n6"} {
		if containsID(ids, banned) {
			t.Errorf("scope select leaked %s (got %v)", banned, ids)
		}
	}

	// IncludeInactive / IncludeQuarantine widen the filter set. With a
	// query they still cannot beat the score gate (an inactive record is
	// −10), so exercise them on the no-query path (pure ranking).
	all := SelectFrom2(t, recs, SelectOptions{Limit: 10, IncludeInactive: true, IncludeQuarantine: true})
	if !containsID(all, "lesson.n5") || !containsID(all, "lesson.n6") {
		t.Errorf("include flags must surface inactive/quarantine (got %v)", all)
	}
	plain := SelectFrom2(t, recs, SelectOptions{Limit: 10})
	if containsID(plain, "lesson.n5") || containsID(plain, "lesson.n6") {
		t.Errorf("defaults must hide inactive/quarantine (got %v)", plain)
	}

	// Layer filter.
	only := Select(recs, q, "", []string{"decision"}, 10)
	if len(only) != 1 || only[0].ID != "decision.d1" {
		t.Errorf("layer filter = %v, want decision.d1 only", idList(only))
	}

	// Limit is honored.
	if got := Select(recs, q, "", nil, 1); len(got) != 1 {
		t.Errorf("limit 1 returned %d records", len(got))
	}
}

func TestSelectQueryThresholdGate(t *testing.T) {
	pinClock(t, "2026-11-08T10:00:00")
	// A no-overlap lesson scores 3.0 + 0.9 = 3.9 ≤ 4.0 threshold: even the
	// top-ranked one must be dropped for a query it shares no token with.
	quiet := mrec("lesson.q1", "lesson", "silent topic", "unrelated words entirely", "global", nil)
	hits := mrec("lesson.q2", "lesson", "target topic", "zebra words here", "global", nil)

	got := Select([]store.Record{quiet, hits}, "zebra", "", nil, 10)
	if len(got) != 1 || got[0].ID != "lesson.q2" {
		t.Fatalf("threshold gate = %v, want only lesson.q2", idList(got))
	}
}

func TestSelectFallbacks(t *testing.T) {
	pinClock(t, "2026-11-08T10:00:00")
	quiet := mrec("lesson.f1", "lesson", "silent", "unrelated words", "global", nil)   // 3.9
	scoped := mrec("lesson.f2", "lesson", "scoped quiet", "other words", "project:Beta", nil)
	recs := []store.Record{quiet, scoped}

	// FTS fallback: an under-threshold record still surfaces when the FTS
	// rank list found it (Python: extra = fts50[:40] ∪ vec50[:40]).
	got := SelectFrom2(t, recs, SelectOptions{Query: "zebra", Limit: 10, FTSIDs: []string{"lesson.f1"}})
	if len(got) != 1 || got[0] != "lesson.f1" {
		t.Fatalf("fts fallback = %v, want lesson.f1", got)
	}
	// Control: without FTS ranks and without a token hit there is nothing —
	// then the scope fallback kicks in (up to 3 exact-scope records).
	got = SelectFrom2(t, recs, SelectOptions{Query: "zebra", Scope: "project:Beta", Limit: 10})
	if len(got) != 1 || got[0] != "lesson.f2" {
		t.Fatalf("scope fallback = %v, want lesson.f2", got)
	}
	got = SelectFrom2(t, recs, SelectOptions{Query: "zebra", Limit: 10})
	if len(got) != 0 {
		t.Fatalf("no fallback without scope/fts = %v, want empty", got)
	}
}

func TestSelectRRFReorders(t *testing.T) {
	pinClock(t, "2026-11-08T10:00:00")
	strong := mrec("lesson.r1", "lesson", "zebra title", "plain body", "global", nil)        // scored rank 1
	lexical := mrec("lesson.r2", "lesson", "other title", "zebra appears in body only", "global", nil) // fts rank 1

	// Score ranking control: the title hit outweighs the body hit; with
	// both retrieval rank lists suppressed (VecIDs pinned empty) the
	// scored order decides alone.
	if !(Score(strong, "zebra", "", DefaultScoring) > Score(lexical, "zebra", "", DefaultScoring)) {
		t.Fatal("control premise: strong must outscore lexical")
	}
	scoreOnly := SelectFrom2(t, []store.Record{strong, lexical},
		SelectOptions{Query: "zebra", Limit: 10, VecIDs: []string{}})
	if len(scoreOnly) != 2 || scoreOnly[0] != "lesson.r1" {
		t.Fatalf("score-only order = %v, want lesson.r1 first", scoreOnly)
	}

	// Without a derived index the baseline has no vector rank list either
	// (vector_ids reads the index's vec table), so the scored order decides
	// alone — exactly the score-only control above.
	noIndex := SelectFrom2(t, []store.Record{strong, lexical},
		SelectOptions{Query: "zebra", Limit: 10})
	if len(noIndex) != 2 || noIndex[0] != "lesson.r1" {
		t.Fatalf("no-index order = %v, want lesson.r1 first", noIndex)
	}

	// With the index present, plain selection fuses the lexical vector ranks:
	// r2 = 1.2/61 + 6/62 > r1 = 6/61.
	fused := SelectFrom2(t, []store.Record{strong, lexical},
		SelectOptions{Query: "zebra", Limit: 10, Indexed: true})
	if fused[0] != "lesson.r2" {
		t.Fatalf("vec fusion order = %v, want lesson.r2 first", fused)
	}

	// With the FTS rank naming only the lexical record the margin grows:
	// r2 = 1/61 + 1.2/61 + 6/62.
	ids := SelectFrom2(t, []store.Record{strong, lexical},
		SelectOptions{Query: "zebra", Limit: 10, FTSIDs: []string{"lesson.r2"}, Indexed: true})
	if ids[0] != "lesson.r2" {
		t.Fatalf("rrf order = %v, want lesson.r2 first", ids)
	}

	// Fusion arithmetic probe: the exact RRF sums.
	rrf := rrfBreakdown([]string{"lesson.r2"}, nil, []string{"lesson.r1", "lesson.r2"})
	if !floatEq(rrf["lesson.r2"], 1.0/(60+1)+6.0/(60+2)) ||
		!floatEq(rrf["lesson.r1"], 6.0/(60+1)) {
		t.Fatalf("rrf breakdown = %v", rrf)
	}
}

func TestPickForQueryGateAndExpiry(t *testing.T) {
	pinClock(t, "2026-11-08T10:00:00")
	// "go" is a 2-rune token: overlap counts (+1 body) but the substring
	// bonus does not (len > 2 required) → 3.0 + 0.9 + 1.0 = 4.9 < 6.0.
	low := mrec("lesson.p1", "lesson", "short token", "go words here", "global", nil)
	// Title hit: 3.0 + 4.0 + 0.9 = 7.9 ≥ 6.0 (2-rune tokens earn no substring bonus).
	high := mrec("lesson.p2", "lesson", "gopher go title", "words here", "global", nil)
	expired := mrec("handoff.p3", "handoff", "expired go handoff", "go handoff words", "global",
		func(fm codec.Frontmatter) { fm["expires"] = "2026-11-07" })
	expired.Expires = "2026-11-07"
	fresh := mrec("handoff.p4", "handoff", "fresh go handoff", "go handoff terms", "global",
		func(fm codec.Frontmatter) { fm["expires"] = "2999-01-01" })
	fresh.Expires = "2999-01-01"
	recs := []store.Record{low, high, expired, fresh}

	picked, err := PickForQuery(recs, RetrievalOptions{Query: "go"})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(picked))
	for _, p := range picked {
		ids = append(ids, p.Rec.ID)
	}
	if containsID(ids, "lesson.p1") {
		t.Errorf("4.9-score record must stay under the 6.0 gate (picked %v)", ids)
	}
	if containsID(ids, "handoff.p3") {
		t.Errorf("expired handoff must be skipped (picked %v)", ids)
	}
	if !containsID(ids, "lesson.p2") || !containsID(ids, "handoff.p4") {
		t.Errorf("want p2 and fresh handoff, got %v", ids)
	}
	// Sorted desc by score; dedup and limit hold.
	for i := 1; i < len(picked); i++ {
		if picked[i-1].Score < picked[i].Score {
			t.Errorf("picked not sorted desc: %v", picked)
		}
	}
	lim, err := PickForQuery(recs, RetrievalOptions{Query: "go", Limit: 1})
	if err != nil || len(lim) != 1 {
		t.Fatalf("limit 1: %d records, err %v", len(lim), err)
	}
}

// The hook recall path fuses exactly the rank lists the baseline can build.
// Python's vector_ids reads the derived index's vec table, so a home that never
// ran a reindex has no vector ranks at all: the per-layer candidate window is
// the top five scores, with no vector entry able to displace them. (P2 hook
// parity: the engine fused on-the-fly vector ranks in that state and surfaced
// records the baseline never retrieved.)
func TestPickForQueryWithoutIndexKeepsScoreOrder(t *testing.T) {
	pinClock(t, "2026-11-08T10:00:00")
	// Five title hits ranked by confidence, all with bodies the query cannot
	// match — so only the score list ranks them.
	recs := make([]store.Record, 0, 6)
	for i, conf := range []string{"1.0", "0.9", "0.8", "0.7", "0.6"} {
		c := conf
		recs = append(recs, mrec("lesson.s"+strconv.Itoa(i+1), "lesson", "zebra title",
			"plain unrelated body", "global", func(fm codec.Frontmatter) {
				fm["confidence"] = c
			}))
	}
	// Below every title hit, but its body carries the query token, so it is
	// the vector rank's first (and only) entry.
	lexical := mrec("lesson.v1", "lesson", "other title", "zebra appears in body only", "global", nil)
	recs = append(recs, lexical)

	ids := func(picked []ScoredRecord) []string {
		out := make([]string, 0, len(picked))
		for _, p := range picked {
			out = append(out, p.Rec.ID)
		}
		return out
	}

	noIndex, err := PickForQuery(recs, RetrievalOptions{Query: "zebra", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	got := ids(noIndex)
	if containsID(got, "lesson.v1") || !containsID(got, "lesson.s5") {
		t.Fatalf("no-index pick = %v, want the five score hits (no vector entry)", got)
	}

	indexed, err := PickForQuery(recs, RetrievalOptions{Query: "zebra", Limit: 5, Indexed: true})
	if err != nil {
		t.Fatal(err)
	}
	got = ids(indexed)
	if !containsID(got, "lesson.v1") || containsID(got, "lesson.s5") {
		t.Fatalf("indexed pick = %v, want lesson.v1 to displace the fifth score hit", got)
	}
}

func TestVectorRanksLexical(t *testing.T) {
	pinClock(t, "2026-11-08T10:00:00")
	relevant := mrec("lesson.v1", "lesson", "topic", "kyoto osaka tokyo nagoya", "global", nil)
	other := mrec("lesson.v2", "lesson", "unrelated", "paris lyon marseille", "global", nil)
	recs := []store.Record{relevant, other}

	ids := vectorIDs(recs, "kyoto tokyo", 50)
	if len(ids) == 0 || ids[0] != "lesson.v1" {
		t.Fatalf("vector ranks = %v, want lesson.v1 first", ids)
	}
	if containsID(ids, "lesson.v2") {
		t.Fatalf("no-overlap record must not rank: %v", ids)
	}
	if got := vectorIDs(recs, "", 50); got != nil {
		t.Fatalf("empty query must return no ranks, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func pinClock(t *testing.T, iso string) {
	t.Helper()
	prev := timeNow
	timeNow = func() time.Time {
		ts, err := time.Parse("2006-01-02T15:04:05", iso)
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}
	t.Cleanup(func() { timeNow = prev })
}

func schemaScoring(wConf, wRecency float64) (s schema.Scoring) {
	s = DefaultScoring
	s.WConf = wConf
	s.WRecency = wRecency
	return
}

func idList(recs []store.Record) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.ID)
	}
	return out
}

func containsID(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func SelectFrom2(t *testing.T, recs []store.Record, o SelectOptions) []string {
	t.Helper()
	out, err := SelectFrom(recs, o)
	if err != nil {
		t.Fatal(err)
	}
	return idList(out)
}

func nil2Records(t *testing.T, root string) []store.Record {
	t.Helper()
	recs, err := store.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}
