// Maintenance operations beyond the record lifecycle: the decay/archival
// scheduler (Archive), lesson→rule promotion (Promote) and sleep-time handoff
// compaction (Compact) — the Python cmd_archive / cmd_promote / cmd_compact
// ports (bin/mem:1242, 1269, 1394).
//
// All three keep the baseline's two-phase shape: plan first — id collisions and
// write guards resolve before the first byte is written, so a refusal cannot
// leave half a run on disk — then mutate under the store flock and rebuild the
// derived index. Rendering stays in the entrypoints (the MCP tools print the
// baseline's exact lines); these functions return data.
//
// Divergence from Python, by design: a collision between two records planned in
// the same run is refused before any write. The baseline only checks planned ids
// against the records already on disk, so two candidates with identical content
// would write two records carrying one id; the engine refuses that loudly.
package domain

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/atomsbaza/my-memory-engine/internal/codec"
	"github.com/atomsbaza/my-memory-engine/internal/schema"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// ---------------------------------------------------------------------------
// Archive — decay/archival scheduler (Python cmd_archive)
// ---------------------------------------------------------------------------

// ArchiveOptions mirrors the `mem archive` flags. Threshold is used verbatim;
// the baseline's 0.15 default belongs to the caller (the MCP layer applies it
// only when the argument is absent, so an explicit 0 keeps meaning "nothing
// qualifies").
type ArchiveOptions struct {
	Threshold float64
	DryRun    bool
}

// ArchiveCandidate is one active record whose retention score is below the
// threshold, in the baseline's (score, id) order.
type ArchiveCandidate struct {
	ID    string
	Layer string
	Scope string
	Rel   string
	Score float64
}

// ArchiveMove is one record that moved into _archive/<layer>/.
type ArchiveMove struct {
	ID    string
	Score float64
	Dest  string // path relative to the memory root
}

// ArchiveResult reports one archive run: every candidate found, and the moves
// actually performed (empty on a dry run).
type ArchiveResult struct {
	Threshold  float64
	DryRun     bool
	Active     int
	Candidates []ArchiveCandidate
	Moved      []ArchiveMove
}

// Archive moves every active record whose retention score is below
// opts.Threshold into _archive/<layer>/, stamping status=archived and
// archived_at first so the front-matter travels with the file. Archived files
// sit under a "_" path, which the scanner skips — they stop being records.
func Archive(root string, o ArchiveOptions) (ArchiveResult, error) {
	res := ArchiveResult{Threshold: o.Threshold, DryRun: o.DryRun}
	err := store.WithLock(root, func() error {
		recs, err := store.Scan(root)
		if err != nil {
			return fmt.Errorf("domain: archive: %w", err)
		}
		active := make([]store.Record, 0, len(recs))
		for _, r := range recs {
			if r.Status == "active" {
				active = append(active, r)
			}
		}
		res.Active = len(active)

		type scored struct {
			rec   store.Record
			score float64
		}
		var cands []scored
		for _, r := range active {
			s := RetentionScore(r)
			if s < o.Threshold {
				cands = append(cands, scored{rec: r, score: s})
			}
		}
		sort.Slice(cands, func(i, j int) bool {
			if cands[i].score != cands[j].score {
				return cands[i].score < cands[j].score
			}
			return cands[i].rec.ID < cands[j].rec.ID
		})
		for _, c := range cands {
			res.Candidates = append(res.Candidates, ArchiveCandidate{
				ID: c.rec.ID, Layer: c.rec.Layer, Scope: c.rec.Scope,
				Rel: c.rec.Rel, Score: c.score,
			})
		}
		if o.DryRun || len(cands) == 0 {
			return nil
		}

		for _, c := range cands {
			m := cloneFront(c.rec.Front)
			m["status"] = "archived"
			m["archived_at"] = today()
			// Python rewrites the front-matter at the old path and then moves
			// the file, so the archive copy is the rewritten one.
			if werr := rewriteRecord(c.rec.Path, c.rec.Body, m); werr != nil {
				return fmt.Errorf("domain: archive %s: %w", c.rec.ID, werr)
			}
			dest := uniquePath(filepath.Join(root, "_archive", c.rec.Layer, filepath.Base(c.rec.Path)))
			if derr := os.MkdirAll(filepath.Dir(dest), 0o755); derr != nil {
				return fmt.Errorf("domain: archive %s: %w", c.rec.ID, derr)
			}
			if merr := os.Rename(c.rec.Path, dest); merr != nil {
				return fmt.Errorf("domain: archive %s: %w", c.rec.ID, merr)
			}
			rel, rerr := filepath.Rel(root, dest)
			if rerr != nil {
				return fmt.Errorf("domain: archive %s: %w", c.rec.ID, rerr)
			}
			res.Moved = append(res.Moved, ArchiveMove{ID: c.rec.ID, Score: c.score, Dest: rel})
		}
		if rerr := ReindexRoot(root); rerr != nil {
			return fmt.Errorf("domain: archive moved %d records but reindex failed: %w", len(res.Moved), rerr)
		}
		return nil
	})
	return res, err
}

