// Verify — the Python cmd_verify port: schema/required-field/id/duplicate/
// layer-dir checks as problems, taxonomy/link/NOT./injection checks as
// warnings, guard scans over the full record text, rules budget, per-agent
// inject cut/missing-rule checks, index freshness and required folders.
package domain

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/atomsbaza/my-memory-engine/internal/index"
	"github.com/atomsbaza/my-memory-engine/internal/schema"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// injectionPhrases mirrors Python INJECTION_PHRASES.
var injectionPhrases = []string{
	"ignore previous",
	"ignore all previous",
	"disregard previous",
	"system:",
	"override rule",
	"override instructions",
	"forget your instructions",
}

func injectionHits(text string) []string {
	low := strings.ToLower(text)
	var hits []string
	for _, p := range injectionPhrases {
		if strings.Contains(low, p) {
			hits = append(hits, p)
		}
	}
	return hits
}

var linkRe = regexp.MustCompile(`https?://`)

// secretHits compiles the schema's secret patterns and reports those that
// match text (pattern source strings, like Python).
func secretHits(sch *schema.Schema, text string) ([]string, error) {
	var hits []string
	for _, pat := range sch.SecretPatterns {
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("domain: secret pattern %q: %w", pat, err)
		}
		if re.MatchString(text) {
			hits = append(hits, pat)
		}
	}
	return hits, nil
}

func pathHits(sch *schema.Schema, text string) []string {
	var hits []string
	for _, frag := range sch.ForbiddenPaths {
		if strings.Contains(text, frag) {
			hits = append(hits, frag)
		}
	}
	return hits
}

// piiHits mirrors Python pii_hits over the store's default PII patterns.
func piiHits(text string) ([]string, error) {
	var hits []string
	for _, pat := range store.DefaultPIIPatterns {
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("domain: PII pattern %q: %w", pat, err)
		}
		if re.MatchString(text) {
			hits = append(hits, pat)
		}
	}
	return hits, nil
}

