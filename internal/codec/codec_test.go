package codec

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func mustParse(t *testing.T, text string) (Frontmatter, string) {
	t.Helper()
	fm, body, err := ParseFront(text)
	if err != nil {
		t.Fatalf("ParseFront error: %v", err)
	}
	return fm, body
}

// roundtrip asserts one rewrite of src reproduces its canonical form.
// Canonical form: DumpFront(fm) + "\n" + strings.TrimLeft(body, "\n")
// — mirrors Python mem's Rec.rewrite: dump_front(meta) + "\n" + body.
// Python's parse_front splits with str.splitlines(), so the body carries no
// trailing newline and the first rewrite drops the file's final "\n"
// (verified against `mem cite` on the Python baseline). A second rewrite is
// therefore byte-stable, which the second assertion pins.
func roundtrip(t *testing.T, src string) {
	t.Helper()
	fm, body, err := ParseFront(src)
	if err != nil {
		t.Fatalf("ParseFront error: %v", err)
	}
	got := DumpFront(fm) + "\n" + strings.TrimLeft(body, "\n")
	if want := strings.TrimSuffix(src, "\n"); got != want {
		t.Errorf("roundtrip mismatch:\n--- want ---\n%q\n--- got ---\n%q", want, got)
	}
	fm2, body2, err := ParseFront(got)
	if err != nil {
		t.Fatalf("ParseFront(rewrite) error: %v", err)
	}
	if again := DumpFront(fm2) + "\n" + strings.TrimLeft(body2, "\n"); again != got {
		t.Errorf("second rewrite is not byte-stable:\n--- want ---\n%q\n--- got ---\n%q", got, again)
	}
}

// ---------------------------------------------------------------------------
// Synthetic fixtures (embedded inline — no real corpus, synthetic + provenance)
// ---------------------------------------------------------------------------

// fixtureSimple is a minimal valid record (synthetic, provenance: test-generated).
const fixtureSimple = `---
id: lesson.aabbccdd
title: simple fixture
layer: lesson
scope: codec-test
tags: []
created: 2026-01-01
status: active
sensitivity: internal
---

Body text here.
`

// fixtureWithTags has a tags list (synthetic, provenance: test-generated).
const fixtureWithTags = `---
id: rule.deadbeef
title: rule with tags
layer: rule
scope: codec-test
tags: [go, testing, codec]
evidence: [E1, E2]
created: 2026-02-15
status: active
sensitivity: internal
---

Rule body.
`

// fixtureQuotedScalar has scalars that require quoting (synthetic, provenance: test-generated).
// title contains ": " → dumps quoted; confidence "0.9" parses to 0.9 → dumps unquoted.
const fixtureQuotedScalar = `---
id: lesson.11223344
title: "needs-quote: edge cases"
layer: lesson
scope: codec-test
tags: []
confidence: 0.9
source: "agent: oh-my-pi"
created: 2026-03-01
status: active
sensitivity: internal
---

Body with #hash and trailing whitespace issues.
`

// fixtureExtraKeys has extra keys beyond FRONT_ORDER (synthetic, provenance: test-generated).
const fixtureExtraKeys = `---
id: decision.cafecafe
title: decision record
layer: decision
scope: codec-test
tags: []
created: 2026-04-10
status: active
sensitivity: public
custom_field: some-value
---

Decision body.
`

// fixtureEmptyTags has an empty tags list via block style on parse, inline on dump (synthetic).
const fixtureEmptyTags = `---
id: knowledge.00001111
title: empty tags example
layer: knowledge
scope: codec-test
tags: []
evidence: []
created: 2026-05-01
status: active
sensitivity: internal
---

Knowledge body.
`

// ---------------------------------------------------------------------------
// TestScaffold (kept for backwards compat)
// ---------------------------------------------------------------------------

func TestScaffold(t *testing.T) {
	if 1+1 != 2 {
		t.Fatal("basic")
	}
}

// ---------------------------------------------------------------------------
// ParseFront
// ---------------------------------------------------------------------------

func TestParseFront_Simple(t *testing.T) {
	fm, body := mustParse(t, fixtureSimple)
	if fm["id"] != "lesson.aabbccdd" {
		t.Errorf("id: got %q", fm["id"])
	}
	if fm["layer"] != "lesson" {
		t.Errorf("layer: got %q", fm["layer"])
	}
	tags, ok := fm["tags"].([]string)
	if !ok {
		t.Fatalf("tags not []string: %T", fm["tags"])
	}
	if len(tags) != 0 {
		t.Errorf("tags should be empty, got %v", tags)
	}
	if !strings.Contains(body, "Body text") {
		t.Errorf("body missing expected text: %q", body)
	}
}

