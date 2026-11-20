// Package schema tests the schema loader and record enforcement.
// All fixtures are synthetic (provenance: test-generated); no real corpus is used.
package schema

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// fixtureRoot returns testdata/ (the data-repo root analogue) so LoadSchema
// reads testdata/schema/{record.schema.json,taxonomy.json,agents.json}.
func fixtureRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "testdata"))
}

// validRecord builds a minimal valid synthetic record for a layer.
func validRecord(layer string) map[string]any {
	return map[string]any{
		"id":          layer + ".aabbccdd",
		"layer":       layer,
		"scope":       "global",
		"created":     "2026-01-01",
		"status":      "active",
		"sensitivity": "internal",
		"title":       "Synthetic " + layer + " record",
	}
}

// --- LoadSchema: defaults (empty root) --------------------------------------

func TestLoadSchema_Defaults(t *testing.T) {
	s, err := LoadSchema("")
	if err != nil {
		t.Fatalf("LoadSchema(\"\") error: %v", err)
	}
	if s.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion: got %d, want %d", s.SchemaVersion, SchemaVersion)
	}
	if len(s.Layers) != 7 {
		t.Errorf("Layers: got %d, want 7", len(s.Layers))
	}
	for _, layer := range []string{"rule", "project", "lesson", "decision", "handoff", "knowledge", "ops"} {
		if !s.ValidLayers[layer] {
			t.Errorf("ValidLayers missing %q", layer)
		}
	}
	if len(s.Required) != 6 {
		t.Errorf("Required: got %d fields, want 6", len(s.Required))
	}
	if len(s.SecretPatterns) == 0 {
		t.Error("SecretPatterns must not be empty in defaults")
	}
	if len(s.ForbiddenPaths) == 0 {
		t.Error("ForbiddenPaths must not be empty in defaults")
	}
	if s.TitleMinLength != 3 {
		t.Errorf("TitleMinLength: got %d, want 3", s.TitleMinLength)
	}
}

// concreteUserPath matches a home directory followed by a real user name
// (`/Users/<name>`, `/home/<name>`, `C:\Users\<name>`) — not the bare
// forbidden-path fragment, which schema defaults carry by design.
var concreteUserPath = regexp.MustCompile(`(/Users/|/home/|C:\\Users\\)[A-Za-z0-9._-]`)

// TestLoadSchema_DefaultsHaveNoMachinePaths proves TB-5 for schema defaults.
func TestLoadSchema_DefaultsHaveNoMachinePaths(t *testing.T) {
	s, _ := LoadSchema("")
	// Only check the data defaults, which must never embed a home directory.
	blob := strings.Join(append(append([]string{}, s.Tags...), s.SecretPatterns...), "\n")
	if concreteUserPath.MatchString(blob) {
		t.Errorf("schema defaults embed a machine-specific path")
	}
}

// --- LoadSchema: from fixture root ------------------------------------------

func TestLoadSchema_FromFixtureRoot(t *testing.T) {
	root := fixtureRoot(t)
	s, err := LoadSchema(root)
	if err != nil {
		t.Fatalf("LoadSchema(%q) error: %v", root, err)
	}
	if s.SchemaVersion != 1 {
		t.Errorf("SchemaVersion from fixture: got %d, want 1", s.SchemaVersion)
	}
	if len(s.Layers) != 7 {
		t.Errorf("Layers from fixture: got %d, want 7", len(s.Layers))
	}
	if s.AgentsVersion != 2 {
		t.Errorf("AgentsVersion from fixture: got %d, want 2", s.AgentsVersion)
	}
	if s.TaxonomyVersion != 1 {
		t.Errorf("TaxonomyVersion from fixture: got %d, want 1", s.TaxonomyVersion)
	}
	// fixture taxonomy narrows tags to three entries
	if len(s.Tags) != 3 {
		t.Errorf("Tags from fixture: got %d, want 3", len(s.Tags))
	}
}

// --- LoadSchema: data-repo instance overrides defaults ----------------------