// ---------------------------------------------------------------------------
// Promote — proven lessons become rules (Python cmd_promote)
// ---------------------------------------------------------------------------

// PromoteOptions mirrors the `mem promote` flags. MinUses 0 becomes the
// baseline default 3 (Python int(a.get("min_uses") or 3) — only a falsy 0
// takes the default, a negative value is passed through like the baseline).
type PromoteOptions struct {
	MinUses int
	Force   bool // consider uses only, not the successes==uses proof
	DryRun  bool
}

// PromoteCandidate is one lesson the promotion pass selected.
type PromoteCandidate struct {
	ID        string
	Uses      int
	Successes int
	Scope     string
	Rel       string
}

// PromoteMove is one lesson promoted into a rule record.
type PromoteMove struct {
	From string // lesson id
	To   string // rule id
	Rel  string // path of the new rule relative to the memory root
}

// PromoteResult reports one promotion run.
type PromoteResult struct {
	MinUses    int
	Mode       string // "strict (successes==uses>0)" | "force (uses only)"
	DryRun     bool
	Candidates []PromoteCandidate
	Promoted   []PromoteMove
}

// Promote turns every lesson that proved itself — by default successes > 0 and
// successes == uses, otherwise uses >= MinUses alone — into a rule, and marks
// the lesson status=promoted with promoted_to set. Rules are written from the
// lesson's title and body; tags, source and sensitivity carry over.
func Promote(root string, o PromoteOptions) (PromoteResult, error) {
	if o.MinUses == 0 {
		o.MinUses = 3
	}
	res := PromoteResult{MinUses: o.MinUses, Mode: promoteMode(o.Force), DryRun: o.DryRun}
	err := store.WithLock(root, func() error {
		sch, err := schema.LoadSchema(root)
		if err != nil {
			return fmt.Errorf("domain: promote: %w", err)
		}
		recs, err := store.Scan(root)
		if err != nil {
			return fmt.Errorf("domain: promote: %w", err)
		}
		ids := make(map[string]bool, len(recs))
		for _, r := range recs {
			ids[r.ID] = true
		}

		var cands []store.Record
		for _, r := range recs {
			if r.Layer != "lesson" || r.Status != "active" ||
				r.Sensitivity == "quarantine" || r.Uses < o.MinUses {
				continue
			}
			if !o.Force && !(r.Successes > 0 && r.Successes == r.Uses) {
				continue
			}
			cands = append(cands, r)
			res.Candidates = append(res.Candidates, PromoteCandidate{
				ID: r.ID, Uses: r.Uses, Successes: r.Successes, Scope: r.Scope, Rel: r.Rel,
			})
		}
		if o.DryRun || len(cands) == 0 {
			return nil
		}

		// Plan phase: every guard and id check runs before the first write.
		type plan struct {
			rec  store.Record
			id   string
			fm   codec.Frontmatter
			slug string
		}
		ruleDir := sch.Layers["rule"].Dir
		if ruleDir == "" {
			return fmt.Errorf("domain: promote: the schema declares no rule layer directory — fix schema/taxonomy.json")
		}
		var plans []plan
		planned := make(map[string]string, len(cands))
		for _, r := range cands {
			title := r.TitleOrDerived()
			newID := codec.MakeID("rule", r.Scope, title, r.Body)
			if ids[newID] {
				return errf(3, "ข้าม %s — มี rule ที่ id เดียวกันอยู่แล้ว: %s (ตรวจด้วย `mem show %s`)", r.ID, newID, newID)
			}
			if other, dup := planned[newID]; dup {
				return errf(3, "ข้าม %s — rule id %s ซ้ำกับ %s ที่จะสร้างในรอบเดียวกัน (lesson สองตัวเนื้อหาเดียวกัน — รวมก่อน promote)",
					r.ID, newID, other)
			}
			planned[newID] = r.ID
			if gerr := store.GuardWith(&sch, title+"\n"+r.Body); gerr != nil {
				return &Error{Code: 2, Msg: gerr.Error()}
			}
			source := strField(r.Front, "source")
			if source == "" {
				source = "agent:mem"
			}
			sensitivity := r.Sensitivity
			if sensitivity == "" {
				sensitivity = "internal"
			}
			fm := codec.Frontmatter{
				"id": newID, "title": title, "layer": "rule", "scope": r.Scope,
				"tags": append([]string{}, r.Tags...), "decay": 1.0,
				"source": source, "promoted_from": r.ID,
				"created": today(), "last_used": today(),
				"status": "active", "sensitivity": sensitivity,
			}
			plans = append(plans, plan{rec: r, id: newID, fm: fm, slug: codec.Slugify(title)})
		}

		for _, p := range plans {
			dest := uniquePath(filepath.Join(root, ruleDir, p.slug+".md"))
			if derr := os.MkdirAll(filepath.Dir(dest), 0o755); derr != nil {
				return fmt.Errorf("domain: promote %s: %w", p.rec.ID, derr)
			}
			text := codec.DumpFront(p.fm) + "\n" + p.rec.Body
			if werr := store.AtomicWrite(dest, []byte(text)); werr != nil {
				return fmt.Errorf("domain: promote %s: %w", p.rec.ID, werr)
			}
			lm := cloneFront(p.rec.Front)
			lm["status"] = "promoted"
			lm["promoted_to"] = p.id
			if werr := rewriteRecord(p.rec.Path, p.rec.Body, lm); werr != nil {
				return fmt.Errorf("domain: promote %s (rule written at %s): %w", p.rec.ID, dest, werr)
			}
			rel, rerr := filepath.Rel(root, dest)
			if rerr != nil {
				return fmt.Errorf("domain: promote %s: %w", p.rec.ID, rerr)
			}
			res.Promoted = append(res.Promoted, PromoteMove{From: p.rec.ID, To: p.id, Rel: rel})
		}
		if rerr := ReindexRoot(root); rerr != nil {
			return fmt.Errorf("domain: promote wrote %d rules but reindex failed: %w", len(res.Promoted), rerr)
		}
		return nil
	})
	return res, err
}