func TestParseFront_TagsList(t *testing.T) {
	fm, _ := mustParse(t, fixtureWithTags)
	tags, ok := fm["tags"].([]string)
	if !ok {
		t.Fatalf("tags not []string: %T", fm["tags"])
	}
	if len(tags) != 3 || tags[0] != "go" || tags[1] != "testing" || tags[2] != "codec" {
		t.Errorf("unexpected tags: %v", tags)
	}
	ev, ok := fm["evidence"].([]string)
	if !ok {
		t.Fatalf("evidence not []string: %T", fm["evidence"])
	}
	if len(ev) != 2 || ev[0] != "E1" || ev[1] != "E2" {
		t.Errorf("unexpected evidence: %v", ev)
	}
}

func TestParseFront_QuotedScalars(t *testing.T) {
	fm, _ := mustParse(t, fixtureQuotedScalar)
	// quoted value should be unquoted
	if fm["confidence"] != "0.9" {
		t.Errorf("confidence: got %q want 0.9", fm["confidence"])
	}
	if fm["source"] != "agent: oh-my-pi" {
		t.Errorf("source: got %q want 'agent: oh-my-pi'", fm["source"])
	}
}

func TestParseFront_NoFrontmatter(t *testing.T) {
	text := "just a plain body\nno frontmatter here"
	fm, body, err := ParseFront(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(fm) != 0 {
		t.Errorf("expected empty fm, got %v", fm)
	}
	if body != text {
		t.Errorf("body should equal original text when no frontmatter")
	}
}

func TestParseFront_MissingClosingFence(t *testing.T) {
	// Opening --- but no closing --- → treated as no frontmatter
	text := "---\nid: foo\nlayer: lesson\n"
	fm, body, err := ParseFront(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(fm) != 0 {
		t.Errorf("expected empty fm for unclosed frontmatter, got %v", fm)
	}
	if body != text {
		t.Errorf("body should equal original text: got %q", body)
	}
}

func TestParseFront_BoolValues(t *testing.T) {
	text := "---\nid: lesson.aaaabbbb\nlayer: lesson\nscope: test\ncreated: 2026-01-01\nstatus: active\nsensitivity: internal\nflag: true\nother: false\n---\n"
	fm, _, err := ParseFront(text)
	if err != nil {
		t.Fatal(err)
	}
	if fm["flag"] != true {
		t.Errorf("flag: want true, got %v (%T)", fm["flag"], fm["flag"])
	}
	if fm["other"] != false {
		t.Errorf("other: want false, got %v (%T)", fm["other"], fm["other"])
	}
}

// ---------------------------------------------------------------------------
// DumpFront
// ---------------------------------------------------------------------------

func TestDumpFront_FieldOrder(t *testing.T) {
	fm := Frontmatter{
		"status":      "active",
		"id":          "lesson.aabbccdd",
		"layer":       "lesson",
		"scope":       "test",
		"created":     "2026-01-01",
		"sensitivity": "internal",
		"tags":        []string{},
		"title":       "my title",
	}
	out := DumpFront(fm)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// First line must be ---
	if lines[0] != "---" {
		t.Fatalf("first line must be ---, got %q", lines[0])
	}
	// Last line must be ---
	if lines[len(lines)-1] != "---" {
		t.Fatalf("last line must be ---, got %q", lines[len(lines)-1])
	}
	// id must come before title, which must come before layer
	pos := func(prefix string) int {
		for i, l := range lines {
			if strings.HasPrefix(l, prefix+":") {
				return i
			}
		}
		return -1
	}
	if pos("id") > pos("title") || pos("title") > pos("layer") || pos("layer") > pos("scope") {
		t.Errorf("FRONT_ORDER violated: id=%d title=%d layer=%d scope=%d",
			pos("id"), pos("title"), pos("layer"), pos("scope"))
	}
	// status must come after scope
	if pos("status") < pos("scope") {
		t.Errorf("status should appear after scope in FRONT_ORDER")
	}
}

func TestDumpFront_ListKeysAlwaysInline(t *testing.T) {
	fm := Frontmatter{
		"id":          "rule.deadbeef",
		"layer":       "rule",
		"scope":       "test",
		"created":     "2026-01-01",
		"status":      "active",
		"sensitivity": "internal",
		"tags":        []string{"a", "b"},
		"evidence":    []string{},
	}
	out := DumpFront(fm)
	if !strings.Contains(out, "tags: [a, b]") {
		t.Errorf("tags should be inline list; dump:\n%s", out)
	}
	if !strings.Contains(out, "evidence: []") {
		t.Errorf("evidence should be empty inline list; dump:\n%s", out)
	}
}

func TestDumpFront_NeedsQuote(t *testing.T) {
	fm := Frontmatter{
		"id":          "lesson.11223344",
		"layer":       "lesson",
		"scope":       "test",
		"title":       "needs-quote: edge cases",
		"source":      "agent: oh-my-pi",
		"created":     "2026-01-01",
		"status":      "active",
		"sensitivity": "internal",
		"tags":        []string{},
	}
	out := DumpFront(fm)
	// title has ": " so it must be quoted
	if !strings.Contains(out, `title: "needs-quote: edge cases"`) {
		t.Errorf("title with ': ' must be quoted; dump:\n%s", out)
	}
	// source has ": " so it must be quoted
	if !strings.Contains(out, `source: "agent: oh-my-pi"`) {
		t.Errorf("source with ': ' must be quoted; dump:\n%s", out)
	}
}

func TestDumpFront_ExtraKeysAppended(t *testing.T) {
	fm := Frontmatter{
		"id":           "decision.cafecafe",
		"layer":        "decision",
		"scope":        "test",
		"created":      "2026-01-01",
		"status":       "active",
		"sensitivity":  "public",
		"tags":         []string{},
		"custom_field": "some-value",
	}
	out := DumpFront(fm)
	// custom_field not in FRONT_ORDER → appended after ordered keys
	lines := strings.Split(strings.TrimSpace(out), "\n")
	customIdx := -1
	statusIdx := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "custom_field:") {
			customIdx = i
		}
		if strings.HasPrefix(l, "status:") {
			statusIdx = i
		}
	}
	if customIdx == -1 {
		t.Fatalf("custom_field missing from dump")
	}
	if customIdx < statusIdx {
		t.Errorf("custom_field (idx %d) should appear after status (idx %d)", customIdx, statusIdx)
	}
}

