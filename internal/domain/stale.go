// `review --stale` — the read-only staleness report (KB P1): which active
// records have gone unused so long they are decay candidates. This is the
// cheap sibling of archive: it changes nothing, it just names the records
// whose retention score is decaying toward the archive threshold. 90 days is
// the default because the half-life defaults (project 30d, handoff 3d, rule
// ∞) leave a record past 90 untouched days at roughly a quarter of its
// project-layer peak — old enough to be noise, recent enough to still be
// recoverable by a cite.
package domain

import (
	"sort"
	"time"

	"github.com/atomsbaza/my-memory-engine/internal/schema"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// DefaultStaleDays is the default `--days` threshold of review --stale.
const DefaultStaleDays = 90

// StaleEntry is one row of the staleness report: a record whose
// last_used (fallback created) is at least Days days old.
type StaleEntry struct {
	ID        string  `json:"id"`
	Layer     string  `json:"layer"`
	Scope     string  `json:"scope"`
	Status    string  `json:"status"`
	Days      int     `json:"days"`
	Decay     float64 `json:"decay"`
	Retention float64 `json:"retention"`
	LastUsed  string  `json:"last_used"`
	Title     string  `json:"title"`
	File      string  `json:"file"`
}

// StaleReport lists every active record under root not used for at least
// `days` days, by last_used falling back to created, sorted days-desc with
// ties broken by taxonomy layer order then id. Unparsable/absent dates
// resolve to age 0 (matching RetentionScore's "counts as today") so they never
// appear at positive thresholds. The report never mutates: no touch, no log
// event, no git.
func StaleReport(root string, days int) ([]StaleEntry, error) {
	sch, err := schema.LoadSchema(root)
	if err != nil {
		return nil, errf(1, "domain: stale: %v", err)
	}
	recs, err := store.Scan(root)
	if err != nil {
		return nil, errf(1, "domain: stale: %v", err)
	}

	layerIdx := make(map[string]int, len(sch.LayerOrder))
	for i, l := range sch.LayerOrder {
		layerIdx[l] = i
	}
	unknown := len(sch.LayerOrder)
	today := truncateToDate(timeNow())

	entries := []StaleEntry{}
	for _, r := range recs {
		if r.Status != "active" {
			continue
		}
		lastUsed := strField(r.Front, "last_used")
		if lastUsed == "" {
			lastUsed = r.Created
		}
		d, ok := daysBetween(lastUsed, today)
		if !ok || d < days {
			continue
		}
		entries = append(entries, StaleEntry{
			ID:        r.ID,
			Layer:     r.Layer,
			Scope:     r.Scope,
			Status:    r.Status,
			Days:      d,
			Decay:     r.Decay,
			Retention: RetentionScore(r),
			LastUsed:  lastUsed,
			Title:     r.Title,
			File:      r.Rel,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Days != entries[j].Days {
			return entries[i].Days > entries[j].Days
		}
		li, lok := layerIdx[entries[i].Layer]
		if !lok {
			li = unknown
		}
		lj, lok := layerIdx[entries[j].Layer]
		if !lok {
			lj = unknown
		}
		if li != lj {
			return li < lj
		}
		return entries[i].ID < entries[j].ID
	})
	return entries, nil
}

// daysBetween returns how many full days date (YYYY-MM-DD, unparsable → not
// ok) lies before today.
func daysBetween(date string, today time.Time) (int, bool) {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return 0, false
	}
	return int(today.Sub(t).Hours() / 24), true
}
