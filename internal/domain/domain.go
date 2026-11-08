// Package domain implements the record lifecycle and retrieval core (P2.1):
// add with write guards and near-duplicate thresholds, supersede, cite,
// review (the only quarantine exit), verify, expiry, scoring, selection and
// the context/recall renderers with budget semantics.
//
// Reference: ~/my-memory/bin/mem — cmd_add, cmd_supersede, cmd_cite,
// cmd_review, cmd_verify, cmd_context, score, select_from, pick_for_query,
// render_recall_body, render_block. Output contracts are preserved as data:
// a refusal carries an *Error whose Code maps onto the Python CLI exit
// contract (1 = usage/data, 2 = guard, 3 = conflict), warnings travel on
// AddResult / Verify results, and write/commit failures stay observable
// (record persisted + Committed=false + CommitReason + one runtime-log
// line) instead of being swallowed.
//
// Divergences from Python, by design:
//   - add --force cannot overwrite an existing id: the engine store refuses
//     blind overwrites (P1.4); supersede is the replacement path.
//   - reindex failures after a successful write are returned as errors that
//     name the persisted file (Python traceays mid-command).
package domain

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/atomsbaza/my-memory-engine/internal/codec"
	"github.com/atomsbaza/my-memory-engine/internal/index"
	"github.com/atomsbaza/my-memory-engine/internal/schema"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// ---------------------------------------------------------------------------
// Error / exit contract
// ---------------------------------------------------------------------------

