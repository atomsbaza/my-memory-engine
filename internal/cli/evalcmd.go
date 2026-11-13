// `mem eval` — the golden-corpus retrieval gate (Python cmd_eval): recall@5,
// MRR, noise from the recall log, threshold sweep, and the baseline comparison.
// Metrics come from internal/eval; this handler owns the flags, the recall-log
// derived numbers and the text/JSON output contract.
package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/atomsbaza/my-memory-engine/internal/eval"
)

func cmdEval(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	days := fs.Int("days", 14, "window ของ recall log")
	asJSON := fs.Bool("json", false, "พิมพ์เป็น JSON document เดียว")
	saveBaseline := fs.Bool("save-baseline", false, "บันทึกผลรอบนี้เป็น baseline")
	fromLog := fs.Bool("from-log", false, "เสนอคู่ใหม่จาก log (dry-run — ไม่เขียน golden เอง)")
	weights := fs.String("weights", "", `override scoring ของรอบนี้ เช่น w_recency=0,w_uses=0`)
	if _, code, stop := e.parseArgs(c, fs, argv); stop {
		return code
	}
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	// Dense seam: nil in off mode — the OFF eval run is the exact
	// pre-embedding retrieval. A model mismatch is loud: the gate must
	// never read stale dense ranks as a PASS.
	sch, err := e.schemaOf(root)
	if err != nil {
		return e.fail(err)
	}
	dense, denseCloser, derr := e.denseSeam(root, sch.Embedding.WithDefaults())
	if derr != nil {
		return e.fail(derr)
	}
	defer denseCloser()

	rows := readRecallRows(root, *days)
	injected, cited, echoed := noiseSets(rows)

	if *fromLog {
		return e.proposeFromLog(rows, injected, cited, echoed)
	}

	res, err := eval.Run(root, eval.Options{Days: *days, Weights: *weights, SaveBaseline: *saveBaseline, Dense: dense})
	if err != nil {
		return e.fail(err)
	}
	if res.Verdict == "EMPTY" {
		fmt.Fprintln(e.Stdout, "golden set ว่าง — รอ bootstrap (วิธีเขียนคู่แรกอยู่ใน README 'Evaluation loop')")
		return ExitOK
	}

	metrics := res.Metrics
	noise := metrics.Noise
	if *asJSON {
		payload := map[string]any{
			"metrics": metrics,
			"scoring": res.Scoring,
			"verdict": res.Verdict,
		}
		enc := json.NewEncoder(e.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		if err := enc.Encode(payload); err != nil {
			return e.fail(err)
		}
		if len(res.Fails) > 0 {
			return ExitError
		}
		return ExitOK
	}

	header := fmt.Sprintf("mem eval — golden %d pairs", metrics.NPairs)
	if metrics.Stale > 0 {
		header += fmt.Sprintf(" (ข้าม stale %d)", metrics.Stale)
	}
	if metrics.AbstentionTotal > 0 {
		header += fmt.Sprintf(" · abstention %d", metrics.AbstentionTotal)
	}
	fmt.Fprintf(e.Stdout, "%s · window %dd\n", header, *days)
	fmt.Fprintf(e.Stdout, "recall@5 = %v\n", metrics.Recall5)
	if metrics.AbstentionTotal > 0 {
		fmt.Fprintf(e.Stdout, "abstention = %v (%d/%d no-answer คู่ต้องไม่ match)\n",
			metrics.AbstentionPrecision, metrics.AbstentionPass, metrics.AbstentionTotal)
	}
	fmt.Fprintf(e.Stdout, "MRR = %v\n", metrics.MRR)
	origins := make([]string, 0, len(metrics.PerOrigin))
	for o := range metrics.PerOrigin {
		origins = append(origins, o)
	}
	sort.Strings(origins)
	for _, o := range origins {
		stat := metrics.PerOrigin[o]
		fmt.Fprintf(e.Stdout, "  %s: recall@5 %v · MRR %v (n=%d)\n", o, stat.Recall5, stat.MRR, stat.N)
	}
	if noise == nil {
		fmt.Fprintln(e.Stdout, "noise rate = n/a (ยังไม่มี inject rows ใน ops/recall-log.jsonl)")
	} else {
		fmt.Fprintf(e.Stdout, "noise rate = %v (injected=%d cited=%d echoed=%d · window %dd)\n",
			*noise, len(injected), len(cited), len(echoed), *days)
	}
	fmt.Fprintf(e.Stdout, "avg inject chars = %d\n", metrics.AvgInjectChars)
	fmt.Fprintf(e.Stdout, "threshold sweep: >=5.0 %d/%d · >=6.0 %d/%d · >=7.0 %d/%d\n",
		metrics.SweepGE["5.0"], metrics.NPairs,
		metrics.SweepGE["6.0"], metrics.NPairs,
		metrics.SweepGE["7.0"], metrics.NPairs)

	if res.Saved {
		fmt.Fprintln(e.Stdout, "saved baseline -> eval/baseline.json")
		return ExitOK
	}
	if len(res.Fails) > 0 {
		fmt.Fprintln(e.Stdout, "VERDICT: FAIL — "+strings.Join(res.Fails, " · "))
		return ExitError
	}
	fmt.Fprintln(e.Stdout, "VERDICT: PASS (เทียบ baseline แล้วไม่ตกเกณฑ์)")
	return ExitOK
}

// proposeFromLog prints candidate golden pairs from the recall log — always a
// dry run: golden is never written by the engine (no golden poisoning).
func (e *Env) proposeFromLog(rows []logRow, injected, cited, echoed map[string]bool) int {
	usedIDs := map[string]bool{}
	for id := range cited {
		if injected[id] {
			usedIDs[id] = true
		}
	}
	for id := range echoed {
		if injected[id] {
			usedIDs[id] = true
		}
	}
	var proposals []map[string]any
	seenHash := map[string]bool{}
	for _, row := range rows {
		if strOf(row["type"]) != "inject" {
			continue
		}
		hash := strOf(row["prompt_hash"])
		if hash == "" || seenHash[hash] {
			continue
		}
		var used []string
		if ids, ok := row["ids"].([]any); ok {
			for _, raw := range ids {
				if id, ok := raw.(string); ok && usedIDs[id] {
					used = append(used, id)
				}
			}
		}
		if len(used) == 0 {
			continue
		}
		seenHash[hash] = true
		proposals = append(proposals, map[string]any{
			"query":  strOf(row["prompt_head"]),
			"expect": used,
			"origin": "cited",
			"note":   "จาก log " + strOf(row["t"]),
		})
	}
	fmt.Fprintf(e.Stdout, "เสนอ %d คู่ใหม่จาก log (dry-run — approve เป็นรายคู่ แล้วเขียน eval/golden.jsonl เอง):\n", len(proposals))
	enc := json.NewEncoder(e.Stdout)
	enc.SetEscapeHTML(false)
	for _, p := range proposals {
		if err := enc.Encode(p); err != nil {
			return e.fail(err)
		}
	}
	return ExitOK
}

// logRow is one recall-log line (all values weakly typed: a malformed but valid
// JSON row must be skipped, never crash the command).
type logRow = map[string]any

// readRecallRows reads ops/recall-log.jsonl within the --days window. A
// missing/unreadable/unparsable log is an empty window (noise reported n/a),
// never a crash — matching the Python read_recall_log contract.
func readRecallRows(root string, days int) []logRow {
	path := filepath.Join(root, "ops", "recall-log.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	cutoff := time.Now().AddDate(0, 0, -days)
	var rows []logRow
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var row logRow
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			continue
		}
		ts, ok := parseLogTime(strOf(row["t"]))
		if !ok || ts.Before(cutoff) {
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

func parseLogTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	clipped := s
	if len(clipped) > 19 {
		clipped = clipped[:19]
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04:05", clipped, time.Local); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// noiseSets mirrors Python eval_noise: which ids were injected, which were
// cited/shown, and which were echoed back inside a later prompt of the same
// session.
func noiseSets(rows []logRow) (injected, cited, echoed map[string]bool) {
	injected, cited, echoed = map[string]bool{}, map[string]bool{}, map[string]bool{}
	seen := map[string]map[string]bool{}
	for _, row := range rows {
		switch strOf(row["type"]) {
		case "inject":
			var ids []string
			if raw, ok := row["ids"].([]any); ok {
				for _, v := range raw {
					if id, ok := v.(string); ok {
						ids = append(ids, id)
					}
				}
			}
			for _, id := range ids {
				injected[id] = true
			}
			session := strings.TrimSpace(strOf(row["session"]))
			if session == "" {
				t := strOf(row["t"])
				if len(t) > 16 {
					t = t[:16]
				}
				session = strOf(row["agent"]) + ":" + t
			}
			prev := seen[session]
			if prev == nil {
				prev = map[string]bool{}
				seen[session] = prev
			}
			for _, m := range echoIDRe.FindAllStringSubmatch(strOf(row["prompt_head"]), -1) {
				if prev[m[1]] {
					echoed[m[1]] = true
				}
			}
			for _, id := range ids {
				prev[id] = true
			}
		case "cite", "show":
			if id := strOf(row["id"]); id != "" {
				cited[id] = true
			}
		}
	}
	return injected, cited, echoed
}

// strOf reads a string value from a weakly-typed log row ("" when absent or of
// another type).
func strOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
