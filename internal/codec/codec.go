// Package codec implements the frontmatter parse/dump/id/slug/filename logic
// that mirrors the Python mem CLI byte-for-byte.
//
// Reference: ~/my-memory/bin/mem — parse_front, dump_front, make_id, slugify,
// _fmt_scalar, _needs_quote, _unquote, _parse_inline_list.
package codec

import (
	"crypto/sha1"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// FRONT_ORDER mirrors the Python FRONT_ORDER list exactly (preserving Python source order).
var FRONT_ORDER = []string{
	"id", "title", "layer", "scope", "tags", "confidence", "decay", "half_life", "source", "session",
	"evidence", "supersedes", "superseded_by", "promoted_from", "promoted_to", "compacted_into", "archived_at",
	"created", "last_used", "uses", "successes", "expires", "status", "sensitivity",
}

// LIST_KEYS are fields that are always serialised as inline YAML lists.
var LIST_KEYS = map[string]bool{
	"tags":     true,
	"evidence": true,
}

// REQUIRED fields that must be present in a valid record.
var REQUIRED = []string{"id", "layer", "scope", "created", "status", "sensitivity"}

// frontOrderSet is for O(1) lookup.
var frontOrderSet map[string]int

func init() {
	frontOrderSet = make(map[string]int, len(FRONT_ORDER))
	for i, k := range FRONT_ORDER {
		frontOrderSet[k] = i
	}
}

// Frontmatter holds parsed YAML front-matter values.
// Values are string, []string, or bool.
type Frontmatter map[string]any

// --- parse helpers ---

// unquote strips a matching outer pair of " or ' quotes.
func unquote(v string) string {
	v = strings.TrimSpace(v)
	n := len(v)
	if n >= 2 && v[0] == v[n-1] && (v[0] == '"' || v[0] == '\'') {
		return v[1 : n-1]
	}
	return v
}

// parseInlineList parses "[a, b, "c d"]" into []string.
func parseInlineList(v string) []string {
	inner := strings.TrimSpace(v)
	// strip outer [ ]
	inner = strings.TrimSpace(inner[1 : len(inner)-1])
	if inner == "" {
		return []string{}
	}
	var out []string
	var cur strings.Builder
	var q byte = 0
	for i := range len(inner) {
		ch := inner[i]
		if q != 0 {
			cur.WriteByte(ch)
			if ch == q {
				q = 0
			}
		} else if ch == '"' || ch == '\'' {
			q = ch
			cur.WriteByte(ch)
		} else if ch == ',' {
			item := unquote(cur.String())
			if item != "" {
				out = append(out, item)
			}
			cur.Reset()
		} else {
			cur.WriteByte(ch)
		}
	}
	if item := unquote(cur.String()); item != "" {
		out = append(out, item)
	}
	return out
}

var kvRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*:\s*(.*)$`)

// ParseFront splits a document into (Frontmatter, body, error).
// Returns empty map and original text if no valid front-matter found.
func ParseFront(text string) (Frontmatter, string, error) {
	lines := splitLines(text)
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return Frontmatter{}, text, nil
	}

	meta := Frontmatter{}
	var pendingKey string
	bodyStart := -1

	i := 1
	for i < len(lines) {
		line := lines[i]
		stripped := strings.TrimSpace(line)

		if stripped == "---" && pendingKey == "" {
			bodyStart = i + 1
			break
		}

		if pendingKey != "" && strings.HasPrefix(stripped, "- ") {
			existing := meta[pendingKey]
			switch ev := existing.(type) {
			case []string:
				meta[pendingKey] = append(ev, unquote(stripped[2:]))
			default:
				meta[pendingKey] = []string{unquote(stripped[2:])}
			}
			i++
			continue
		}
		pendingKey = ""

		m := kvRe.FindStringSubmatch(line)
		if m != nil {
			key, val := m[1], strings.TrimSpace(m[2])
			switch {
			case val == "":
				meta[key] = []string{}
				pendingKey = key
			case strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]"):
				meta[key] = parseInlineList(val)
			case strings.ToLower(val) == "true":
				meta[key] = true
			case strings.ToLower(val) == "false":
				meta[key] = false
			default:
				meta[key] = unquote(val)
			}
		}
		i++
	}

	if bodyStart == -1 {
		return Frontmatter{}, text, nil
	}

	body := strings.Join(lines[bodyStart:], "\n")
	return meta, body, nil
}

// splitLines splits text the way Python's str.splitlines() does for the
// newline-terminated text a record file holds: a single trailing "\n" does not
// produce a final empty element. The body ParseFront returns is therefore the
// same string Python's parse_front yields — which is what Rewrite re-emits
// (Python rewrites keep no trailing newline after the body).
func splitLines(text string) []string {
	lines := strings.Split(text, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// --- dump helpers ---

// needsQuote mirrors Python _needs_quote.
func needsQuote(v string) bool {
	if v == "" {
		return true
	}
	if strings.Contains(v, ": ") || strings.HasSuffix(v, ":") {
		return true
	}
	if strings.TrimSpace(v) != v {
		return true
	}
	if strings.Contains(v, "#") {
		return true
	}
	first, _ := utf8.DecodeRuneInString(v)
	for _, ch := range []rune{'#', '-', '?', '*', '&', '!', '|', '>', '%', '@', '`', '"', '\''} {
		if first == ch {
			return true
		}
	}
	return false
}