// ---------------------------------------------------------------------------
// Roundtrip (byte-exact)
// ---------------------------------------------------------------------------

func TestRoundtrip_Simple(t *testing.T) {
	roundtrip(t, fixtureSimple)
}

func TestRoundtrip_Tags(t *testing.T) {
	roundtrip(t, fixtureWithTags)
}

func TestRoundtrip_QuotedScalar(t *testing.T) {
	roundtrip(t, fixtureQuotedScalar)
}

func TestRoundtrip_ExtraKeys(t *testing.T) {
	roundtrip(t, fixtureExtraKeys)
}

func TestRoundtrip_EmptyTags(t *testing.T) {
	roundtrip(t, fixtureEmptyTags)
}

// Mutation test: changing a field value must break the original round-trip equality.
func TestRoundtrip_MutationDetected(t *testing.T) {
	fm, body, _ := ParseFront(fixtureSimple)
	fm["scope"] = "mutated-scope"
	out := DumpFront(fm) + "\n" + body
	if out == fixtureSimple {
		t.Error("mutation of scope should produce different output")
	}
	if !strings.Contains(out, "scope: mutated-scope") {
		t.Error("mutated scope not present in output")
	}
}

// Mutation test: changing list key content must change dump.
func TestRoundtrip_ListMutationDetected(t *testing.T) {
	fm, body, _ := ParseFront(fixtureWithTags)
	fm["tags"] = []string{"only-one"}
	out := DumpFront(fm) + "\n" + body
	if out == fixtureWithTags {
		t.Error("mutation of tags should produce different output")
	}
	if !strings.Contains(out, "tags: [only-one]") {
		t.Error("mutated tag not in output")
	}
}

// Mutation test: adding a quoted-requiring value must introduce quotes.
func TestRoundtrip_QuoteMutationDetected(t *testing.T) {
	fm, body, _ := ParseFront(fixtureSimple)
	fm["source"] = "needs: quoting"
	out := DumpFront(fm) + "\n" + body
	if !strings.Contains(out, `source: "needs: quoting"`) {
		t.Errorf("source with ': ' must be double-quoted; got:\n%s", out)
	}
	if out == fixtureSimple {
		t.Error("mutation should produce different output")
	}
}

