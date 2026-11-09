// Package schema implements the schema loader and record enforcement for
// my-memory-engine, mirroring the Python my-memory schema contract
// (record.schema.json + taxonomy.json + agents.json).
//
// Layering rule: the engine ships portable defaults (no machine-specific
// paths). When a data repo provides its own schema/ directory, its
// record.schema.json and taxonomy.json instances override those defaults.
package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// EmbeddingConfig is the optional "embedding" block of taxonomy.json:
// {"provider":"off"|"ollama","model":"...","base_url":"..."}. It lives here —
// not in internal/embed — so schema, store and index can parse and carry it
// without gaining a network-client dependency (NFR-NET-01 guards).
type EmbeddingConfig struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	BaseURL  string `json:"base_url"`
}

// Embedding defaults: off mode, nomic-embed-text, local Ollama.
const (
	EmbeddingProviderOff    = "off"
	EmbeddingProviderOllama = "ollama"
	EmbeddingDefaultModel   = "nomic-embed-text"
	EmbeddingDefaultBaseURL = "http://127.0.0.1:11434"
)

// WithDefaults fills the documented defaults: provider off, model
// nomic-embed-text, base URL http://127.0.0.1:11434.
func (c EmbeddingConfig) WithDefaults() EmbeddingConfig {
	out := c
	if out.Provider == "" {
		out.Provider = EmbeddingProviderOff
	}
	if out.Model == "" {
		out.Model = EmbeddingDefaultModel
	}
	if out.BaseURL == "" {
		out.BaseURL = EmbeddingDefaultBaseURL
	}
	return out
}

// Enabled reports whether the config selects a live provider (not off/empty).
func (c EmbeddingConfig) Enabled() bool {
	p := strings.TrimSpace(strings.ToLower(c.Provider))
	return p != "" && p != EmbeddingProviderOff
}

// SchemaVersion is the record schema version this engine writes and supports.
// A record carrying any other version is refused.
const SchemaVersion = 1

// --- taxonomy types --------------------------------------------------------

// LayerDef holds per-layer metadata from taxonomy.json.
type LayerDef struct {
	Dir             string `json:"dir"`
	FileMode        string `json:"file_mode"`
	ExpiresRequired bool   `json:"expires_required"`
	Inject          string `json:"inject"`
}

// Scoring holds the scoring weights from taxonomy.json.
type Scoring struct {
	WConf         float64 `json:"w_conf"`
	WRecency      float64 `json:"w_recency"`
	TauDays       float64 `json:"tau_days"`
	WUses         float64 `json:"w_uses"`
	ThresholdHook float64 `json:"threshold_hook"`
}

// Schema holds the effective schema configuration.
type Schema struct {
	SchemaVersion   int
	TaxonomyVersion int
	AgentsVersion   int
	AgentCount      int

	Layers         map[string]LayerDef
	LayerOrder     []string // taxonomy declaration order (Python scan order)
	Budgets        map[string]int
	Tags           []string
	SecretPatterns []string
	ForbiddenPaths []string
	Scoring        Scoring

	// Embedding is the optional semantic-search block (off | ollama). The
	// zero value means "block absent" which WithDefaults resolves to off —
	// retrieval without it is byte-identical to the pre-embedding engine.
	Embedding EmbeddingConfig

	// Structural rules (defaults, overridable by record.schema.json).
	Required           []string
	ValidLayers        map[string]bool
	ValidStatuses      map[string]bool
	ValidSensitivities map[string]bool
	ValidScopes        *regexp.Regexp
	IDPattern          *regexp.Regexp
	DatePattern        *regexp.Regexp
	TitleMinLength     int
}

// --- portable defaults -----------------------------------------------------

var defaultLayers = map[string]LayerDef{
	"rule":      {Dir: "rules", FileMode: "one-per-topic", ExpiresRequired: false, Inject: "always"},
	"project":   {Dir: "projects", FileMode: "one-per-entity", ExpiresRequired: false, Inject: "scope-match"},
	"lesson":    {Dir: "lessons", FileMode: "one-per-fact", ExpiresRequired: false, Inject: "relevance"},
	"decision":  {Dir: "decisions", FileMode: "one-per-fact", ExpiresRequired: false, Inject: "scope-match"},
	"handoff":   {Dir: "handoffs", FileMode: "one-per-fact", ExpiresRequired: true, Inject: "relevance+not-expired"},
	"knowledge": {Dir: "knowledge", FileMode: "one-per-topic", ExpiresRequired: false, Inject: "pointer-only"},
	"ops":       {Dir: "ops", FileMode: "append-only", ExpiresRequired: false, Inject: "never"},
}

