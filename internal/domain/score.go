// Scoring, expiry and tokenisation — the Python score()/Rec.expired()/
// Rec.half_life_days()/tokens()/jaccard()/core_text() ports. All length
// semantics are rune-based (Python len() over str).
package domain

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/atomsbaza/my-memory-engine/internal/codec"
	"github.com/atomsbaza/my-memory-engine/internal/schema"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// DefaultScoring mirrors Python DEFAULT_SCORING — shipped neutral.
var DefaultScoring = schema.Scoring{
	WConf:         1.0,
	WRecency:      0.0,
	TauDays:       14,
	WUses:         0.0,
	ThresholdHook: 6.0,
}

// layerWeight mirrors Python LAYER_WEIGHT.
var layerWeight = map[string]float64{
	"rule": 6.0, "project": 4.0, "lesson": 3.0, "decision": 2.5,
	"handoff": 2.0, "knowledge": 1.0, "ops": 0.5,
}

// LayerWeight returns the base weight of a layer (1.0 when unknown).
func LayerWeight(layer string) float64 {
	if w, ok := layerWeight[layer]; ok {
		return w
	}
	return 1.0
}

// tokenRe mirrors Python tokens(): re.findall(r"[0-9A-Za-z_ก-๙]+", s.lower()).
var tokenRe = regexp.MustCompile(`[0-9A-Za-z_ก-๙]+`)

// Tokens returns the distinct word tokens of s, lowercased (Python set).
func Tokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, m := range tokenRe.FindAllString(strings.ToLower(s), -1) {
		out[m] = true
	}
	return out
}

// Jaccard is |a∩b| / |a∪b|; empty sets score 0.
func Jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0.0
	}
	inter := 0
	for t := range a {
		if b[t] {
			inter++
		}
	}
	if inter == 0 {
		return 0.0
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

// CoreText is the duplicate-comparison text: the title plus body lines with
// Evidence/Source citations stripped (two records differing only in cited
// evidence are still one fact).
func CoreText(title, body string) string {
	var lines []string
	for _, l := range strings.Split(body, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "**Evidence.**") || strings.HasPrefix(t, "**Source.**") ||
			strings.HasPrefix(t, "_[source:") {
			continue
		}
		lines = append(lines, l)
	}
	return title + " " + strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// Expiry — Python norm_expires / Rec.expired
// ---------------------------------------------------------------------------

var expiresRelRe = regexp.MustCompile(`^\+?(\d{1,4})\s*([dwmy])$`)

var expiresUnitDays = map[string]int{"d": 1, "w": 7, "m": 30, "y": 365}

// NormExpires normalises an --expires value to "none" or an ISO date:
// none | YYYY-MM-DD | +21d | +2w | +3m are accepted, anything else is a
// loud usage error (relative forms resolve against today so the stored
// value stays sync-safe across machines).
func NormExpires(v string, now time.Time) (string, error) {
	s := strings.ToLower(strings.TrimSpace(v))
	if s == "" || s == "none" || s == "null" || s == "never" {
		return "none", nil
	}
	if m := expiresRelRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		days := n * expiresUnitDays[m[2]]
		return now.AddDate(0, 0, days).Format("2006-01-02"), nil
	}
	iso := clipRunes(s, 10)
	if _, err := time.Parse("2006-01-02", iso); err != nil {
		return "", errf(1, "--expires ต้องเป็น none | YYYY-MM-DD | +21d | +2w | +3m (ได้มา: %s)", v)
	}
	return iso, nil
}

// Expired reports whether rec's expires date is strictly before today.
// Unparsable values never expire (Python Rec.expired semantics: relative
// forms were already normalised at write time; garbage dates resolve false).
func Expired(rec store.Record) bool {
	e := strings.ToLower(strings.TrimSpace(rec.Expires))
	if e == "" || e == "none" || e == "null" {
		return false
	}
	d, err := time.Parse("2006-01-02", clipRunes(e, 10))
	if err != nil {
		return false // includes the defensive relative-form branch: never past
	}
	return d.Before(truncateToDate(timeNow()))
}