// ---------------------------------------------------------------------------
// MakeID
// ---------------------------------------------------------------------------

func TestMakeID_Format(t *testing.T) {
	id := MakeID("lesson", "codec-test", "my title", "body content")
	if !strings.HasPrefix(id, "lesson.") {
		t.Errorf("id must start with layer prefix: %q", id)
	}
	parts := strings.SplitN(id, ".", 2)
	if len(parts[1]) != 8 {
		t.Errorf("hex suffix must be 8 chars, got %d: %q", len(parts[1]), parts[1])
	}
	// must be lowercase hex
	for _, ch := range parts[1] {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			t.Errorf("hex suffix contains non-hex char %q in %q", ch, id)
		}
	}
}

func TestMakeID_Deterministic(t *testing.T) {
	a := MakeID("rule", "scope", "title", "body")
	b := MakeID("rule", "scope", "title", "body")
	if a != b {
		t.Errorf("MakeID not deterministic: %q vs %q", a, b)
	}
}

func TestMakeID_LayerChangesID(t *testing.T) {
	a := MakeID("rule", "scope", "title", "body")
	b := MakeID("lesson", "scope", "title", "body")
	if a == b {
		t.Error("different layer must produce different id")
	}
}

func TestMakeID_BodyChangesID(t *testing.T) {
	a := MakeID("lesson", "scope", "title", "body A")
	b := MakeID("lesson", "scope", "title", "body B")
	if a == b {
		t.Error("different body must produce different id")
	}
}

// Cross-verify against known SHA1 value computed from Python reference.
// Python: hashlib.sha1("lesson|codec-test|hello|world".encode("utf-8")).hexdigest()[:8]
// == "ced23a45"
func TestMakeID_KnownVector(t *testing.T) {
	got := MakeID("lesson", "codec-test", "hello", "world")
	const want = "lesson.ced23a45"
	if got != want {
		t.Errorf("MakeID vector: want %q got %q", want, got)
	}
}

// ---------------------------------------------------------------------------
// Slugify
// ---------------------------------------------------------------------------

func TestSlugify_Basic(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Hello World", "hello-world"},
		{"  spaces  ", "spaces"},
		{"multiple   spaces", "multiple-spaces"},
		{"special!@#$chars", "specialchars"},
		{"already-lower", "already-lower"},
		{"UPPERCASE", "uppercase"},
		{"", "entry"},
		{"!!!", "entry"},
		{"a--b", "a-b"},
		{"Thai: สวัสดี", "thai-สวัสดี"},
	}
	for _, c := range cases {
		got := Slugify(c.in)
		if got != c.want {
			t.Errorf("Slugify(%q): want %q got %q", c.in, c.want, got)
		}
	}
}

func TestSlugify_TruncatesAt60Runes(t *testing.T) {
	long := strings.Repeat("a", 70)
	got := Slugify(long)
	if len([]rune(got)) > 60 {
		t.Errorf("slug must be ≤60 runes, got %d", len([]rune(got)))
	}
}

// ---------------------------------------------------------------------------
// Filename / FilenameN
// ---------------------------------------------------------------------------

// Python cmd_add: lesson/handoff/decision carry the created date in the
// filename, every other layer uses the bare slug.
func TestFilename_DatedLayers(t *testing.T) {
	for _, layer := range []string{"lesson", "handoff", "decision"} {
		got := Filename(layer, "2026-11-08", "Parity Runner Synthetic Marker")
		const want = "2026-11-08-parity-runner-synthetic-marker.md"
		if got != want {
			t.Errorf("Filename(%s): want %q got %q", layer, want, got)
		}
	}
}

func TestFilename_PlainLayers(t *testing.T) {
	for _, layer := range []string{"rule", "project", "knowledge"} {
		got := Filename(layer, "2026-11-08", "Memory Index Sqlite")
		const want = "memory-index-sqlite.md"
		if got != want {
			t.Errorf("Filename(%s): want %q got %q", layer, want, got)
		}
	}
}

func TestFilename_EmptyTitleFallsBackToEntry(t *testing.T) {
	if got := Filename("rule", "2026-11-08", "!!!"); got != "entry.md" {
		t.Errorf("Filename with unusable title = %q, want entry.md", got)
	}
}

