// Package eval implements the golden-corpus evaluation gate (P2.1 public
// half): recall@5 and MRR over a golden.jsonl pair set, threshold sweep,
// noise from the recall log, and the baseline gate — the Python cmd_eval
// port. Real-corpus parity (S5) runs privately on the user's machine; this
// package only ever sees the fixtures it is pointed at.
package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/atomsbaza/my-memory-engine/internal/domain"
	"github.com/atomsbaza/my-memory-engine/internal/index"
	"github.com/atomsbaza/my-memory-engine/internal/schema"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// Pair is one golden retrieval pair: a query and the record ids it must
// surface (any of them counts as a hit, the earliest ranks highest).
type Pair struct {
	Query  string   `json:"query"`
	Expect []string `json:"expect"`
	Origin string   `json:"origin,omitempty"`
	Note   string   `json:"note,omitempty"`
}

// OriginStat is the per-origin breakdown.
type OriginStat struct {
	Recall5 float64 `json:"recall5"`
	MRR     float64 `json:"mrr"`
	N       int     `json:"n"`
}

// Metrics mirrors the Python eval metrics JSON object.
type Metrics struct {
	Recall5        float64            `json:"recall5"`
	MRR            float64            `json:"mrr"`
	Noise          *float64           `json:"noise"`
	NPairs         int                `json:"n_pairs"`
	Stale          int                `json:"stale"`
	AvgInjectChars int                `json:"avg_inject_chars"`
	SweepGE        map[string]int     `json:"sweep_ge"`
	PerOrigin      map[string]OriginStat `json:"per_origin"`
}

// Result carries the run outcome; Fails follows the Python gate rules and
// Verdict is SAVED | FAIL | PASS | EMPTY.
type Result struct {
	Metrics Metrics
	Scoring schema.Scoring
	Fails   []string
	Saved   bool
	Verdict string
}

// Options mirrors the Python eval flags.
type Options struct {
	Days         int  // recall-log window; default 14
	Weights      string
	SaveBaseline bool

	// Dense is the semantic rank seam (embedding mode on; nil = off). When
	// set, every pair's query is embedded once and the dense list fuses
	// into the RRF — the ON/OFF S5 comparison measures exactly this.
	Dense func(query string, limit int) ([]string, error)
}

// echoIDRe mirrors Python ECHO_ID_RE — the "[id: <layer>.<hex>]" tag that
// survives the budget cut inside a rendered recall body.
var echoIDRe = regexp.MustCompile(`\[id: ([a-z]+\.[0-9a-f]{8,16})\]`)

func echoIDs(text string) []string {
	var out []string
	for _, m := range echoIDRe.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	return out
}

func round4(f float64) float64 { return math.Round(f*10000) / 10000 }

// LoadGolden reads eval/golden.jsonl. Malformed rows fail loudly with their
// line number (eval is a CLI-facing gate, not a hook — no silent skips).
func LoadGolden(root string) ([]Pair, error) {
	path := filepath.Join(root, "eval", "golden.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("eval: read %s: %w", path, err)
	}
	var pairs []Pair
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			return nil, fmt.Errorf("eval/golden.jsonl บรรทัด %d: JSON parse ไม่ผ่าน", i+1)
		}
		var q string
		if err := json.Unmarshal(raw["query"], &q); err != nil || strings.TrimSpace(q) == "" {
			return nil, fmt.Errorf("eval/golden.jsonl บรรทัด %d: query ต้องเป็น str ไม่ว่าง", i+1)
		}
		var ex []string
		if err := json.Unmarshal(raw["expect"], &ex); err != nil || len(ex) == 0 {
			return nil, fmt.Errorf("eval/golden.jsonl บรรทัด %d: expect ต้องเป็น list ของ id (str ไม่ว่าง)", i+1)
		}
		for _, e := range ex {
			if strings.TrimSpace(e) == "" {
				return nil, fmt.Errorf("eval/golden.jsonl บรรทัด %d: expect ต้องเป็น list ของ id (str ไม่ว่าง)", i+1)
			}
		}
		p := Pair{Query: q, Expect: ex}
		if v, ok := raw["origin"]; ok {
			_ = json.Unmarshal(v, &p.Origin)
		}
		if v, ok := raw["note"]; ok {
			_ = json.Unmarshal(v, &p.Note)
		}
		pairs = append(pairs, p)
	}
	return pairs, nil
}