func TestLoadSchema_DataRepoOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "schema", "record.schema.json"), []byte(`{
		"required": ["id", "layer", "scope", "created", "status", "sensitivity", "expires"],
		"properties": {
			"layer": { "enum": ["rule", "custom"] },
			"status": { "enum": ["active", "retired"] },
			"sensitivity": { "enum": ["public"] },
			"title": { "minLength": 5 }
		}
	}`))
	writeFile(t, filepath.Join(dir, "schema", "taxonomy.json"), []byte(`{
		"version": 7,
		"layers": { "rule": { "dir": "rules" }, "custom": { "dir": "customs" } }
	}`))

	s, err := LoadSchema(dir)
	if err != nil {
		t.Fatalf("LoadSchema error: %v", err)
	}
	if len(s.Required) != 7 {
		t.Errorf("Required override: got %d fields, want 7", len(s.Required))
	}
	if !s.ValidLayers["custom"] {
		t.Error("layer enum override lost \"custom\"")
	}
	if s.ValidLayers["lesson"] {
		t.Error("layer enum override should drop \"lesson\"")
	}
	if !s.ValidStatuses["retired"] || s.ValidStatuses["archived"] {
		t.Error("status enum override not applied")
	}
	if len(s.ValidSensitivities) != 1 || !s.ValidSensitivities["public"] {
		t.Error("sensitivity enum override not applied")
	}
	if s.TitleMinLength != 5 {
		t.Errorf("TitleMinLength override: got %d, want 5", s.TitleMinLength)
	}
	if s.TaxonomyVersion != 7 {
		t.Errorf("TaxonomyVersion: got %d, want 7", s.TaxonomyVersion)
	}
	if s.Layers["custom"].Dir != "customs" {
		t.Error("layer dir override not applied")
	}
	// id pattern is derived from the effective layer set when the record
	// schema does not pin one.
	if !s.IDPattern.MatchString("custom.abcdef12") {
		t.Errorf("derived id pattern should accept a custom-layer id, pattern=%s", s.IDPattern)
	}
	if s.IDPattern.MatchString("lesson.abcdef12") {
		t.Errorf("derived id pattern should reject a dropped layer, pattern=%s", s.IDPattern)
	}

	// override is enforced: expires now required, retired status allowed
	rec := map[string]any{
		"id": "rule.aabbccdd", "layer": "rule", "scope": "global",
		"created": "2026-01-01", "status": "retired", "sensitivity": "public",
		"title": "A long enough title", "expires": "2027-01-01",
	}
	if err := s.ValidateRecord(rec); err != nil {
		t.Errorf("record matching data-repo schema should pass: %v", err)
	}
	delete(rec, "expires")
	if err := s.ValidateRecord(rec); err == nil || !strings.Contains(err.Error(), "expires") {
		t.Errorf("missing expires should be rejected naming the field, got: %v", err)
	}
}

// --- ValidateRecord: all seven layers accepted ------------------------------

func TestValidateRecord_AllLayersAccepted(t *testing.T) {
	s, _ := LoadSchema("")
	for _, layer := range []string{"rule", "project", "lesson", "decision", "handoff", "knowledge", "ops"} {
		if err := s.ValidateRecord(validRecord(layer)); err != nil {
			t.Errorf("layer %q: unexpected rejection: %v", layer, err)
		}
	}
}

// --- ValidateRecord: missing required fields --------------------------------

func TestValidateRecord_MissingRequired(t *testing.T) {
	s, _ := LoadSchema("")
	for _, missing := range []string{"id", "layer", "scope", "created", "status", "sensitivity"} {
		rec := validRecord("lesson")
		delete(rec, missing)
		err := s.ValidateRecord(rec)
		if err == nil {
			t.Errorf("missing %q: expected error, got nil", missing)
			continue
		}
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("missing %q: error should name the field, got: %v", missing, err)
		}
	}
}

// --- ValidateRecord: wrong type for layer -----------------------------------

func TestValidateRecord_WrongLayerType(t *testing.T) {
	s, _ := LoadSchema("")
	rec := validRecord("lesson")
	rec["layer"] = 42
	err := s.ValidateRecord(rec)
	if err == nil || !strings.Contains(err.Error(), "layer") {
		t.Fatalf("expected layer-typed error, got: %v", err)
	}
}

// --- ValidateRecord: unknown layer value ------------------------------------

func TestValidateRecord_UnknownLayer(t *testing.T) {
	s, _ := LoadSchema("")
	rec := map[string]any{
		"id": "unknown.aabbccdd", "layer": "unknown", "scope": "global",
		"created": "2026-01-01", "status": "active", "sensitivity": "internal",
	}
	err := s.ValidateRecord(rec)
	if err == nil || !strings.Contains(err.Error(), "layer") {
		t.Fatalf("expected layer-enum error, got: %v", err)
	}
}

// --- ValidateRecord: id prefix mismatch -------------------------------------

