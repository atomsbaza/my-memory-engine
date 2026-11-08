// Package store implements the local record store (P1.4): memory-home
// resolution, scanning with skip rules, reading records, and the write guard.
// Persistence (atomic write, flock, git commit, runtime log) lives in
// write.go within this package.
//
// Reference: ~/my-memory/bin/mem — find_root, scan, guard, secret_hits,
// path_hits, pii_hits, Rec. The engine is local-only: this package performs
// no network calls (NFR-NET-01 / AC-P1-10).
package store

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/atomsbaza/my-memory-engine/internal/codec"
	"github.com/atomsbaza/my-memory-engine/internal/schema"
)

// ---------------------------------------------------------------------------
// Record
// ---------------------------------------------------------------------------

// Record is one memory record: a markdown file with YAML front-matter.
//
// Front retains every parsed front-matter key (the codec round-trips it
// byte-exactly); the typed fields below are convenience views, mirroring the
// Python Rec accessors.
type Record struct {
	Path string // filesystem path of the record file
	Rel  string // path relative to the scan root ("" when not from Scan)
	Body string // body bytes after the closing "---" line

	// Front is the parsed front-matter (all keys, in parsed form).
	Front codec.Frontmatter

	// Typed views over Front (zero values when absent or mistyped).
	ID          string
	Title       string
	Layer       string
	Scope       string
	Status      string // defaults to "active" when absent (Python Rec.status)
	Sensitivity string
	Tags        []string
	Created     string
	Expires     string
	Uses        int
	Successes   int
	Decay       float64 // clamped to [0,1]; 1.0 when absent (Python Rec.decay)
}

// TitleOrDerived returns the front-matter title, or the first body line
// truncated to 80 runes, or the id when the body is empty — mirroring the
// Python Rec.title property. Id computation for derived-title records must
// use this value (S4 parity).
func (r Record) TitleOrDerived() string {
	if r.Title != "" {
		return r.Title
	}
	line := strings.TrimSpace(r.Body)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if line == "" {
		return r.ID
	}
	runes := []rune(line)
	if len(runes) > 80 {
		runes = runes[:80]
	}
	return string(runes)
}

// newRecord asfills the typed view from parsed front-matter.
func newRecord(path string, fm codec.Frontmatter, body string) Record {
	rec := Record{Path: path, Body: body, Front: fm}
	rec.ID = strField(fm, "id")
	rec.Title = strField(fm, "title")
	rec.Layer = strField(fm, "layer")
	rec.Scope = strField(fm, "scope")
	rec.Status = strField(fm, "status")
	if rec.Status == "" {
		rec.Status = "active"
	}
	rec.Sensitivity = strField(fm, "sensitivity")
	rec.Tags = tagsField(fm)
	rec.Created = strField(fm, "created")
	rec.Expires = strField(fm, "expires")
	rec.Uses = nonNegIntField(fm, "uses")
	rec.Successes = nonNegIntField(fm, "successes")
	rec.Decay = decayField(fm)
	return rec
}

func strField(fm codec.Frontmatter, key string) string {
	s, _ := fm[key].(string)
	return s
}

// tagsField mirrors Python Rec.tags: a list stays a list; a bare scalar
// becomes a one-element list; anything else is empty.
func tagsField(fm codec.Frontmatter) []string {
	switch v := fm[keyTags].(type) {
	case []string:
		return v
	case string:
		if v != "" {
			return []string{v}
		}
	}
	return nil
}

const keyTags = "tags"

func nonNegIntField(fm codec.Frontmatter, key string) int {
	v := fm[key]
	switch n := v.(type) {
	case int:
		return max(0, n)
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return 0
		}
		return max(0, i)
	}
	return 0
}

func decayField(fm codec.Frontmatter) float64 {
	v, ok := fm["decay"]
	if !ok || v == nil {
		return 1.0
	}
	s, _ := v.(string)
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 1.0
	}
	return min(1.0, max(0.0, f))
}

// ---------------------------------------------------------------------------
// FindRoot — REQ-STORE-01 / AC-P1-05
// ---------------------------------------------------------------------------