// defaultLayerOrder is the taxonomy order the Python baseline declares — the
// order its scan walks layers in, so record order (and every stable sort built
// on it) depends on it.
var defaultLayerOrder = []string{"rule", "project", "lesson", "decision", "handoff", "knowledge", "ops"}

var defaultBudgets = map[string]int{
	"rules": 5400, "project": 6000, "lesson": 8000,
	"handoff": 4000, "decision": 3000, "knowledge": 1200, "total": 20000,
}

var defaultTags = []string{
	"pr-ops", "git", "ci", "test", "build", "xcode", "ios", "macos", "swift",
	"swiftdata", "python", "typescript", "nextjs", "solidity", "mql5", "trading",
	"verification", "statistics", "agent-tooling", "memory", "skills", "mcp",
	"obsidian", "cron", "security", "privacy", "appstore", "healthkit", "widgetkit",
	"watchos", "model-routing", "worktree", "release", "opencode", "plugin",
	"hooks", "workflow", "orchestration", "agents", "permissions", "meta-loop",
}

var defaultSecretPatterns = []string{
	`(?i)\b(api[_-]?key|secret|password|passwd|token|bearer)\b\s*[:=]\s*["']?[A-Za-z0-9_\-]{16,}`,
	`gh[pousr]_[A-Za-z0-9]{20,}`,
	`sk-[A-Za-z0-9]{20,}`,
	`AKIA[0-9A-Z]{16}`,
	`-----BEGIN [A-Z ]*PRIVATE KEY-----`,
	`xox[baprs]-[A-Za-z0-9-]{10,}`,
	`\beyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.`,
}