// fmtScalar mirrors Python _fmt_scalar. Python renders a scalar with str(),
// so a float keeps its decimal point ("decay: 1.0", never "decay: 1") —
// floats therefore go through pyFloatRepr rather than %v.
func fmtScalar(v any) string {
	var s string
	switch tv := v.(type) {
	case nil:
		s = ""
	case float64:
		s = pyFloatRepr(tv)
	case float32:
		s = pyFloatRepr(float64(tv))
	default:
		s = fmt.Sprintf("%v", v)
	}
	if needsQuote(s) {
		return `"` + s + `"`
	}
	return s
}

// pyFloatRepr renders a float the way Python's str() does for the values a
// record front-matter carries (decay and confidence, both within [0,1]):
// whole numbers keep one decimal place, everything else uses the shortest
// round-tripping form, e.g. 1.0 -> "1.0", 0.8 -> "0.8", 0.30000000000000004.
func pyFloatRepr(v float64) string {
	s := strconv.FormatFloat(v, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

func fmtList(items []string) string {
	parts := make([]string, len(items))
	for i, x := range items {
		parts[i] = fmtScalar(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// DumpFront serialises Frontmatter back to a YAML front-matter block (--- fenced).
// Field order follows FRONT_ORDER; unknown extra fields appended at the end.
func DumpFront(meta Frontmatter) string {
	var out []string
	out = append(out, "---")

	for _, k := range FRONT_ORDER {
		v, ok := meta[k]
		if !ok {
			continue
		}
		if LIST_KEYS[k] {
			var items []string
			switch tv := v.(type) {
			case []string:
				items = tv
			case []any:
				for _, x := range tv {
					items = append(items, fmt.Sprintf("%v", x))
				}
			case string:
				if tv != "" {
					items = []string{tv}
				}
			default:
				items = []string{}
			}
			out = append(out, k+": "+fmtList(items))
		} else {
			switch tv := v.(type) {
			case []string:
				var parts []string
				for _, x := range tv {
					parts = append(parts, fmtScalar(x))
				}
				out = append(out, k+": ["+strings.Join(parts, ", ")+"]")
			case []any:
				var parts []string
				for _, x := range tv {
					parts = append(parts, fmtScalar(fmt.Sprintf("%v", x)))
				}
				out = append(out, k+": ["+strings.Join(parts, ", ")+"]")
			default:
				out = append(out, k+": "+fmtScalar(v))
			}
		}
	}

	// Extra keys not in FRONT_ORDER.
	for k, v := range meta {
		if _, inOrder := frontOrderSet[k]; inOrder {
			continue
		}
		out = append(out, k+": "+fmtScalar(v))
	}

	out = append(out, "---")
	return strings.Join(out, "\n") + "\n"
}

// MakeID computes layer.sha1(layer|scope|title|body)[:8].
// Mirrors Python: hashlib.sha1(f"{layer}|{scope}|{title}|{body}".encode("utf-8")).hexdigest()[:8]
func MakeID(layer, scope, title, body string) string {
	h := sha1.New()
	h.Write([]byte(layer + "|" + scope + "|" + title + "|" + body))
	hex := fmt.Sprintf("%x", h.Sum(nil))
	return layer + "." + hex[:8]
}

// slugRe1 strips chars that are not alphanumeric, Thai, whitespace, or hyphen.
var slugRe1 = regexp.MustCompile(`[^0-9A-Za-zก-๙\s-]`)

// slugRe2 collapses whitespace/underscore runs to a single hyphen.
var slugRe2 = regexp.MustCompile(`[\s_]+`)

// slugRe3 collapses consecutive hyphens.
var slugRe3 = regexp.MustCompile(`-{2,}`)

// Slugify converts a title to a filename-safe slug, mirroring Python slugify.
func Slugify(title string) string {
	s := slugRe1.ReplaceAllString(title, "")
	s = strings.TrimSpace(strings.ToLower(s))
	s = slugRe2.ReplaceAllString(s, "-")
	s = slugRe3.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	// truncate to 60 runes, then strip trailing hyphen
	runes := []rune(s)
	if len(runes) > 60 {
		runes = runes[:60]
	}
	s = strings.TrimRight(string(runes), "-")
	if s == "" {
		return "entry"
	}
	return s
}

// datedLayers are the layers whose filename carries the created date
// (Python cmd_add: `if layer in ("lesson", "handoff", "decision")`).
var datedLayers = map[string]bool{"lesson": true, "handoff": true, "decision": true}

// Filename returns the canonical filename for a record, mirroring Python
// cmd_add exactly: "<created>-<slug>.md" for the dated layers (lesson,
// handoff, decision) and "<slug>.md" for every other layer. The slug falls
// back to "entry" when the title has no usable characters.
func Filename(layer, created, title string) string {
	slug := Slugify(title)
	if datedLayers[layer] {
		return created + "-" + slug + ".md"
	}
	return slug + ".md"
}

// FilenameN returns Filename with the Python collision suffix: n==0 is the
// plain name, n>0 appends "-<n>" to the stem ("<name>-2.md").
func FilenameN(layer, created, title string, n int) string {
	name := Filename(layer, created, title)
	if n == 0 {
		return name
	}
	return strings.TrimSuffix(name, ".md") + "-" + strconv.Itoa(n) + ".md"
}