// FindRoot resolves the memory home root in the documented order:
//
//  1. $MEM_HOME
//  2. $MY_MEMORY_HOME
//  3. upward walk from the working directory for schema/taxonomy.json
//  4. the documented default ~/my-memory (only when it is a memory repo)
//
// An unresolvable home fails loudly; an empty path is never returned as
// success. Paths are ~-expanded, made absolute and symlink-resolved
// (mirroring Python Path.expanduser().resolve()).
func FindRoot() (string, error) {
	for _, env := range []string{"MEM_HOME", "MY_MEMORY_HOME"} {
		if v := os.Getenv(env); v != "" {
			return expandHome(v)
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		for cand := cwd; ; {
			if isMemoryRepo(cand) {
				return cand, nil
			}
			parent := filepath.Dir(cand)
			if parent == cand {
				break
			}
			cand = parent
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		def := filepath.Join(home, "my-memory")
		if isMemoryRepo(def) {
			return def, nil
		}
		return "", fmt.Errorf("store: memory root not found — set MEM_HOME or MY_MEMORY_HOME, run from inside a memory repo, or initialise %s", def)
	}
	return "", fmt.Errorf("store: memory root not found and the user home directory is unavailable — set MEM_HOME or MY_MEMORY_HOME")
}

// expandHome expands a leading ~ to the user home, then makes the path
// absolute and symlink-resolved.
func expandHome(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("store: cannot expand %q: %w", p, err)
		}
		p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("store: cannot resolve %q: %w", p, err)
	}
	// Best-effort symlink resolution (matches Python .resolve(); a missing
	// path is kept as-is so the failure surfaces at first use, like Python).
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}

// ExpandPath is the exported form of the home override rule: a leading ~ is
// expanded, then the path is made absolute and symlink-resolved. It lets
// `mem --home PATH` resolve exactly like FindRoot's $MEM_HOME branch instead of
// growing a second, subtly different path rule in the CLI.
func ExpandPath(p string) (string, error) { return expandHome(p) }

func isMemoryRepo(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "schema", "taxonomy.json"))
	return err == nil && !info.IsDir()
}

// ---------------------------------------------------------------------------
// Scan — REQ-STORE-02 / AC-P1-06
// ---------------------------------------------------------------------------

// BrokenFile is a *.md file inside a scanned layer directory that has no
// parsable front-matter. Broken files are never counted as records, but they
// are surfaced instead of being skipped silently.
type BrokenFile struct {
	Path    string
	Rel     string
	Problem string
}

// ScanReport is the full result of a scan: valid records plus every broken
// file that was refused.
type ScanReport struct {
	Records []Record
	Broken  []BrokenFile
}

// Scan returns every valid record under root, applying the skip rules:
//
//   - layers whose file_mode is "append-only" (ops/) are skipped entirely —
//     they are append-only logs, not front-matter records;
//   - any path component starting with "_" (dirs and files) is skipped.
//
// Files that look like records but have broken front-matter are not returned
// and not silently ignored — use ScanWithReport to see them.
func Scan(root string) ([]Record, error) {
	rep, err := ScanWithReport(root)
	return rep.Records, err
}

// ScanWithReport is Scan plus the list of broken files (REQ-STORE-02:
// broken front-matter must be observable, never a silent skip).
func ScanWithReport(root string) (ScanReport, error) {
	sch, err := schema.LoadSchema(root)
	if err != nil {
		return ScanReport{}, fmt.Errorf("store: scan %s: %w", root, err)
	}
	rep := ScanReport{}
	// layerPos is the taxonomy declaration order — the order Python scan walks
	// layers, and therefore the order records are handed to every consumer.
	layerOrder := sch.LayerOrder
	if len(layerOrder) == 0 {
		layerOrder = sortedLayerNames(sch.Layers)
	}
	layerPos := make(map[string]int, len(layerOrder))
	for i, name := range layerOrder {
		layerPos[name] = i
	}
	for _, layer := range layerOrder {
		if _, known := sch.Layers[layer]; !known {
			continue
		}
		if sch.Layers[layer].FileMode == "append-only" {
			continue // ops/ log — not a record directory
		}
		dir := filepath.Join(root, sch.Layers[layer].Dir)
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if p == dir && os.IsNotExist(err) {
					return nil // layer directory absent — nothing to scan
				}
				return err
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), "_") {
					return fs.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(d.Name(), "_") || !strings.HasSuffix(d.Name(), ".md") {
				return nil
			}
			rootRel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				return rerr
			}
			rec, rerr := ReadRecord(p)
			if rerr != nil {
				rep.Broken = append(rep.Broken, BrokenFile{Path: p, Rel: rootRel, Problem: rerr.Error()})
				return nil
			}
			if len(rec.Front) == 0 {
				rep.Broken = append(rep.Broken, BrokenFile{Path: p, Rel: rootRel, Problem: "no YAML front-matter"})
				return nil
			}
			rec.Rel = rootRel
			rep.Records = append(rep.Records, rec)
			return nil
		})
		if err != nil {
			return ScanReport{}, fmt.Errorf("store: scan %s: %w", root, err)
		}
	}
	sort.SliceStable(rep.Records, func(i, j int) bool {
		li, lj := layerPos[rep.Records[i].Layer], layerPos[rep.Records[j].Layer]
		if li != lj {
			return li < lj
		}
		return rep.Records[i].Rel < rep.Records[j].Rel
	})
	sort.SliceStable(rep.Broken, func(i, j int) bool { return rep.Broken[i].Rel < rep.Broken[j].Rel })
	return rep, nil
}