// Verify inspects the whole home and returns problems (blocking) and
// warnings (advisory). A problem means the repo violates the record
// contract; warnings flag hygiene risks. Unexpected infrastructure errors
// (unreadable index, bad patterns) are returned as errors instead of being
// folded into the report.
func Verify(root string) (problems, warnings []string, err error) {
	sch, err := schema.LoadSchema(root)
	if err != nil {
		return nil, nil, fmt.Errorf("domain: verify: %w", err)
	}
	rep, err := store.ScanWithReport(root)
	if err != nil {
		return nil, nil, fmt.Errorf("domain: verify: %w", err)
	}
	recs := rep.Records
	seen := map[string]string{}

	for _, b := range rep.Broken {
		problems = append(problems, fmt.Sprintf("%s: ไม่มี frontmatter", b.Rel))
	}
	for _, r := range recs {
		for _, k := range sch.Required {
			if strField(r.Front, k) == "" {
				problems = append(problems, fmt.Sprintf("%s: ขาด field '%s'", r.Rel, k))
			}
		}
		if r.ID != "" && !sch.IDPattern.MatchString(r.ID) {
			problems = append(problems, fmt.Sprintf("%s: id ผิดรูปแบบ (%s)", r.Rel, r.ID))
		}
		if prev, dup := seen[r.ID]; dup {
			problems = append(problems, fmt.Sprintf("id ซ้ำ: %s (%s / %s)", r.ID, r.Rel, prev))
		}
		seen[r.ID] = r.Rel

		dir := ""
		if def, ok := sch.Layers[r.Layer]; ok {
			dir = def.Dir
		}
		if parent := parentDirName(r.Path); r.Layer != "" && parent != dir {
			problems = append(problems, fmt.Sprintf("%s: layer=%s ไม่ตรงกับโฟลเดอร์ (%s/)", r.Rel, r.Layer, parent))
		}

		source := strField(r.Front, "source")
		if r.Sensitivity == "quarantine" && !strings.HasPrefix(source, "external:") {
			problems = append(problems, fmt.Sprintf("%s: sensitivity=quarantine ต้องมี source external:*", r.Rel))
		}
		if r.Layer == "rule" && r.Status == "active" && linkRe.MatchString(r.Body) &&
			!strings.HasPrefix(source, "external:") && !strings.HasPrefix(source, "import") {
			warnings = append(warnings, fmt.Sprintf("%s: rule มีลิงก์ภายนอกแต่ source ไม่ใช่ external/import (พิจารณา quarantine)", r.Rel))
		}
		if def, ok := sch.Layers[r.Layer]; ok && def.ExpiresRequired &&
			strings.ToLower(strField(r.Front, "expires")) == "none" {
			problems = append(problems, fmt.Sprintf("%s: layer %s ต้องมี expires", r.Rel, r.Layer))
		}
		if len(sch.Tags) > 0 {
			known := map[string]bool{}
			for _, t := range sch.Tags {
				known[t] = true
			}
			for _, t := range r.Tags {
				if !known[t] {
					warnings = append(warnings, fmt.Sprintf("%s: tag '%s' ไม่อยู่ใน taxonomy.json", r.Rel, t))
				}
			}
		}
		if cv, ok := r.Front["confidence"]; ok && cv != nil {
			f, perr := parseFloatStrict(scalarString(cv))
			if perr != nil {
				return nil, nil, fmt.Errorf("domain: verify: %s: confidence %v ไม่ใช่ตัวเลข: %w", r.Rel, cv, perr)
			}
			if f < 1.0 && len(evidenceStrings(r.Front)) == 0 {
				warnings = append(warnings, fmt.Sprintf("%s: confidence<1.0 แต่ไม่มี evidence", r.Rel))
			}
		}

		text := recText(r)
		sh, serr := secretHits(&sch, text)
		if serr != nil {
			return nil, nil, serr
		}
		for _, pat := range sh {
			problems = append(problems, fmt.Sprintf("%s: พบ pattern คล้าย secret (%s)", r.Rel, clipRunes(pat, 40)))
		}
		for _, frag := range pathHits(&sch, text) {
			problems = append(problems, fmt.Sprintf("%s: มี absolute path '%s'", r.Rel, frag))
		}
		ph, perr := piiHits(text)
		if perr != nil {
			return nil, nil, perr
		}
		for _, pat := range ph {
			problems = append(problems, fmt.Sprintf("%s: พบ pattern คล้าย PII (%s)", r.Rel, clipRunes(pat, 40)))
		}
		for _, inj := range injectionHits(text) {
			warnings = append(warnings, fmt.Sprintf("%s: ข้อความมีรูปแบบคล้าย prompt injection ('%s')", r.Rel, inj))
		}
		if r.Layer == "lesson" && !strings.Contains(r.Body, "**NOT.**") && !strings.Contains(r.Body, "NOT.") &&
			!strings.HasPrefix(source, "import") {
			warnings = append(warnings, fmt.Sprintf("%s: lesson ควรมี **NOT.** (anti-pattern)", r.Rel))
		}
	}

	rulesTotal := 0
	for _, r := range recs {
		if r.Layer == "rule" {
			rulesTotal += rlen(r.Body)
		}
	}
	if rulesTotal > sch.Budgets["rules"] {
		warnings = append(warnings, fmt.Sprintf("rules/ รวม %d chars — เกิน budget %d (ต้องเล็กเพราะ inject ทุก session)",
			rulesTotal, sch.Budgets["rules"]))
	}

	// Inject-shape check per render agent: a cut block or missing rules is a
	// silent budget failure of the injected instruction file.
	agents, aerr := LoadAgents(root)
	if aerr != nil {
		return nil, nil, aerr
	}
	if len(agents) > 0 {
		ruleTitles := map[string]bool{}
		for _, r := range recs {
			if r.Layer == "rule" && r.Status == "active" {
				ruleTitles[titleOrID(r)] = true
			}
		}
		for _, a := range agents {
			if a.Mode != "render" {
				continue
			}
			block := RenderBlock(root, recs, a, sch.Budgets["rules"])
			if strings.Contains(block, renderCutMarker) {
				warnings = append(warnings, fmt.Sprintf(
					"inject ของ agent '%s' ถูกตัด (block เกิน budget %d หลังหัก usage) — ย่อกฎ หรือเพิ่ม budgets_chars.rules ใน taxonomy.json",
					a.Name, sch.Budgets["rules"]))
				continue
			}
			var missing []string
			for t := range ruleTitles {
				if !strings.Contains(block, t) {
					missing = append(missing, t)
				}
			}
			sort.Strings(missing)
			if len(missing) > 0 {
				warnings = append(warnings, fmt.Sprintf("inject ของ agent '%s' ไม่มีกฎ: %s", a.Name, strings.Join(missing, ", ")))
			}
		}
	}

	// Index freshness: compare the derived index against the markdown tree
	// (broken files count toward the difference, as in Python where the
	// broken records are part of the scanned set).
	total := len(recs) + len(rep.Broken)
	if _, statErr := os.Stat(index.IndexPath(root)); statErr == nil {
		ix, oerr := openIndex(root)
		if oerr != nil {
			return nil, nil, oerr
		}
		n, cerr := ix.Count()
		ix.Close()
		if cerr != nil {
			return nil, nil, cerr
		}
		if n != total {
			warnings = append(warnings, fmt.Sprintf("index เก่า: มี %d แต่พบ %d records — รัน `mem reindex`", n, total))
		}
	} else {
		warnings = append(warnings, "ยังไม่มี index — รัน `mem reindex` ไม่มีผลต่อความถูกต้อง (index เป็น derived)")
	}

	for _, sub := range []string{"lessons", "projects", "rules"} {
		if st, derr := os.Stat(filepath.Join(root, sub)); derr != nil || !st.IsDir() {
			problems = append(problems, fmt.Sprintf("ขาดโฟลเดอร์ %s/", sub))
		}
	}
	return problems, warnings, nil
}

func parentDirName(path string) string {
	parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2]
}