// Python cmd_add's collision loop: "-2", "-3", … appended to the stem.
func TestFilenameN_CollisionSuffix(t *testing.T) {
	if got := FilenameN("lesson", "2026-11-08", "my title", 0); got != "2026-11-08-my-title.md" {
		t.Errorf("n=0 = %q", got)
	}
	if got := FilenameN("lesson", "2026-11-08", "my title", 2); got != "2026-11-08-my-title-2.md" {
		t.Errorf("n=2 = %q", got)
	}
	if got := FilenameN("rule", "2026-11-08", "my title", 3); got != "my-title-3.md" {
		t.Errorf("n=3 plain layer = %q", got)
	}
}

// ---------------------------------------------------------------------------
// _needs_quote / _fmt_scalar via DumpFront integration
// ---------------------------------------------------------------------------

func TestNeedsQuote_EmptyString(t *testing.T) {
	if !needsQuote("") {
		t.Error("empty string must need quoting")
	}
}

func TestNeedsQuote_ColonSpace(t *testing.T) {
	if !needsQuote("key: value") {
		t.Error("string with ': ' must need quoting")
	}
}

func TestNeedsQuote_TrailingColon(t *testing.T) {
	if !needsQuote("value:") {
		t.Error("string ending with ':' must need quoting")
	}
}

func TestNeedsQuote_HashChar(t *testing.T) {
	if !needsQuote("has#hash") {
		t.Error("string with '#' must need quoting")
	}
}

func TestNeedsQuote_LeadingSpecial(t *testing.T) {
	for _, s := range []string{"#comment", "- item", "* bullet", "& anchor", "! bang", "| pipe", "> gt", "% percent", "@ at", "` backtick", `"quote`, "'squote"} {
		if !needsQuote(s) {
			t.Errorf("string with leading special char must need quoting: %q", s)
		}
	}
}

func TestNeedsQuote_PlainOK(t *testing.T) {
	for _, s := range []string{"hello", "world", "2026-01-01", "active", "internal", "lesson.aabbccdd"} {
		if needsQuote(s) {
			t.Errorf("plain string should not need quoting: %q", s)
		}
	}
}

// Python _fmt_scalar renders a float with str(): a whole number keeps its
// decimal point. Provenance: `python3 -c "print(repr(1.0))"` == "1.0".
func TestDumpFront_FloatKeepsDecimalPoint(t *testing.T) {
	cases := []struct {
		value float64
		want  string
	}{
		{1.0, "decay: 1.0"},
		{0.5, "decay: 0.5"},
		{0.9, "decay: 0.9"},
		{0.30000000000000004, "decay: 0.30000000000000004"},
	}
	for _, c := range cases {
		out := DumpFront(Frontmatter{"decay": c.value})
		if !strings.Contains(out, c.want) {
			t.Errorf("DumpFront(decay=%v) = %q, want it to contain %q", c.value, out, c.want)
		}
	}
}

func TestPyFloatRepr(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{1.0, "1.0"},
		{0.0, "0.0"},
		{0.8, "0.8"},
		{0.25, "0.25"},
		{-1.0, "-1.0"},
		{1e-05, "1e-05"},
	}
	for _, c := range cases {
		if got := pyFloatRepr(c.in); got != c.want {
			t.Errorf("pyFloatRepr(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Python parse_front returns "\n".join(text.splitlines()), so the body never
// carries the file's trailing newline. Provenance: `mem cite` on the Python
// baseline rewrites a record and the file no longer ends with "\n".
func TestParseFront_BodyDropsTrailingNewline(t *testing.T) {
	src := "---\nid: lesson.aabbccdd\n---\n\nbody line\n"
	_, body, err := ParseFront(src)
	if err != nil {
		t.Fatalf("ParseFront: %v", err)
	}
	if want := "\nbody line"; body != want {
		t.Errorf("body = %q, want %q", body, want)
	}

	// A blank line at the end of the body survives (splitlines keeps it).
	_, body, err = ParseFront("---\nid: lesson.aabbccdd\n---\n\nbody\n\n")
	if err != nil {
		t.Fatalf("ParseFront: %v", err)
	}
	if want := "\nbody\n"; body != want {
		t.Errorf("body with a trailing blank line = %q, want %q", body, want)
	}
}