func TestValidateRecord_IDPrefixMismatch(t *testing.T) {
	s, _ := LoadSchema("")
	rec := validRecord("lesson")
	rec["id"] = "rule.aabbccdd"
	err := s.ValidateRecord(rec)
	if err == nil || !strings.Contains(err.Error(), "id") {
		t.Fatalf("expected id-prefix error, got: %v", err)
	}
}

// --- ValidateRecord: bad id pattern -----------------------------------------

func TestValidateRecord_BadIDPattern(t *testing.T) {
	s, _ := LoadSchema("")
	rec := validRecord("lesson")
	rec["id"] = "lesson.ZZZZZZZZ"
	err := s.ValidateRecord(rec)
	if err == nil || !strings.Contains(err.Error(), "id") {
		t.Fatalf("expected id-pattern error, got: %v", err)
	}
}

// --- ValidateRecord: bad scope ----------------------------------------------

func TestValidateRecord_BadScope(t *testing.T) {
	s, _ := LoadSchema("")
	rec := validRecord("lesson")
	rec["scope"] = "invalid-scope-format"
	err := s.ValidateRecord(rec)
	if err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("expected scope error, got: %v", err)
	}
}

// --- ValidateRecord: bad status ---------------------------------------------

func TestValidateRecord_BadStatus(t *testing.T) {
	s, _ := LoadSchema("")
	rec := validRecord("lesson")
	rec["status"] = "deleted"
	err := s.ValidateRecord(rec)
	if err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("expected status error, got: %v", err)
	}
}

// --- ValidateRecord: bad sensitivity ----------------------------------------

func TestValidateRecord_BadSensitivity(t *testing.T) {
	s, _ := LoadSchema("")
	rec := validRecord("lesson")
	rec["sensitivity"] = "top-secret"
	err := s.ValidateRecord(rec)
	if err == nil || !strings.Contains(err.Error(), "sensitivity") {
		t.Fatalf("expected sensitivity error, got: %v", err)
	}
}

// --- ValidateRecord: bad date -----------------------------------------------

func TestValidateRecord_BadDate(t *testing.T) {
	s, _ := LoadSchema("")
	rec := validRecord("lesson")
	rec["created"] = "01-01-2026"
	err := s.ValidateRecord(rec)
	if err == nil || !strings.Contains(err.Error(), "created") {
		t.Fatalf("expected created-date error, got: %v", err)
	}
}

// --- ValidateRecord: schema_version mismatch --------------------------------

func TestValidateRecord_SchemaVersionMismatch(t *testing.T) {
	s, _ := LoadSchema("")
	rec := validRecord("lesson")
	rec["schema_version"] = 999
	err := s.ValidateRecord(rec)
	if err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Fatalf("expected schema_version error, got: %v", err)
	}
}

// --- ValidateRecord: schema_version matching --------------------------------

func TestValidateRecord_SchemaVersionMatch(t *testing.T) {
	s, _ := LoadSchema("")
	rec := validRecord("rule")
	rec["schema_version"] = SchemaVersion
	if err := s.ValidateRecord(rec); err != nil {
		t.Errorf("valid schema_version should be accepted: %v", err)
	}
}

// --- ValidateRecord: schema_version as float64 (JSON decode) ----------------

func TestValidateRecord_SchemaVersionFloat64(t *testing.T) {
	s, _ := LoadSchema("")
	rec := validRecord("decision")
	rec["schema_version"] = float64(SchemaVersion)
	if err := s.ValidateRecord(rec); err != nil {
		t.Errorf("schema_version as float64(%d) should be accepted: %v", SchemaVersion, err)
	}
}

// --- ValidateRecord: confidence out of range --------------------------------

func TestValidateRecord_ConfidenceOutOfRange(t *testing.T) {
	s, _ := LoadSchema("")
	rec := validRecord("rule")
	rec["confidence"] = 1.5
	err := s.ValidateRecord(rec)
	if err == nil || !strings.Contains(err.Error(), "confidence") {
		t.Fatalf("expected confidence error, got: %v", err)
	}
}

// --- ValidateRecord: decay/half_life accepted (Phase 1 fields) --------------

func TestValidateRecord_DecayAndHalfLife(t *testing.T) {
	s, _ := LoadSchema("")
	rec := validRecord("lesson")
	rec["decay"] = 0.5
	rec["half_life"] = "30d"
	if err := s.ValidateRecord(rec); err != nil {
		t.Errorf("decay/half_life should be accepted: %v", err)
	}
	rec["half_life"] = 30
	if err := s.ValidateRecord(rec); err != nil {
		t.Errorf("numeric half_life should be accepted: %v", err)
	}
}