// Error is a domain refusal mapped onto the Python CLI exit contract.
type Error struct {
	Code int // 1 = usage/data error, 2 = guard refusal, 3 = conflict
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func errf(code int, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// AsError reports whether err is a domain refusal and returns its exit code.
func AsError(err error) (code int, ok bool) {
	var de *Error
	if ok = errors.As(err, &de); ok {
		code = de.Code
	}
	return
}

// timeNow is the clock seam: tests pin it for deterministic expiry,
// recency and date-normalisation behaviour.
var timeNow = time.Now

func today() string { return timeNow().Format("2006-01-02") }

// ---------------------------------------------------------------------------
// Add — guards, dedup thresholds, supersede-on-add (Python cmd_add)
// ---------------------------------------------------------------------------

// dedup thresholds (Python cmd_add): >=0.88 blocks, the 0.60–0.88 band warns.
const (
	DedupBlockAt = 0.88
	DedupWarnAt  = 0.60
)

// AddOptions mirrors the Python `mem add` flags. Zero values become the
// argparse defaults: Source "agent:unknown", Confidence "0.9", Decay "1.0",
// Expires "none", Sensitivity "internal". Confidence and Decay stay strings
// so the written front-matter keeps the caller's exact bytes (Python stores
// the raw strings too).
type AddOptions struct {
	ID          string
	Title       string
	Tags        []string
	Confidence  string // default "0.9"
	Decay       string // default "1.0"; must parse as float
	HalfLife    string
	Source      string // default "agent:unknown"
	Session     string
	Evidence    []string
	Supersedes  string // explicit old record id
	Supersede   bool   // auto-supersede the found duplicate
	Created     string // ISO date; default today
	Expires     string // none | YYYY-MM-DD | +21d | +2w | +3m
	Sensitivity string // default "internal"
	Force       bool
	// NoCommit writes the record without the local git commit (the CLI's
	// `mem add --no-commit`). The record still lands on disk.
	NoCommit bool
}

func (o AddOptions) withDefaults() AddOptions {
	if o.Confidence == "" {
		o.Confidence = "0.9"
	}
	if o.Decay == "" {
		o.Decay = "1.0"
	}
	if o.Source == "" {
		o.Source = "agent:unknown"
	}
	if o.Expires == "" {
		o.Expires = "none"
	}
	if o.Sensitivity == "" {
		o.Sensitivity = "internal"
	}
	return o
}

// AddResult reports the outcome of Add. Committed==false always carries a
// non-empty CommitReason; the record itself is already on disk.
type AddResult struct {
	ID           string
	Rel          string
	Path         string
	Committed    bool
	CommitReason string // non-empty iff !Committed
	Superseded   string // old record id superseded by this add, "" when none
	Warnings     []string
}

// addTitleRe strips a leading "**Bold.** " marker from the first body line,
// mirroring the Python title derivation in cmd_add.
var addTitleRe = regexp.MustCompile(`^\*\*[^*]{1,24}\.\*\*\s*`)

// deriveAddTitle mirrors Python cmd_add: first body line, bold prefix
// stripped, leading "*# " trimmed, at most 80 runes.
func deriveAddTitle(body string) string {
	line := strings.TrimSpace(body)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = addTitleRe.ReplaceAllString(line, "")
	line = strings.TrimLeft(line, "*# ")
	line = strings.TrimSpace(line)
	runes := []rune(line)
	if len(runes) > 80 {
		runes = runes[:80]
	}
	return string(runes)
}

// Add persists one record under root: layer/scope validation, write guards
// (secret / absolute-path / PII → *Error{2}), source screening rules,
// duplicate-id refusal, near-duplicate thresholds (>=0.88 block as
// *Error{3}; 0.60–0.88 warn via AddResult.Warnings), the lesson→knowledge
// canonical warning, expires normalisation, then the store write and local
// commit. The write/commit failure contract follows store.WriteRecord.
func Add(root string, rec store.Record, opts AddOptions) (AddResult, error) {
	opts = opts.withDefaults()
	sch, err := schema.LoadSchema(root)
	if err != nil {
		return AddResult{}, fmt.Errorf("domain: add: %w", err)
	}

	body := strings.TrimSpace(rec.Body)
	if body == "" {
		return AddResult{}, errf(1, "เนื้อหาว่าง — ส่งทาง stdin ด้วย --file -")
	}
	body += "\n"

	layer := rec.Layer
	if layer == "" {
		layer = strField(rec.Front, "layer")
	}
	def, ok := sch.Layers[layer]
	if !ok {
		return AddResult{}, errf(1, "layer ไม่รู้จัก: %s (เลือก: %s)", layer, strings.Join(sortedLayerNames(sch.Layers), ", "))
	}
	if def.FileMode == "append-only" {
		return AddResult{}, errf(1, "layer %s เป็น log แบบ append-only — ไม่ผ่าน mem add ให้ append บรรทัดตรง ๆ ใน %s/", layer, def.Dir)
	}

	scope := rec.Scope
	if scope == "" {
		scope = strField(rec.Front, "scope")
	}
	if !sch.ValidScopes.MatchString(scope) {
		return AddResult{}, errf(1, "scope ต้องเป็น global | project:<Name> | repo:<owner/name> | agent:<name>")
	}

	title := opts.Title
	if title == "" {
		title = deriveAddTitle(body)
	}

	// Guards: secret / machine path / PII refuse (exit 2); prompt-injection
	// look-alikes only warn — same order and wording as Python guard().
	guardText := title + "\n" + body
	if gerr := store.GuardWith(&sch, guardText); gerr != nil {
		return AddResult{}, &Error{Code: 2, Msg: gerr.Error()}
	}
	var warnings []string
	if inj := injectionHits(guardText); len(inj) > 0 {
		warnings = append(warnings, fmt.Sprintf("เตือน — ตรวจพบข้อความคล้าย prompt injection ('%s')", inj[0]))
	}

	if strings.HasPrefix(opts.Source, "external:") && len(opts.Evidence) == 0 {
		return AddResult{}, errf(3, "source 'external:*' คือข้อมูลที่ยังไม่ผ่านการ screen — screen ก่อน แล้ว add ใหม่ด้วย "+
			"--evidence <screened-by> (หรือเปลี่ยน source ให้ตรงความจริง)")
	}

	recID := opts.ID
	if recID != "" && !sch.IDPattern.MatchString(recID) {
		return AddResult{}, errf(1, "--id ต้องเป็น <layer>.<hex 8-16>")
	}
	if recID == "" {
		recID = codec.MakeID(layer, scope, title, body)
	}

	existingSlice, err := store.Scan(root)
	if err != nil {
		return AddResult{}, fmt.Errorf("domain: add: %w", err)
	}
	existing := make(map[string]store.Record, len(existingSlice))
	for _, r := range existingSlice {
		existing[r.ID] = r
	}
	if prev, seen := existing[recID]; seen && !opts.Force {
		return AddResult{}, errf(3, "id นี้มีอยู่แล้ว: %s (%s) — ถ้าจะแก้ของเดิมให้ --force, ถ้าเป็นเรื่องใหม่ให้ตั้ง --id อื่น", recID, prev.Rel)
	}

	// Near-duplicate pass: same layer+scope+active only; >=0.88 blocks on
	// the first hit, the warn band keeps the closest one (Python N3).
	cand := Tokens(CoreText(title, body))
	var dupRec *store.Record
	var dupSim float64
	var nearRec *store.Record
	var nearSim float64
	for i := range existingSlice {
		r := existingSlice[i]
		if r.ID == recID || r.Layer != layer || r.Scope != scope || r.Status != "active" {
			continue
		}
		sim := Jaccard(cand, Tokens(CoreText(r.Title, r.Body)))
		if sim >= DedupBlockAt {
			dupRec = &existingSlice[i]
			dupSim = sim
			break
		}
		if sim >= DedupWarnAt && (nearRec == nil || sim > nearSim) {
			nearRec = &existingSlice[i]
			nearSim = sim
		}
	}
	override := opts.Force || opts.Supersede || opts.Supersedes != ""
	if dupRec != nil && !override {
		return AddResult{}, errf(3, "ดูซ้ำกับ %s (%s) similarity=%.2f — ถ้าเป็นเรื่องเดียวกันให้ใช้ --supersede "+
			"(แทนที่ของที่ซ้ำ) หรือ --supersedes <id> (ระบุของเดิมเอง)", dupRec.ID, dupRec.Rel, dupSim)
	}
	if nearRec != nil && !override {
		warnings = append(warnings, fmt.Sprintf("เตือน — คล้าย %s (%s) similarity=%.2f (โดน block ที่ ≥0.88 · band นี้แค่เตือน)",
			nearRec.ID, nearRec.Rel, nearSim))
	}
	if layer == "lesson" {
		// Cross-layer canonical check: watch the lesson→knowledge promote band.
		for i := range existingSlice {
			r := existingSlice[i]
			if r.Layer != "knowledge" || r.Status != "active" {
				continue
			}
			if csim := Jaccard(cand, Tokens(CoreText(r.Title, r.Body))); csim >= DedupWarnAt {
				warnings = append(warnings, fmt.Sprintf("เตือน — คล้าย %s (canonical) — พิจารณา --supersedes %s หรือเขียนเป็น pointer",
					r.ID, r.ID))
				break
			}
		}
	}
	if opts.Supersede && dupRec == nil && opts.Supersedes == "" {
		warnings = append(warnings, "เตือน — ไม่พบของซ้ำให้แทนที่ (ถ้าต้องการชี้ของเดิมเองให้ใช้ --supersedes <id>)")
	}

	expires, eerr := NormExpires(opts.Expires, timeNow())
	if eerr != nil {
		return AddResult{}, errf(1, "%s", eerr.Error())
	}
	if def.ExpiresRequired && expires == "none" {
		return AddResult{}, errf(1, "layer %s ต้องมี --expires (เช่น +21d หรือ YYYY-MM-DD)", layer)
	}
	decayF, derr := parseFloatStrict(opts.Decay)
	if derr != nil {
		return AddResult{}, errf(1, "--decay ต้องเป็นตัวเลข (ได้มา: %s)", opts.Decay)
	}
	confF, cerr := parseFloatStrict(opts.Confidence)
	if cerr != nil || confF < 0 || confF > 1 {
		return AddResult{}, errf(1, "--confidence ต้องเป็นตัวเลขในช่วง [0,1] (ได้มา: %s)", opts.Confidence)
	}

	var target *store.Record
	if opts.Supersedes != "" {
		t, found := existing[opts.Supersedes]
		if !found {
			return AddResult{}, errf(1, "ไม่พบ record ที่จะ supersede: %s", opts.Supersedes)
		}
		target = &t
	} else if opts.Supersede && dupRec != nil {
		target = dupRec
	}

	// Front-matter mirrors the Python meta dict (None values dropped).
	fm := codec.Frontmatter{
		"id":          recID,
		"title":       title,
		"layer":       layer,
		"scope":       scope,
		"tags":        opts.Tags,
		"confidence":  confF,
		"decay":       decayF,
		"source":      opts.Source,
		"evidence":    opts.Evidence,
		"created":     orDefault(opts.Created, today()),
		"last_used":   today(),
		"expires":     expires,
		"status":      "active",
		"sensitivity": opts.Sensitivity,
	}
	if opts.HalfLife != "" {
		fm["half_life"] = opts.HalfLife
	}
	if opts.Session != "" {
		fm["session"] = opts.Session
	}
	if target != nil {
		fm["supersedes"] = target.ID
	}

	wr, werr := store.WriteRecordWith(root, store.Record{Front: fm, Body: body}, store.WriteOptions{NoCommit: opts.NoCommit})
	if werr != nil {
		return AddResult{}, werr
	}
	res := AddResult{
		ID:           recID,
		Rel:          wr.Rel,
		Path:         wr.Path,
		Committed:    wr.Committed,
		CommitReason: wr.CommitReason,
		Warnings:     warnings,
	}

	if target != nil {
		oldFM := cloneFront(target.Front)
		oldFM["status"] = "superseded"
		oldFM["superseded_by"] = recID
		if rerr := rewriteRecord(target.Path, target.Body, oldFM); rerr != nil {
			return res, fmt.Errorf("domain: add %s persisted but supersede rewrite of %s failed: %w", recID, target.ID, rerr)
		}
		res.Superseded = target.ID
	}
	if rerr := ReindexRoot(root); rerr != nil {
		return res, fmt.Errorf("domain: add %s persisted at %s but reindex failed: %w", recID, wr.Rel, rerr)
	}
	return res, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func parseFloatStrict(s string) (float64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("not a finite number: %s", s)
	}
	return f, nil
}

// sortedLayerNames lists known layers alphabetically (refusal messages).
func sortedLayerNames(layers map[string]schema.LayerDef) []string {
	names := make([]string, 0, len(layers))
	for name := range layers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// openIndex opens the engine's derived index for root. ReindexRoot and the
// selection path share this entry point; index.Open itself refuses the
// Python engine's index.sqlite (REQ-IDX-01).
func openIndex(root string) (*index.Index, error) {
	ix, err := index.Open(index.IndexPath(root))
	if err != nil {
		return nil, fmt.Errorf("reindex: %w", err)
	}
	return ix, nil
}

// strField mirrors store's accessor without exporting it: front-matter
// string view, "" when absent or non-string.
func strField(fm codec.Frontmatter, key string) string {
	s, _ := fm[key].(string)
	return s
}

func cloneFront(fm codec.Frontmatter) codec.Frontmatter {
	out := make(codec.Frontmatter, len(fm))
	for k, v := range fm {
		out[k] = v
	}
	return out
}

// rewriteRecord rewrites one existing record file in place with updated
// front-matter, atomically (Python Rec.rewrite is a plain write; the engine
// keeps the atomic guarantee). The caller owns locking.
func rewriteRecord(path, body string, fm codec.Frontmatter) error {
	text := codec.DumpFront(fm) + "\n" + strings.TrimLeft(body, "\n")
	return store.AtomicWrite(path, []byte(text))
}

// ReindexRoot rebuilds the engine's derived index from the canonical
// markdown tree (Python reindex(quiet=True)). Failures are returned, never
// swallowed — the index is derived but a broken rebuild must be visible.
func ReindexRoot(root string) error {
	rep, err := store.ScanWithReport(root)
	if err != nil {
		return fmt.Errorf("reindex: %w", err)
	}
	ix, err := openIndex(root)
	if err != nil {
		return err
	}
	defer ix.Close()
	return ix.Rebuild(rep.Records)
}

// rstripRunes trims trailing whitespace (Python str.rstrip()).
func rstripRunes(s string) string {
	return strings.TrimRightFunc(s, unicode.IsSpace)
}

// clipRunes returns the first n runes of s (Python s[:n]).
func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n])
}

// rlen counts runes (Python len() over str).
func rlen(s string) int { return utf8.RuneCountInString(s) }

// collapseSpace joins whitespace-split fields with single spaces (Python
// " ".join(s.split())).
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