var defaultForbiddenPaths = []string{"/Users/", "/home/", `C:\Users\`}

var defaultScoring = Scoring{WConf: 1.0, TauDays: 14, ThresholdHook: 6.0}

var defaultRequired = []string{"id", "layer", "scope", "created", "status", "sensitivity"}

var defaultStatuses = []string{"active", "superseded", "archived", "promoted", "compacted"}

var defaultSensitivities = []string{"public", "internal", "private", "quarantine"}

const (
	defaultIDPattern    = `^(rule|project|lesson|decision|handoff|knowledge|ops)\.[0-9a-f]{8,16}$`
	defaultScopePattern = `^(global|project:.+|repo:.+/.+|agent:.+)$`
	defaultDatePattern  = `^[0-9]{4}-[0-9]{2}-[0-9]{2}$`
	defaultTitleMinLen  = 3
)

// --- raw JSON shapes -------------------------------------------------------

type rawTaxonomy struct {
	Version        int                 `json:"version"`
	Layers         map[string]LayerDef `json:"layers"`
	BudgetsChars   map[string]int      `json:"budgets_chars"`
	Scoring        Scoring             `json:"scoring"`
	Tags           []string            `json:"tags"`
	SecretPatterns []string            `json:"secret_patterns"`
	ForbiddenPaths []string            `json:"forbidden_path_fragments"`

	// Embedding is the optional semantic-search block; absent (nil) = off.
	// Unknown keys inside the block are tolerated like every other block.
	Embedding *EmbeddingConfig `json:"embedding,omitempty"`
}

type rawProperty struct {
	Type      any      `json:"type"`
	Pattern   string   `json:"pattern"`
	Enum      []string `json:"enum"`
	MinLength *int     `json:"minLength"`
}

type rawRecordSchema struct {
	Version    *int                   `json:"version"`
	Required   []string               `json:"required"`
	Properties map[string]rawProperty `json:"properties"`
}

type rawAgents struct {
	Version int `json:"version"`
	Agents  []struct {
		Name string `json:"name"`
	} `json:"agents"`
}

// --- LoadSchema ------------------------------------------------------------

// LoadSchema reads the schema instance from root (a data-repo root that
// contains schema/record.schema.json, schema/taxonomy.json and
// schema/agents.json). Missing files fall back to the engine's portable
// defaults; malformed files are a hard error (never silently ignored).
func LoadSchema(root string) (Schema, error) {
	s := buildDefault()

	if root == "" {
		return s, nil
	}

	// record.schema.json — structural rules override defaults.
	idPatternSet := false
	recPath := filepath.Join(root, "schema", "record.schema.json")
	if data, err := os.ReadFile(recPath); err == nil {
		var rs rawRecordSchema
		if err := json.Unmarshal(data, &rs); err != nil {
			return Schema{}, fmt.Errorf("schema: parse %s: %w", recPath, err)
		}
		idPatternSet, err = s.applyRecordSchema(rs)
		if err != nil {
			return Schema{}, fmt.Errorf("schema: %s: %w", recPath, err)
		}
		if rs.Version != nil && *rs.Version > 0 {
			s.SchemaVersion = *rs.Version
		}
	} else if !os.IsNotExist(err) {
		return Schema{}, fmt.Errorf("schema: read %s: %w", recPath, err)
	}

	// taxonomy.json — layers/budgets/tags/patterns override defaults.
	taxPath := filepath.Join(root, "schema", "taxonomy.json")
	if data, err := os.ReadFile(taxPath); err == nil {
		var t rawTaxonomy
		if err := json.Unmarshal(data, &t); err != nil {
			return Schema{}, fmt.Errorf("schema: parse %s: %w", taxPath, err)
		}
		if len(t.Layers) > 0 {
			s.Layers = t.Layers
			s.ValidLayers = stringSet(sortedMapKeys(t.Layers))
			s.LayerOrder = taxonomyLayerOrder(data, t.Layers)
		}
		if len(t.BudgetsChars) > 0 {
			s.Budgets = t.BudgetsChars
		}
		if len(t.Tags) > 0 {
			s.Tags = t.Tags
		}
		if len(t.SecretPatterns) > 0 {
			s.SecretPatterns = t.SecretPatterns
		}
		if len(t.ForbiddenPaths) > 0 {
			s.ForbiddenPaths = t.ForbiddenPaths
		}
		if t.Scoring.WConf != 0 || t.Scoring.TauDays != 0 {
			s.Scoring = t.Scoring
		}
		if t.Embedding != nil {
			s.Embedding = t.Embedding.WithDefaults()
		}
		s.TaxonomyVersion = t.Version
	} else if !os.IsNotExist(err) {
		return Schema{}, fmt.Errorf("schema: read %s: %w", taxPath, err)
	}

	// When record.schema.json does not pin an id pattern, derive it from the
	// effective layer set so a taxonomy layer override stays self-consistent.
	if !idPatternSet {
		s.IDPattern = deriveIDPattern(s.ValidLayers)
	}

	// agents.json — adapter registry version/count (diagnostics).
	agPath := filepath.Join(root, "schema", "agents.json")
	if data, err := os.ReadFile(agPath); err == nil {
		var ag rawAgents
		if err := json.Unmarshal(data, &ag); err != nil {
			return Schema{}, fmt.Errorf("schema: parse %s: %w", agPath, err)
		}
		s.AgentsVersion = ag.Version
		s.AgentCount = len(ag.Agents)
	} else if !os.IsNotExist(err) {
		return Schema{}, fmt.Errorf("schema: read %s: %w", agPath, err)
	}

	return s, nil
}

// applyRecordSchema overrides structural defaults from record.schema.json.
// It reports whether the id pattern was pinned explicitly.
func (s *Schema) applyRecordSchema(rs rawRecordSchema) (bool, error) {
	if len(rs.Required) > 0 {
		s.Required = append([]string(nil), rs.Required...)
	}

	idPatternSet := false
	if p, ok := rs.Properties["id"]; ok && p.Pattern != "" {
		re, err := regexp.Compile(p.Pattern)
		if err != nil {
			return false, fmt.Errorf("property \"id\": invalid pattern: %w", err)
		}
		s.IDPattern = re
		idPatternSet = true
	}
	if p, ok := rs.Properties["scope"]; ok && p.Pattern != "" {
		re, err := regexp.Compile(p.Pattern)
		if err != nil {
			return false, fmt.Errorf("property \"scope\": invalid pattern: %w", err)
		}
		s.ValidScopes = re
	}
	if p, ok := rs.Properties["created"]; ok && p.Pattern != "" {
		re, err := regexp.Compile(p.Pattern)
		if err != nil {
			return false, fmt.Errorf("property \"created\": invalid pattern: %w", err)
		}
		s.DatePattern = re
	}
	if p, ok := rs.Properties["layer"]; ok && len(p.Enum) > 0 {
		s.ValidLayers = stringSet(p.Enum)
	}
	if p, ok := rs.Properties["status"]; ok && len(p.Enum) > 0 {
		s.ValidStatuses = stringSet(p.Enum)
	}
	if p, ok := rs.Properties["sensitivity"]; ok && len(p.Enum) > 0 {
		s.ValidSensitivities = stringSet(p.Enum)
	}
	if p, ok := rs.Properties["title"]; ok && p.MinLength != nil {
		s.TitleMinLength = *p.MinLength
	}
	return idPatternSet, nil
}

// deriveIDPattern builds "^<layer>.<8-16 hex>$" for the effective layer set.
func deriveIDPattern(layers map[string]bool) *regexp.Regexp {
	names := make([]string, 0, len(layers))
	for name := range layers {
		names = append(names, regexp.QuoteMeta(name))
	}
	sort.Strings(names)
	return regexp.MustCompile(`^(` + strings.Join(names, "|") + `)\.[0-9a-f]{8,16}$`)
}

// Defaults returns the engine's portable schema with no data repo instance
// read. `mem init` uses it to scaffold schema/taxonomy.json from the same
// numbers the loader falls back to, so a fresh home and a schema-less home can
// never drift apart.
func Defaults() Schema { return buildDefault() }

// buildDefault returns a Schema populated with portable defaults.
func buildDefault() Schema {
	return Schema{
		SchemaVersion:      SchemaVersion,
		Layers:             copyLayers(defaultLayers),
		LayerOrder:         append([]string(nil), defaultLayerOrder...),
		Budgets:            copyInts(defaultBudgets),
		Tags:               append([]string(nil), defaultTags...),
		SecretPatterns:     append([]string(nil), defaultSecretPatterns...),
		ForbiddenPaths:     append([]string(nil), defaultForbiddenPaths...),
		Scoring:            defaultScoring,
		Embedding:          EmbeddingConfig{}.WithDefaults(),
		Required:           append([]string(nil), defaultRequired...),
		ValidLayers:        stringSet(sortedMapKeys(defaultLayers)),
		ValidStatuses:      stringSet(defaultStatuses),
		ValidSensitivities: stringSet(defaultSensitivities),
		ValidScopes:        regexp.MustCompile(defaultScopePattern),
		IDPattern:          regexp.MustCompile(defaultIDPattern),
		DatePattern:        regexp.MustCompile(defaultDatePattern),
		TitleMinLength:     defaultTitleMinLen,
	}
}

// --- validation ------------------------------------------------------------

// ValidateRecord enforces the effective schema on a parsed frontmatter map
// (map[string]any as returned by codec.ParseFront). The first violation is
// returned as an error naming the offending field.
func (s *Schema) ValidateRecord(rec map[string]any) error {
	_, err := s.ValidateRecordWithWarnings(rec)
	return err
}

// ValidateRecordWithWarnings is ValidateRecord plus soft diagnostics: tags
// outside the taxonomy are reported as warnings rather than hard failures
// (AC-P1-04). The record is accepted when err is nil.
func (s *Schema) ValidateRecordWithWarnings(rec map[string]any) ([]string, error) {
	// 1. required fields, in the schema's declared order.
	for _, f := range s.Required {
		if _, ok := rec[f]; !ok {
			return nil, fmt.Errorf("schema: missing required field %q", f)
		}
	}

	// 2. layer — string enum.
	layer, ok := rec["layer"].(string)
	if !ok {
		return nil, fmt.Errorf("schema: field \"layer\" must be a string")
	}
	if !s.ValidLayers[layer] {
		return nil, fmt.Errorf("schema: field \"layer\" has unsupported value %q (must be one of: %s)",
			layer, strings.Join(sortedBoolKeys(s.ValidLayers), ", "))
	}

	// 3. id — string, pattern, and prefix consistent with layer.
	idVal, ok := rec["id"].(string)
	if !ok {
		return nil, fmt.Errorf("schema: field \"id\" must be a string")
	}
	if !s.IDPattern.MatchString(idVal) {
		return nil, fmt.Errorf("schema: field \"id\" does not match pattern %s (got %q)", s.IDPattern, idVal)
	}
	if prefix, _, found := strings.Cut(idVal, "."); found && prefix != layer {
		return nil, fmt.Errorf("schema: field \"id\" prefix %q does not match layer %q", prefix, layer)
	}

	// 4. scope — string pattern.
	scope, ok := rec["scope"].(string)
	if !ok {
		return nil, fmt.Errorf("schema: field \"scope\" must be a string")
	}
	if !s.ValidScopes.MatchString(scope) {
		return nil, fmt.Errorf("schema: field \"scope\" does not match pattern %s (got %q)", s.ValidScopes, scope)
	}

	// 5. created — required date.
	if err := s.validateDate(rec, "created"); err != nil {
		return nil, err
	}

	// 6. status — string enum.
	status, ok := rec["status"].(string)
	if !ok {
		return nil, fmt.Errorf("schema: field \"status\" must be a string")
	}
	if !s.ValidStatuses[status] {
		return nil, fmt.Errorf("schema: field \"status\" has unsupported value %q (must be one of: %s)",
			status, strings.Join(sortedBoolKeys(s.ValidStatuses), ", "))
	}

	// 7. sensitivity — string enum.
	sens, ok := rec["sensitivity"].(string)
	if !ok {
		return nil, fmt.Errorf("schema: field \"sensitivity\" must be a string")
	}
	if !s.ValidSensitivities[sens] {
		return nil, fmt.Errorf("schema: field \"sensitivity\" has unsupported value %q (must be one of: %s)",
			sens, strings.Join(sortedBoolKeys(s.ValidSensitivities), ", "))
	}

	// 8. schema_version — integer equal to the supported version.
	if sv, exists := rec["schema_version"]; exists {
		v, err := intValue("schema_version", sv)
		if err != nil {
			return nil, err
		}
		if v != s.SchemaVersion {
			return nil, fmt.Errorf("schema: field \"schema_version\" %d is not supported (engine supports %d)", v, s.SchemaVersion)
		}
	}

	// 9. optional date fields.
	for _, f := range []string{"archived_at", "last_used"} {
		if _, ok := rec[f]; ok {
			if err := s.validateDate(rec, f); err != nil {
				return nil, err
			}
		}
	}

	// 10. tags and evidence — lists of strings; unknown tags are warnings.
	var warnings []string
	for _, f := range []string{"tags", "evidence"} {
		v, ok := rec[f]
		if !ok {
			continue
		}
		items, err := toStringSlice(v)
		if err != nil {
			return nil, fmt.Errorf("schema: field %q must be a list of strings", f)
		}
		if f == "tags" {
			warnings = append(warnings, s.unknownTags(items)...)
		}
	}

	// 11. confidence, decay — numbers in [0,1].
	for _, f := range []string{"confidence", "decay"} {
		if v, ok := rec[f]; ok {
			if err := validateFloat01(f, v); err != nil {
				return nil, err
			}
		}
	}

	// 12. uses, successes — non-negative integers.
	for _, f := range []string{"uses", "successes"} {
		if v, ok := rec[f]; ok {
			if err := validateNonNegInt(f, v); err != nil {
				return nil, err
			}
		}
	}

	// 13. title — string with the schema's minimum length.
	if v, ok := rec["title"]; ok {
		title, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("schema: field \"title\" must be a string")
		}
		if len([]rune(title)) < s.TitleMinLength {
			return nil, fmt.Errorf("schema: field \"title\" must be at least %d characters", s.TitleMinLength)
		}
	}

	// 14. expires is a freeform string (no format constraint).
	if v, ok := rec["expires"]; ok {
		if _, ok := v.(string); !ok {
			return nil, fmt.Errorf("schema: field \"expires\" must be a string")
		}
	}

	return warnings, nil
}

// unknownTags returns one warning per tag absent from the taxonomy tag list.
func (s *Schema) unknownTags(tags []string) []string {
	known := stringSet(s.Tags)
	var out []string
	for _, t := range tags {
		if !known[t] {
			out = append(out, fmt.Sprintf("schema: warning: tag %q is not in taxonomy", t))
		}
	}
	return out
}

func (s *Schema) validateDate(rec map[string]any, field string) error {
	v, ok := rec[field].(string)
	if !ok {
		return fmt.Errorf("schema: field %q must be a string date", field)
	}
	if !s.DatePattern.MatchString(v) {
		return fmt.Errorf("schema: field %q must match %s (got %q)", field, s.DatePattern, v)
	}
	return nil
}

// --- helpers ---------------------------------------------------------------

func toStringSlice(v any) ([]string, error) {
	switch t := v.(type) {
	case []string:
		return t, nil
	case []any:
		out := make([]string, len(t))
		for i, item := range t {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("element %d is not a string", i)
			}
			out[i] = s
		}
		return out, nil
	default:
		return nil, fmt.Errorf("not a list")
	}
}

func validateFloat01(field string, v any) error {
	var f float64
	switch t := v.(type) {
	case float64:
		f = t
	case int:
		f = float64(t)
	default:
		return fmt.Errorf("schema: field %q must be a number", field)
	}
	if f < 0 || f > 1 {
		return fmt.Errorf("schema: field %q must be in [0, 1] (got %v)", field, f)
	}
	return nil
}

func validateNonNegInt(field string, v any) error {
	switch t := v.(type) {
	case int:
		if t < 0 {
			return fmt.Errorf("schema: field %q must be >= 0 (got %d)", field, t)
		}
	case float64:
		if t < 0 || t != float64(int(t)) {
			return fmt.Errorf("schema: field %q must be a non-negative integer (got %v)", field, t)
		}
	default:
		return fmt.Errorf("schema: field %q must be an integer", field)
	}
	return nil
}

func intValue(field string, v any) (int, error) {
	switch t := v.(type) {
	case int:
		return t, nil
	case int64:
		return int(t), nil
	case float64:
		if t != float64(int(t)) {
			return 0, fmt.Errorf("schema: field %q must be an integer (got %v)", field, t)
		}
		return int(t), nil
	default:
		return 0, fmt.Errorf("schema: field %q must be an integer", field)
	}
}

func stringSet(values []string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}
	return m
}

func sortedMapKeys(m map[string]LayerDef) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// taxonomyLayerOrder returns the layer names in the order taxonomy.json
// declares them. Go map decoding loses that order, so the raw document is
// re-tokenized: Python scan walks `ctx.layers.items()` in declaration order
// and that order is the tie-break of every stable sort downstream. Keys
// present in the map but absent from the document (impossible for a decoded
// document, but cheap insurance) are appended alphabetically.
func taxonomyLayerOrder(data []byte, layers map[string]LayerDef) []string {
	order, ok := jsonObjectKeyOrder(data, "layers")
	if !ok {
		return sortedMapKeys(layers)
	}
	seen := make(map[string]bool, len(order))
	out := make([]string, 0, len(layers))
	for _, k := range order {
		if _, known := layers[k]; known && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, k := range sortedMapKeys(layers) {
		if !seen[k] {
			out = append(out, k)
		}
	}
	return out
}

// jsonObjectKeyOrder scans a JSON document for the object member named field
// at the top level and returns that object's keys in document order.
func jsonObjectKeyOrder(data []byte, field string) ([]string, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, _ := keyTok.(string)
		if key != field {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, false
			}
			continue
		}
		if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
			return nil, false
		}
		var keys []string
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, false
			}
			k, _ := keyTok.(string)
			keys = append(keys, k)
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, false
			}
		}
		if _, err := dec.Token(); err != nil { // closing brace
			return nil, false
		}
		return keys, true
	}
	return nil, false
}

func sortedBoolKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func copyLayers(in map[string]LayerDef) map[string]LayerDef {
	out := make(map[string]LayerDef, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyInts(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