// --- ValidateRecord: superseded_by and uses accepted (AC-P1-04) -------------

func TestValidateRecord_EngineWrittenFields(t *testing.T) {
	s, _ := LoadSchema("")
	rec := validRecord("lesson")
	rec["superseded_by"] = "lesson.11223344"
	rec["uses"] = 3
	rec["successes"] = 2
	rec["schema_version"] = SchemaVersion
	if err := s.ValidateRecord(rec); err != nil {
		t.Errorf("superseded_by/uses/successes/schema_version should be accepted: %v", err)
	}
}

// --- ValidateRecord: tags valid + unknown tag warning -----------------------

func TestValidateRecord_TagsValid(t *testing.T) {
	s, _ := LoadSchema("")
	rec := validRecord("lesson")
	rec["tags"] = []string{"git", "test"}
	warnings, err := s.ValidateRecordWithWarnings(rec)
	if err != nil {
		t.Errorf("valid tags should be accepted: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("known tags should not warn, got: %v", warnings)
	}
}

func TestValidateRecord_UnknownTagWarns(t *testing.T) {
	s, _ := LoadSchema("")
	rec := validRecord("lesson")
	rec["tags"] = []string{"git", "totally-unknown"}
	warnings, err := s.ValidateRecordWithWarnings(rec)
	if err != nil {
		t.Fatalf("unknown tag must not be a hard failure: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "totally-unknown") {
		t.Errorf("expected one warning naming the tag, got: %v", warnings)
	}
}

func TestValidateRecord_TagsWrongType(t *testing.T) {
	s, _ := LoadSchema("")
	rec := validRecord("lesson")
	rec["tags"] = "not-a-list"
	err := s.ValidateRecord(rec)
	if err == nil || !strings.Contains(err.Error(), "tags") {
		t.Fatalf("expected tags-type error, got: %v", err)
	}
}

// --- ValidateRecord: title too short ----------------------------------------

func TestValidateRecord_TitleTooShort(t *testing.T) {
	s, _ := LoadSchema("")
	rec := validRecord("lesson")
	rec["title"] = "ab"
	err := s.ValidateRecord(rec)
	if err == nil || !strings.Contains(err.Error(), "title") {
		t.Fatalf("expected title error, got: %v", err)
	}
}

// --- ValidateRecord: scope variants accepted --------------------------------

func TestValidateRecord_ScopeVariants(t *testing.T) {
	s, _ := LoadSchema("")
	for _, scope := range []string{"global", "project:my-project", "repo:owner/name", "agent:claude"} {
		rec := validRecord("lesson")
		rec["scope"] = scope
		if err := s.ValidateRecord(rec); err != nil {
			t.Errorf("scope %q: unexpected rejection: %v", scope, err)
		}
	}
}

// --- ValidateRecord: uses/successes validation ------------------------------

func TestValidateRecord_NonNegativeInts(t *testing.T) {
	s, _ := LoadSchema("")

	rec := validRecord("rule")
	rec["uses"] = 5
	rec["successes"] = 3
	if err := s.ValidateRecord(rec); err != nil {
		t.Errorf("valid uses/successes: %v", err)
	}

	rec2 := validRecord("rule")
	rec2["uses"] = -1
	if err := s.ValidateRecord(rec2); err == nil || !strings.Contains(err.Error(), "uses") {
		t.Errorf("expected uses error, got: %v", err)
	}
}

// --- LoadSchema: malformed files are hard errors ----------------------------

func TestLoadSchema_BadTaxonomyJSON(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "schema", "taxonomy.json"), []byte("{invalid json"))
	if _, err := LoadSchema(dir); err == nil {
		t.Fatal("expected error for malformed taxonomy.json")
	}
}

func TestLoadSchema_BadRecordSchemaJSON(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "schema", "record.schema.json"), []byte("{invalid json"))
	if _, err := LoadSchema(dir); err == nil {
		t.Fatal("expected error for malformed record.schema.json")
	}
}

func TestLoadSchema_BadAgentsJSON(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "schema", "agents.json"), []byte("{invalid json"))
	if _, err := LoadSchema(dir); err == nil {
		t.Fatal("expected error for malformed agents.json")
	}
}

