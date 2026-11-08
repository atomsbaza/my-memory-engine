// Lifecycle operations beyond add: supersede, cite, review (the only
// quarantine exit) and touch — the Python cmd_supersede / cmd_cite /
// cmd_review / cmd_touch ports. Read-modify-write cycles run under the
// store's flock; every mutation rebuilds the derived index and reports
// failures instead of swallowing them.
package domain

import (
	"fmt"
	"strings"

	"github.com/atomsbaza/my-memory-engine/internal/codec"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// Supersede marks oldID as superseded and points newID's supersedes field
// at it (Python cmd_supersede: new.supersedes = old.id, old.status =
// "superseded"; the explicit cmd path does not set superseded_by — only
// add's supersede path does). Both ids must exist exactly.
func Supersede(root, oldID, newID string) error {
	recs, err := store.Scan(root)
	if err != nil {
		return fmt.Errorf("domain: supersede: %w", err)
	}
	byID := make(map[string]store.Record, len(recs))
	for _, r := range recs {
		byID[r.ID] = r
	}
	old, okOld := byID[oldID]
	new, okNew := byID[newID]
	missing := "new"
	if !okOld {
		missing = "old"
	}
	if !okOld || !okNew {
		return errf(1, "ไม่พบ %s record", missing)
	}
	return store.WithLock(root, func() error {
		newFM := cloneFront(new.Front)
		newFM["supersedes"] = old.ID
		if rerr := rewriteRecord(new.Path, new.Body, newFM); rerr != nil {
			return fmt.Errorf("domain: supersede: rewrite %s: %w", new.ID, rerr)
		}
		oldFM := cloneFront(old.Front)
		oldFM["status"] = "superseded"
		if rerr := rewriteRecord(old.Path, old.Body, oldFM); rerr != nil {
			return fmt.Errorf("domain: supersede: rewrite %s: %w", old.ID, rerr)
		}
		if rerr := ReindexRoot(root); rerr != nil {
			return fmt.Errorf("domain: supersede %s -> %s persisted but reindex failed: %w", old.ID, new.ID, rerr)
		}
		return nil
	})
}

// Cite records one real use of the record matching id (exact id or unique
// prefix): uses += 1 and last bumped to today, under the store flock so
// concurrent hooks/MCP calls cannot lose an update (Python M4). It returns
// the full id of the matched record.
func Cite(root, id string) (string, error) {
	var fullID string
	err := store.WithLock(root, func() error {
		recs, err := store.Scan(root)
		if err != nil {
			return fmt.Errorf("domain: cite: %w", err)
		}
		var matches []store.Record
		for _, r := range recs {
			if r.ID == id || strings.HasPrefix(r.ID, id) {
				matches = append(matches, r)
			}
		}
		if len(matches) != 1 {
			return errf(1, "id ไม่ชัด: ตรง %d records ('%s')", len(matches), id)
		}
		r := matches[0]
		m := cloneFront(r.Front)
		m["uses"] = r.Uses + 1
		m["last_used"] = today()
		if rerr := rewriteRecord(r.Path, r.Body, m); rerr != nil {
			return fmt.Errorf("domain: cite %s: %w", r.ID, rerr)
		}
		if rerr := ReindexRoot(root); rerr != nil {
			return errf(1, "reindex พังหลัง cite: %s", clipRunes(rerr.Error(), 160))
		}
		fullID = r.ID
		return nil
	})
	return fullID, err
}

// Review is the single quarantine exit (Python cmd_review): it flips
// sensitivity quarantine -> internal, appends the screening evidence to the
// record so the audit trace travels with it, rewrites, reindexes and
// commits locally for durability. Evidence is required; a non-quarantine
// record cannot be "reviewed".
func Review(root, id string, evidence []string) error {
	if len(evidence) == 0 {
		return errf(1, `review ต้องมี --evidence "reviewed: <หมายเหตุ>"`)
	}
	return store.WithLock(root, func() error {
		recs, err := store.Scan(root)
		if err != nil {
			return fmt.Errorf("domain: review: %w", err)
		}
		var matches []store.Record
		for _, r := range recs {
			if strings.HasPrefix(r.ID, id) {
				matches = append(matches, r)
			}
		}
		if len(matches) != 1 {
			return errf(1, "id ไม่ชัด: ตรง %d records ('%s')", len(matches), id)
		}
		r := matches[0]
		if r.Sensitivity != "quarantine" {
			return errf(1, "%s ไม่ได้อยู่ใน quarantine (sensitivity=%s)", r.ID, r.Sensitivity)
		}
		m := cloneFront(r.Front)
		m["sensitivity"] = "internal"
		var ev []string
		if existing, ok := r.Front["evidence"].([]string); ok {
			for _, e := range existing {
				if e != "" {
					ev = append(ev, e)
				}
			}
		}
		for _, e := range evidence {
			if e != "" {
				ev = append(ev, e)
			}
		}
		m["evidence"] = ev
		if rerr := rewriteRecord(r.Path, r.Body, m); rerr != nil {
			return fmt.Errorf("domain: review %s: %w", r.ID, rerr)
		}
		if rerr := ReindexRoot(root); rerr != nil {
			return fmt.Errorf("domain: review %s persisted but reindex failed: %w", r.ID, rerr)
		}
		if cerr := store.CommitPath(root, r.Rel, fmt.Sprintf("review(%s): unquarantine %s", r.Scope, r.ID)); cerr != nil {
			return fmt.Errorf("domain: review %s persisted but commit failed: %w", r.ID, cerr)
		}
		return nil
	})
}

// Touch bumps last_used to today for every record whose last_used differs
// (the cmd_context rotation side effect and cmd_touch over a record set).
// It returns how many records were rewritten.
func Touch(root string, recs []store.Record) (int, error) {
	n := 0
	for _, r := range recs {
		if strField(r.Front, "last_used") == today() {
			continue
		}
		m := cloneFront(r.Front)
		m["last_used"] = today()
		if rerr := rewriteRecord(r.Path, r.Body, m); rerr != nil {
			return n, fmt.Errorf("domain: touch %s: %w", r.ID, rerr)
		}
		n++
	}
	if n > 0 {
		if rerr := ReindexRoot(root); rerr != nil {
			return n, fmt.Errorf("domain: touch persisted but reindex failed: %w", rerr)
		}
	}
	return n, nil
}

// evidenceStrings views a front-matter evidence entry as a clean string
// list (missing / malformed → nil).
func evidenceStrings(fm codec.Frontmatter) []string {
	ev, _ := fm["evidence"].([]string)
	out := make([]string, 0, len(ev))
	for _, e := range ev {
		if e != "" {
			out = append(out, e)
		}
	}
	return out
}