// ParseWeights parses a "k=v,k=v" scoring override; unknown keys and
// non-numeric values are loud errors (an A/B typo must not pass silently).
func ParseWeights(override string, base schema.Scoring) (schema.Scoring, error) {
	if strings.TrimSpace(override) == "" {
		return base, nil
	}
	for _, kv := range strings.Split(override, ",") {
		if !strings.Contains(kv, "=") {
			return base, fmt.Errorf("--weights รูปแบบผิด: '%s' (ต้องเป็น k=v คั่นด้วย ,)", kv)
		}
		parts := strings.SplitN(kv, "=", 2)
		k := strings.TrimSpace(parts[0])
		v, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err != nil {
			return base, fmt.Errorf("--weights ค่าไม่ใช่ตัวเลข: '%s'", kv)
		}
		switch k {
		case "w_conf":
			base.WConf = v
		case "w_recency":
			base.WRecency = v
		case "tau_days":
			base.TauDays = v
		case "w_uses":
			base.WUses = v
		case "threshold_hook":
			base.ThresholdHook = v
		default:
			return base, fmt.Errorf("--weights ไม่รู้จัก key: '%s' (ใช้ได้: tau_days, threshold_hook, w_conf, w_recency, w_uses)", k)
		}
	}
	return base, nil
}