func TestLoadSchema_InvalidPattern(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "schema", "record.schema.json"),
		[]byte(`{"properties": {"id": {"pattern": "["}}}`))
	if _, err := LoadSchema(dir); err == nil {
		t.Fatal("expected error for invalid regex pattern in record.schema.json")
	}
}

// --- helpers ----------------------------------------------------------------

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// TestContentSHAProperty (KB P1): the optional content_sha256 must be a
// 64-hex string when present; absent stays valid.
func TestContentSHAProperty(t *testing.T) {
	sch := Defaults()
	base := map[string]any{
		"id": "rule.aabbccdd", "layer": "rule", "scope": "global",
		"created": "2026-01-01", "status": "active", "sensitivity": "internal",
	}
	if _, err := sch.ValidateRecordWithWarnings(base); err != nil {
		t.Fatalf("record without content_sha256 must stay valid: %v", err)
	}
	good := map[string]any{}
	for k, v := range base {
		good[k] = v
	}
	good["content_sha256"] = strings.Repeat("ab", 32) // 64 hex chars
	if _, err := sch.ValidateRecordWithWarnings(good); err != nil {
		t.Errorf("well-formed content_sha256 rejected: %v", err)
	}
	for i, bad := range []any{
		strings.Repeat("a", 63),
		strings.Repeat("A", 64),
		"zzzz" + strings.Repeat("0", 60),
		42,
	} {
		rec := map[string]any{}
		for k, v := range base {
			rec[k] = v
		}
		rec["content_sha256"] = bad
		if _, err := sch.ValidateRecordWithWarnings(rec); err == nil {
			t.Errorf("malformed content_sha256 #%d accepted: %v", i, bad)
		}
	}
}

// --- ValidateRecord: SIM fields (v0.3 problem/predicted/observed/model) -----

func TestValidateRecord_SIMFieldsAccepted(t *testing.T) {
	s, _ := LoadSchema("")
	rec := validRecord("lesson")
	rec["problem"] = "rate-limit-retry"
	rec["predicted"] = "backoff จะลด failed call ลงครึ่งหนึ่ง"
	rec["observed"] = "fixed: failed เหลือ 4%; note: วัดบน omp"
	rec["model"] = "omp"
	if err := s.ValidateRecord(rec); err != nil {
		t.Fatalf("SIM fields rejected: %v", err)
	}
}

func TestValidateRecord_SIMFieldsWrongType(t *testing.T) {
	s, _ := LoadSchema("")
	for _, f := range []string{"problem", "predicted", "observed", "model"} {
		rec := validRecord("lesson")
		rec[f] = 42
		err := s.ValidateRecord(rec)
		if err == nil {
			t.Errorf("%s: expected type error, got nil", f)
			continue
		}
		if !strings.Contains(err.Error(), f) {
			t.Errorf("%s: error should name the field, got: %v", f, err)
		}
	}
}

func TestValidateRecord_SIMFieldsTooLong(t *testing.T) {
	s, _ := LoadSchema("")
	cases := map[string]int{"problem": 81, "predicted": 201, "observed": 201, "model": 41}
	for f, n := range cases {
		rec := validRecord("lesson")
		rec[f] = strings.Repeat("x", n)
		err := s.ValidateRecord(rec)
		if err == nil {
			t.Errorf("%s at %d runes: expected cap error, got nil", f, n)
			continue
		}
		if !strings.Contains(err.Error(), f) || !strings.Contains(err.Error(), "ย่อ") {
			t.Errorf("%s: error should name the field and suggest abbreviating, got: %v", f, err)
		}
	}
	// Exactly at the cap is fine.
	rec := validRecord("lesson")
	rec["problem"] = strings.Repeat("x", 80)
	if err := s.ValidateRecord(rec); err != nil {
		t.Errorf("problem at exactly 80 runes rejected: %v", err)
	}
}

// A record.schema.json maxLength on a SIM field overrides the portable cap.
func TestValidateRecord_SIMFieldCapOverride(t *testing.T) {
	s := Defaults()
	s.FieldMaxLength["model"] = 8
	rec := validRecord("lesson")
	rec["model"] = "claude" // 6 ≤ 8
	if err := s.ValidateRecord(rec); err != nil {
		t.Fatalf("model within overridden cap rejected: %v", err)
	}
	rec["model"] = "gemini-flash" // 12 > 8
	if err := s.ValidateRecord(rec); err == nil {
		t.Fatal("model past overridden cap accepted")
	}
}