func sortedLayerNames(layers map[string]schema.LayerDef) []string {
	names := make([]string, 0, len(layers))
	for name := range layers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ---------------------------------------------------------------------------
// ReadRecord
// ---------------------------------------------------------------------------

// ReadRecord reads one record file and parses its front-matter. A file with
// no front-matter parses successfully with an empty Front — callers that
// need to distinguish use len(rec.Front) == 0.
func ReadRecord(path string) (Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Record{}, fmt.Errorf("store: read %s: %w", path, err)
	}
	fm, body, err := codec.ParseFront(string(data))
	if err != nil {
		return Record{}, fmt.Errorf("store: parse %s: %w", path, err)
	}
	return newRecord(path, fm, body), nil
}

// ---------------------------------------------------------------------------
// Guard — write refusal for secrets, machine paths and PII
// ---------------------------------------------------------------------------

// DefaultPIIPatterns mirror the data repo's taxonomy defaults (email, US
// phone, Thai mobile, SSN, card numbers). The Go schema package does not
// load pii_patterns yet, so the store carries these defaults.
var DefaultPIIPatterns = []string{
	`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`,
	`\b(?:\+1[-. ]?)?\(?\d{3}\)?[-. ]\d{3}[-. ]\d{4}\b`,
	`\b0[689]\d{8}\b`,
	`\b\d{3}-\d{2}-\d{4}\b`,
	`\b(?:4\d{12}(?:\d{3})?|5[1-5]\d{14}|3[47]\d{13}|6(?:011|5\d{2})\d{12})\b`,
}

var (
	defaultGuardOnce sync.Once
	defaultGuardSch  *schema.Schema
	defaultGuardErr  error
)

// defaultGuardSchema returns the engine-default guard configuration
// (schema.LoadSchema with no repo — portable defaults).
func defaultGuardSchema() (*schema.Schema, error) {
	defaultGuardOnce.Do(func() {
		sch, err := schema.LoadSchema("")
		if err != nil {
			defaultGuardErr = err
			return
		}
		defaultGuardSch = &sch
	})
	return defaultGuardSch, defaultGuardErr
}

// Guard refuses text that may contain a secret, a machine-specific absolute
// path, or personal data — mirroring Python guard() with the engine's
// portable defaults. Check order: secret, path, PII.
func Guard(text string) error {
	sch, err := defaultGuardSchema()
	if err != nil {
		return fmt.Errorf("store: guard defaults: %w", err)
	}
	return GuardWith(sch, text)
}

// GuardWith is Guard with an explicit schema (secret patterns and forbidden
// path fragments come from the schema; PII patterns from the store defaults).
func GuardWith(sch *schema.Schema, text string) error {
	if sch == nil {
		return fmt.Errorf("store: guard: schema is nil")
	}
	for _, pat := range sch.SecretPatterns {
		re, err := regexp.Compile(pat)
		if err != nil {
			return fmt.Errorf("store: guard: secret pattern %q does not compile: %w", pat, err)
		}
		if re.MatchString(text) {
			return fmt.Errorf("store: guard: possible secret — write refused (pattern %s…); if this is a false positive, make the phrase clearly non-real or fix schema/taxonomy.json", truncRunes(pat, 60))
		}
	}
	for _, frag := range sch.ForbiddenPaths {
		if strings.Contains(text, frag) {
			return fmt.Errorf("store: guard: machine-specific absolute path %q — forbidden (breaks cross-machine sync); use ~ or a relative path", frag)
		}
	}
	for _, pat := range DefaultPIIPatterns {
		re, err := regexp.Compile(pat)
		if err != nil {
			return fmt.Errorf("store: guard: PII pattern %q does not compile: %w", pat, err)
		}
		if re.MatchString(text) {
			return fmt.Errorf("store: guard: possible personal data (PII) — write refused (pattern %s…); never store email/phone/SSN/card numbers, redact first", truncRunes(pat, 60))
		}
	}
	return nil
}

func truncRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