// Run evaluates the golden pairs against the current corpus and applies
// the baseline gate. The derived index is used as-is when present (Python
// reads the existing sqlite index); with no index the run degrades to
// scoring + lexical vector ranks, never failing silently.
func Run(root string, o Options) (Result, error) {
	if o.Days <= 0 {
		o.Days = 14
	}
	sch, err := schema.LoadSchema(root)
	if err != nil {
		return Result{}, fmt.Errorf("eval: %w", err)
	}
	sc := sch.Scoring
	if sc == (schema.Scoring{}) {
		sc = domain.DefaultScoring
	}
	sc, err = ParseWeights(o.Weights, sc)
	if err != nil {
		return Result{}, err
	}
	thr := sc.ThresholdHook
	if thr == 0 {
		thr = domain.DefaultScoring.ThresholdHook
	}

	pairs, err := LoadGolden(root)
	if err != nil {
		return Result{}, err
	}
	if len(pairs) == 0 {
		return Result{Verdict: "EMPTY", Scoring: sc}, nil
	}

	recs, err := store.Scan(root)
	if err != nil {
		return Result{}, fmt.Errorf("eval: %w", err)
	}
	status := map[string]string{}
	for _, r := range recs {
		status[r.ID] = r.Status
	}
	var live []Pair
	for _, p := range pairs {
		stalePair := false
		hasExpected := false
		for _, e := range p.Expect {
			if _, ok := status[e]; ok {
				hasExpected = true
				if status[e] == "active" {
					live = append(live, p)
					break
				}
				stalePair = true
			}
		}
		// An expected id that no longer exists at all (e.g. body changed,
		// id drifted) is a miss, not a stale pair — count it in live so the
		// recall regression is observable.
		if !hasExpected && len(p.Expect) > 0 {
			live = append(live, p)
		}
		_ = stalePair
	}
	stale := len(pairs) - len(live)

	var fts func(query string, limit int) ([]string, error)
	if _, statErr := os.Stat(index.IndexPath(root)); statErr == nil {
		ix, oerr := index.Open(index.IndexPath(root))
		if oerr != nil {
			return Result{}, oerr
		}
		defer ix.Close()
		fts = ix.Search
	}

	agg := map[string]*[3]float64{} // hits, rr-sum, n
	metrics := Metrics{SweepGE: map[string]int{"5.0": 0, "6.0": 0, "7.0": 0}, PerOrigin: map[string]OriginStat{}}
	for _, p := range live {
		origin := p.Origin
		if origin == "" {
			origin = "authored"
		}
		a := agg[origin]
		if a == nil {
			a = &[3]float64{}
			agg[origin] = a
		}
		a[2]++

		picked, perr := domain.PickForQuery(recs, domain.RetrievalOptions{
			Query: p.Query, Threshold: thr, Limit: 5, Scoring: &sc, FTS: fts,
			Dense: o.Dense, Indexed: fts != nil,
		})
		if perr != nil {
			return Result{}, perr
		}
		pickedRecs := make([]store.Record, 0, len(picked))
		for _, s := range picked {
			pickedRecs = append(pickedRecs, s.Rec)
		}
		top := echoIDs(domain.RenderRecallBody(pickedRecs, 1400))
		for _, e := range p.Expect {
			if idx := indexOf(top, e); idx >= 0 {
				a[0]++
				a[1] += 1.0 / float64(idx+1)
				break
			}
		}
	}
	n := len(live)
	var totHits, totRR float64
	for _, a := range agg {
		totHits += a[0]
		totRR += a[1]
	}
	if n > 0 {
		metrics.Recall5 = round4(totHits / float64(n))
		metrics.MRR = round4(totRR / float64(n))
	}
	metrics.NPairs = n
	metrics.Stale = stale

	for _, p := range live {
		es := map[string]bool{}
		for _, e := range p.Expect {
			es[e] = true
		}
		picked, perr := domain.PickForQuery(recs, domain.RetrievalOptions{
			Query: p.Query, Threshold: 5.0, Limit: 5, Scoring: &sc, FTS: fts,
			Dense: o.Dense, Indexed: fts != nil,
		})
		if perr != nil {
			return Result{}, perr
		}
		var ss []float64
		for _, s := range picked {
			if es[s.Rec.ID] {
				ss = append(ss, s.Score)
			}
		}
		for _, lvl := range []float64{5.0, 6.0, 7.0} {
			for _, x := range ss {
				if x >= lvl {
					metrics.SweepGE[strconv.FormatFloat(lvl, 'f', 1, 64)]++
					break
				}
			}
		}
	}

	rows := readRecallLog(root, o.Days)
	noise, injected, cited, echoed := evalNoise(rows)
	if noise != nil {
		v := round4(*noise)
		metrics.Noise = &v
	}
	var chars []int
	for _, row := range rows {
		if row["type"] != "inject" {
			continue
		}
		if c, ok := row["chars"].(float64); ok {
			chars = append(chars, int(c))
		}
	}
	if len(chars) > 0 {
		sum := 0
		for _, c := range chars {
			sum += c
		}
		metrics.AvgInjectChars = int(math.Round(float64(sum) / float64(len(chars))))
	}
	_ = injected
	_ = cited
	_ = echoed

	for _, origin := range sortedKeys(agg) {
		a := agg[origin]
		metrics.PerOrigin[origin] = OriginStat{
			Recall5: round4(a[0] / a[2]),
			MRR:     round4(a[1] / a[2]),
			N:       int(a[2]),
		}
	}

	res := Result{Metrics: metrics, Scoring: sc}

	if o.SaveBaseline {
		if err := saveBaseline(root, sc, metrics); err != nil {
			return res, err
		}
		res.Saved = true
		res.Verdict = "SAVED"
		return res, nil
	}

	bp := filepath.Join(root, "eval", "baseline.json")
	if _, statErr := os.Stat(bp); statErr == nil {
		bm, berr := loadBaselineMetrics(bp)
		if berr != nil {
			return res, berr
		}
		if bm.Recall5 != 0 && metrics.Recall5 < bm.Recall5-0.05 {
			res.Fails = append(res.Fails, fmt.Sprintf("recall@5 %v < baseline %v (ตกเกิน 0.05)", metrics.Recall5, bm.Recall5))
		}
		thrM := math.Max(bm.MRR, 0.50)
		if metrics.MRR < thrM-0.05 {
			res.Fails = append(res.Fails, fmt.Sprintf("MRR %v < threshold %v (baseline %v · floor 0.50 · ตกเกิน 0.05)", metrics.MRR, thrM, bm.MRR))
		}
		if metrics.Noise != nil && bm.Noise != nil && *metrics.Noise > *bm.Noise+0.10 {
			res.Fails = append(res.Fails, fmt.Sprintf("noise %v > baseline %v (พุ่งเกิน 0.10)", *metrics.Noise, *bm.Noise))
		}
	} else {
		// Fail-closed: a non-empty golden set without a baseline must not pass.
		res.Fails = append(res.Fails, "ยังไม่มี eval/baseline.json — golden set ไม่ว่าง ต้องมี baseline ก่อน (รัน `mem eval --save-baseline`)")
	}

	if len(res.Fails) > 0 {
		res.Verdict = "FAIL"
	} else {
		res.Verdict = "PASS"
	}
	return res, nil
}

func indexOf(ids []string, id string) int {
	for i, x := range ids {
		if x == id {
			return i
		}
	}
	return -1
}