// promoteMode names the selection rule the run used (Python mem's mode label).
func promoteMode(force bool) string {
	if force {
		return "force (uses only)"
	}
	return "strict (successes==uses>0)"
}

// ---------------------------------------------------------------------------
// Compact — sleep-time handoff compaction (Python cmd_compact)
// ---------------------------------------------------------------------------

// CompactOptions mirrors the `mem compact` flags. A zero Days/MinGroup becomes
// the baseline default 7 / 3 (Python int(a.get("days") or 7): only a falsy 0
// takes the default).
type CompactOptions struct {
	Days     int
	MinGroup int
	DryRun   bool
}

// CompactGroup is one scope's batch of old handoffs (ids sorted).
type CompactGroup struct {
	Scope string
	IDs   []string
}

// CompactWritten is one knowledge record produced from a group.
type CompactWritten struct {
	Scope string
	IDs   []string // handoffs folded into the record
	ID    string   // new knowledge id
	Rel   string   // path relative to the memory root
}

// CompactResult reports one compaction run: the groups selected, and the
// records written (empty on a dry run).
type CompactResult struct {
	Days     int
	MinGroup int
	DryRun   bool
	Groups   []CompactGroup
	Written  []CompactWritten
}

// Compact folds handoffs that are older than opts.Days (by last_used, else
// created) into one knowledge record per scope, once a scope has at least
// opts.MinGroup of them. The handoffs are marked status=compacted with
// compacted_into set, and their bullet list carries each id as evidence.
func Compact(root string, o CompactOptions) (CompactResult, error) {
	if o.Days == 0 {
		o.Days = 7
	}
	if o.MinGroup == 0 {
		o.MinGroup = 3
	}
	res := CompactResult{Days: o.Days, MinGroup: o.MinGroup, DryRun: o.DryRun}
	err := store.WithLock(root, func() error {
		sch, err := schema.LoadSchema(root)
		if err != nil {
			return fmt.Errorf("domain: compact: %w", err)
		}
		recs, err := store.Scan(root)
		if err != nil {
			return fmt.Errorf("domain: compact: %w", err)
		}
		ids := make(map[string]bool, len(recs))
		for _, r := range recs {
			ids[r.ID] = true
		}
		cutoff := timeNow().AddDate(0, 0, -o.Days)
		cutoff = time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, time.UTC)

		groups := make(map[string][]store.Record, len(recs))
		for _, r := range recs {
			if r.Layer != "handoff" || r.Status != "active" || r.Sensitivity == "quarantine" {
				continue
			}
			ref := strField(r.Front, "last_used")
			if ref == "" {
				ref = r.Created
			}
			if len(ref) > 10 {
				ref = ref[:10]
			}
			refDate, derr := time.Parse("2006-01-02", ref)
			if derr != nil {
				continue // no readable date — not a compaction candidate
			}
			if !refDate.Before(cutoff) {
				continue
			}
			groups[r.Scope] = append(groups[r.Scope], r)
		}

		var scopes []string
		for sc, g := range groups {
			if len(g) < o.MinGroup {
				continue
			}
			scopes = append(scopes, sc)
		}
		sort.Strings(scopes)
		for _, sc := range scopes {
			items := groups[sc]
			sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
			ids := make([]string, len(items))
			for i, r := range items {
				ids[i] = r.ID
			}
			res.Groups = append(res.Groups, CompactGroup{Scope: sc, IDs: ids})
		}
		if o.DryRun || len(scopes) == 0 {
			return nil
		}

		// Plan phase: every group is guarded and id-checked before any write.
		type plan struct {
			scope   string
			items   []store.Record
			id      string
			fm      codec.Frontmatter
			bullets string
			slug    string
		}
		knowledgeDir := sch.Layers["knowledge"].Dir
		if knowledgeDir == "" {
			return fmt.Errorf("domain: compact: the schema declares no knowledge layer directory — fix schema/taxonomy.json")
		}
		var plans []plan
		for _, sc := range scopes {
			items := groups[sc]
			lines := make([]string, len(items))
			tags := make([]string, 0, len(items))
			seenTag := make(map[string]bool, len(items))
			sensitivity := "internal"
			evidence := make([]string, len(items))
			for i, r := range items {
				// Python core_text() gets the raw front-matter title here (not
				// the derived one), so a body-only title stays out of the bullet.
				lines[i] = "- " + collapseSpace(CoreText(strField(r.Front, "title"), r.Body)) + " [id: " + r.ID + "]"
				for _, t := range r.Tags {
					if !seenTag[t] {
						seenTag[t] = true
						tags = append(tags, t)
					}
				}
				if r.Sensitivity == "private" {
					sensitivity = "private"
				}
				evidence[i] = "compacted-from:" + r.ID
			}
			bullets := strings.Join(lines, "\n")
			title := "Compacted handoffs: " + sc
			newID := codec.MakeID("knowledge", sc, title, bullets)
			if ids[newID] {
				return errf(3, "ข้าม scope %s — มี knowledge ที่ id เดียวกันอยู่แล้ว: %s", sc, newID)
			}
			if gerr := store.GuardWith(&sch, title+"\n"+bullets); gerr != nil {
				return &Error{Code: 2, Msg: gerr.Error()}
			}
			fm := codec.Frontmatter{
				"id": newID, "title": title, "layer": "knowledge", "scope": sc,
				"tags": tags, "source": "mem compact", "evidence": evidence,
				"created": today(), "last_used": today(),
				"status": "active", "sensitivity": sensitivity,
			}
			plans = append(plans, plan{
				scope: sc, items: items, id: newID, fm: fm,
				bullets: bullets, slug: codec.Slugify(title),
			})
		}

		for _, p := range plans {
			dest := uniquePath(filepath.Join(root, knowledgeDir, p.slug+".md"))
			if derr := os.MkdirAll(filepath.Dir(dest), 0o755); derr != nil {
				return fmt.Errorf("domain: compact %s: %w", p.scope, derr)
			}
			text := codec.DumpFront(p.fm) + "\n" + p.bullets + "\n"
			if werr := store.AtomicWrite(dest, []byte(text)); werr != nil {
				return fmt.Errorf("domain: compact %s: %w", p.scope, werr)
			}
			for _, r := range p.items {
				m := cloneFront(r.Front)
				m["status"] = "compacted"
				m["compacted_into"] = p.id
				if werr := rewriteRecord(r.Path, r.Body, m); werr != nil {
					return fmt.Errorf("domain: compact %s (knowledge written at %s): %w", p.scope, dest, werr)
				}
			}
			rel, rerr := filepath.Rel(root, dest)
			if rerr != nil {
				return fmt.Errorf("domain: compact %s: %w", p.scope, rerr)
			}
			ids := make([]string, len(p.items))
			for i, r := range p.items {
				ids[i] = r.ID
			}
			res.Written = append(res.Written, CompactWritten{Scope: p.scope, IDs: ids, ID: p.id, Rel: rel})
		}
		if rerr := ReindexRoot(root); rerr != nil {
			return fmt.Errorf("domain: compact wrote %d records but reindex failed: %w", len(res.Written), rerr)
		}
		return nil
	})
	return res, err
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// uniquePath disambiguates a taken filename with a -2, -3, … suffix on the stem
// — the same pattern Python's _unique_path and cmd_add use.
func uniquePath(target string) string {
	ext := filepath.Ext(target)
	stem := strings.TrimSuffix(target, ext)
	candidate := target
	for i := 2; ; i++ {
		if _, err := os.Stat(candidate); err != nil {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d%s", stem, i, ext)
	}
}