func truncateToDate(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// ---------------------------------------------------------------------------
// Half-life — Python Rec.half_life_days
// ---------------------------------------------------------------------------

var halfLifeRe = regexp.MustCompile(`^\+?(\d+(?:\.\d+)?)\s*([dwmy])?$`)

var halfLifeMult = map[string]float64{"d": 1.0, "w": 7.0, "m": 30.0, "y": 365.0}

var layerHalfLife = map[string]float64{"rule": math.Inf(1), "project": 30.0, "handoff": 3.0}

func halfLifeDays(rec store.Record) float64 {
	if v := rec.Front["half_life"]; v != nil {
		s := strings.ToLower(strings.TrimSpace(scalarString(v)))
		switch s {
		case "inf", "infinity", "none", "never":
			return math.Inf(1)
		}
		if m := halfLifeRe.FindStringSubmatch(s); m != nil {
			num, _ := strconv.ParseFloat(m[1], 64)
			unit := m[2]
			if unit == "" {
				unit = "d"
			}
			return math.Max(0.1, num*halfLifeMult[unit])
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return math.Max(0.1, f)
		}
	}
	if hl, ok := layerHalfLife[rec.Layer]; ok {
		return hl
	}
	return 14.0
}

// HalfLifeDays exposes a record's half-life in days (Python
// Rec.half_life_days) for the debug surfaces: +Inf for rules and explicit
// "inf", the layer default otherwise.
func HalfLifeDays(rec store.Record) float64 { return halfLifeDays(rec) }

// urgentTagFragments mirrors Python URGENT_TAG_FRAGMENTS: a record whose tags
// mention security work decays slower (urgency multiplier 1.5).
var urgentTagFragments = []string{"security", "guard", "safety"}

// RetentionScore is Python retention_score:
//
//	S = decay · 2^(−days_since_last_used / half_life) · (1+ln(1+uses)) · urgency
//
// days is clamped at 0, so a record last used today is not penalised; an
// unparsable or absent last_used counts as today.
func RetentionScore(rec store.Record) float64 {
	days := 0
	if v, ok := rec.Front["last_used"]; ok {
		if t, err := time.Parse("2006-01-02", strings.TrimSpace(scalarString(v))); err == nil {
			now := timeNow()
			// Civil-date difference in UTC so a DST change cannot shift the day.
			a := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
			b := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
			days = int(a.Sub(b) / (24 * time.Hour))
			if days < 0 {
				days = 0
			}
		}
	}
	hl := halfLifeDays(rec)
	falloff := 1.0
	if !math.IsInf(hl, 1) {
		falloff = math.Pow(2, -float64(days)/hl)
	}
	urgency := 1.0
	tags := strings.ToLower(strings.Join(rec.Tags, " "))
	for _, frag := range urgentTagFragments {
		if strings.Contains(tags, frag) {
			urgency = 1.5
			break
		}
	}
	return rec.Decay * falloff * (1.0 + math.Log(1+float64(rec.Uses))) * urgency
}

// scalarString renders a front-matter scalar the way Python str() would;
// unparsable renders (bools, lists) make downstream float parses fail the
// same way Python's would.
func scalarString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case int:
		return strconv.Itoa(x)
	}
	return ""
}

// ---------------------------------------------------------------------------
// Score — Python score()
// ---------------------------------------------------------------------------

// confValue mirrors Python float(meta.get("confidence", 0.8)) with the
// except-fallback 0.8 for unparsable values.
func confValue(fm codec.Frontmatter) float64 {
	v, ok := fm["confidence"]
	if !ok || v == nil {
		return 0.8
	}
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return f
		}
	}
	return 0.8
}

// recText reconstructs the record's full file text (front-matter + body)
// for the substring bonus; every token of the raw file survives the
// round-trip, so containment semantics match Python rec.text.lower().
func recText(rec store.Record) string {
	return codec.DumpFront(rec.Front) + "\n" + rec.Body
}

func countIntersection(a, b map[string]bool) int {
	n := 0
	for t := range a {
		if b[t] {
			n++
		}
	}
	return n
}

// Score rates one record against a query and scope, mirroring Python
// score(): layer base weight, token overlaps (title ×4, tags ×3, body ×1,
// whole-text substring +1.5 per >2-rune query token), confidence term,
// scope match +3, used-today +0.2, expired −5, inactive −10, half-life
// recency term and capped uses term, all multiplied by decay.
func Score(rec store.Record, query, scope string, w schema.Scoring) float64 {
	s := LayerWeight(rec.Layer)
	qTokens := Tokens(query)
	if len(qTokens) > 0 {
		titleT := Tokens(strField(rec.Front, "title"))
		bodyT := Tokens(rec.Body)
		tagT := Tokens(strings.Join(rec.Tags, " "))
		s += 4.0*float64(countIntersection(qTokens, titleT)) +
			3.0*float64(countIntersection(qTokens, tagT)) +
			1.0*float64(countIntersection(qTokens, bodyT))
		low := strings.ToLower(recText(rec))
		for q := range qTokens {
			if rlen(q) > 2 && strings.Contains(low, q) {
				s += 1.5
			}
		}
	}
	s += w.WConf * confValue(rec.Front)
	if scope != "" && rec.Scope == scope {
		s += 3.0
	}
	if strField(rec.Front, "last_used") == today() {
		s += 0.2
	}
	if Expired(rec) {
		s -= 5.0
	}
	if rec.Status != "active" {
		s -= 10.0
	}
	if lu, err := time.Parse("2006-01-02", strField(rec.Front, "last_used")); err == nil {
		hl := halfLifeDays(rec)
		dd := int(truncateToDate(timeNow()).Sub(lu).Hours() / 24)
		if dd < 0 {
			dd = 0
		}
		recency := 1.0
		if !math.IsInf(hl, 1) {
			recency = math.Pow(0.5, float64(dd)/hl)
		}
		s += w.WRecency * recency
	}
	s += math.Min(w.WUses*math.Log2(1+float64(rec.Uses)), 2.5)
	s *= rec.Decay
	return s
}