func sortedKeys(m map[string]*[3]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------------------
// baseline.json
// ---------------------------------------------------------------------------

type baselineDoc struct {
	Date    string        `json:"date"`
	Sha     string        `json:"sha"`
	Scoring schema.Scoring `json:"scoring"`
	Metrics Metrics       `json:"metrics"`
}

func gitSHA(root string) string {
	git, err := exec.LookPath("git")
	if err != nil {
		return "-"
	}
	out, err := exec.Command(git, "-C", root, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "-"
	}
	return strings.TrimSpace(string(out))
}

func saveBaseline(root string, sc schema.Scoring, m Metrics) error {
	dir := filepath.Join(root, "eval")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("eval: create %s: %w", dir, err)
	}
	doc := baselineDoc{
		Date:    time.Now().Format("2006-01-02"),
		Sha:     gitSHA(root),
		Scoring: sc,
		Metrics: m,
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	bp := filepath.Join(dir, "baseline.json")
	if werr := os.WriteFile(bp, append(data, '\n'), 0o644); werr != nil {
		return fmt.Errorf("eval: write %s: %w", bp, werr)
	}
	return nil
}

// LoadBaseline reads eval/baseline.json, failing loud on a broken file and
// fail-closed on a wrong shape (Python NIT-2).
func LoadBaseline(root string) (baselineDoc, error) {
	return loadBaseline(filepath.Join(root, "eval", "baseline.json"))
}

func loadBaselineMetrics(path string) (Metrics, error) {
	doc, err := loadBaseline(path)
	return doc.Metrics, err
}

func loadBaseline(path string) (baselineDoc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return baselineDoc{}, fmt.Errorf("eval/baseline.json parse ไม่ผ่าน — แก้หรือลบก่อนรัน eval: %w", err)
	}
	var doc baselineDoc
	if err := json.Unmarshal(data, &doc); err != nil || doc.Metrics.NPairs == 0 && doc.Metrics.Recall5 == 0 && doc.Metrics.MRR == 0 {
		return baselineDoc{}, fmt.Errorf("eval/baseline.json รูปแบบไม่ถูก — รัน 'mem eval --save-baseline' ใหม่")
	}
	return doc, nil
}

// ---------------------------------------------------------------------------
// recall log — noise (Python read_recall_log + eval_noise)
// ---------------------------------------------------------------------------

type logRow = map[string]any

func readRecallLog(root string, days int) []logRow {
	path := filepath.Join(root, "ops", "recall-log.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	cutoff := time.Now().AddDate(0, 0, -days)
	var rows []logRow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var row logRow
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			continue
		}
		t, ok := row["t"].(string)
		if !ok {
			continue
		}
		ts, err := parseLogTime(t)
		if err != nil || ts.Before(cutoff) {
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

func parseLogTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02T15:04:05", clipTo(s, 19), time.Local)
}

func clipTo(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// evalNoise mirrors Python eval_noise: injected/cited/echoed sets, session
// bucketing for harnesses that do not send a session, noise = 1 −
// used/injected (nil when nothing was injected).
func evalNoise(rows []logRow) (noise *float64, injected, cited, echoed map[string]bool) {
	injected, cited, echoed = map[string]bool{}, map[string]bool{}, map[string]bool{}
	seen := map[string]map[string]bool{}
	for _, row := range rows {
		switch row["type"] {
		case "inject":
			if raw, ok := row["ids"].([]any); ok {
				for _, v := range raw {
					if id, ok := v.(string); ok {
						injected[id] = true
					}
				}
			}
			ses, _ := row["session"].(string)
			ses = strings.TrimSpace(ses)
			if ses == "" {
				agent, _ := row["agent"].(string)
				if agent == "" {
					agent = "-"
				}
				t, _ := row["t"].(string)
				ses = agent + ":" + clipTo(t, 16)
			}
			ph, _ := row["prompt_head"].(string)
			prev := seen[ses]
			if prev == nil {
				prev = map[string]bool{}
				seen[ses] = prev
			}
			for _, m := range echoIDRe.FindAllStringSubmatch(ph, -1) {
				if prev[m[1]] {
					echoed[m[1]] = true
				}
			}
			if raw, ok := row["ids"].([]any); ok {
				for _, v := range raw {
					if id, ok := v.(string); ok {
						prev[id] = true
					}
				}
			}
		case "cite", "show":
			if id, ok := row["id"].(string); ok {
				cited[id] = true
			}
		}
	}
	if len(injected) == 0 {
		return nil, injected, cited, echoed
	}
	used := map[string]bool{}
	for id := range cited {
		if injected[id] {
			used[id] = true
		}
	}
	for id := range echoed {
		if injected[id] {
			used[id] = true
		}
	}
	v := 1.0 - float64(len(used))/float64(len(injected))
	return &v, injected, cited, echoed
}
