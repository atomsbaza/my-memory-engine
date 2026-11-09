// `mem embed` tests: config round-trip with backup, off-mode output identity
// (absent block vs explicit off must be byte-identical), reindex's provable
// off state, and the dense-seam mismatch guard. No real network: the seam
// tests use synthetic index state only.
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atomsbaza/my-memory-engine/internal/index"
	"github.com/atomsbaza/my-memory-engine/internal/schema"
)

func TestEmbedSetOllamaRoundTripAndBackup(t *testing.T) {
	home := bareHome(t)
	tax := filepath.Join(home, "schema", "taxonomy.json")
	orig, err := os.ReadFile(tax)
	if err != nil {
		t.Fatal(err)
	}

	code, out, stderr := runHome(t, home, "", "embed", "set", "ollama")
	mustCode(t, ExitOK, code, out, stderr, "embed set ollama")
	if !strings.Contains(out, "provider") || !strings.Contains(out, "nomic-embed-text") {
		t.Fatalf("set ollama output = %s", out)
	}

	// Block written and parseable with defaults.
	sch, err := schemaOfForTest(t, home)
	if err != nil {
		t.Fatal(err)
	}
	if sch.Embedding.Provider != "ollama" || sch.Embedding.Model != "nomic-embed-text" ||
		sch.Embedding.BaseURL != "http://127.0.0.1:11434" {
		t.Fatalf("Embedding = %+v", sch.Embedding)
	}
	if !sch.Embedding.Enabled() {
		t.Fatal("ollama config must be Enabled")
	}

	// Exactly one backup exists and holds the original bytes.
	matches, _ := filepath.Glob(tax + ".bak-*")
	if len(matches) != 1 {
		t.Fatalf("backups = %v, want 1", matches)
	}
	bak, _ := os.ReadFile(matches[0])
	if string(bak) != string(orig) {
		t.Fatal("backup must hold the pre-edit bytes")
	}

	// Other members keep their original bytes and order.
	var before, after map[string]json.RawMessage
	if err := json.Unmarshal(orig, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(readFileForTest(t, tax), &after); err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after)-1 {
		t.Fatalf("member count %d -> %d, want +1 (embedding only)", len(before), len(after))
	}
	for k, v := range before {
		if string(v) != string(after[k]) {
			t.Fatalf("member %q changed:\n%s\n%s", k, v, after[k])
		}
	}

	// set off removes the block and restores the original bytes.
	code, out, stderr = runHome(t, home, "", "embed", "set", "off")
	mustCode(t, ExitOK, code, out, stderr, "embed set off")
	if got := readFileForTest(t, tax); string(got) != string(orig) {
		t.Fatalf("set off must restore the original file:\n%s", got)
	}
	sch, err = schemaOfForTest(t, home)
	if err != nil {
		t.Fatal(err)
	}
	if sch.Embedding.Enabled() || sch.Embedding.Provider != "off" {
		t.Fatalf("after set off: %+v", sch.Embedding)
	}
}

func TestEmbedSetCustomModelAndURL(t *testing.T) {
	home := bareHome(t)
	code, out, stderr := runHome(t, home, "", "embed", "set", "ollama", "--model", "m2", "--base-url", "http://localhost:11500")
	mustCode(t, ExitOK, code, out, stderr, "embed set custom")
	sch, err := schemaOfForTest(t, home)
	if err != nil {
		t.Fatal(err)
	}
	if sch.Embedding.Model != "m2" || sch.Embedding.BaseURL != "http://localhost:11500" {
		t.Fatalf("custom = %+v", sch.Embedding)
	}

	if code, _, _ := runHome(t, home, "", "embed", "set", "bogus"); code != ExitGuard {
		t.Fatalf("bogus mode exit = %d, want 2", code)
	}
}

func TestEmbedStatusOffAndJSON(t *testing.T) {
	home := bareHome(t)
	code, out, stderr := runHome(t, home, "", "embed", "status")
	mustCode(t, ExitOK, code, out, stderr, "embed status")
	if !strings.Contains(out, "provider: off") || strings.Contains(out, "ollama:") {
		t.Fatalf("off status must not probe network:\n%s", out)
	}
	code, out, stderr = runHome(t, home, "", "embed", "status", "--json")
	mustCode(t, ExitOK, code, out, stderr, "embed status json")
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("status json: %v\n%s", err, out)
	}
	if doc["provider"] != "off" {
		t.Fatalf("status json provider = %v", doc["provider"])
	}
	if _, probed := doc["ollama_reachable"]; probed {
		t.Fatal("off mode must not report a reachability probe")
	}
}

func TestReindexOffModeProvablyDropsEmb(t *testing.T) {
	home := bareHome(t)
	// Build an emb table by hand, then reindex in off mode.
	code, _, stderr := runHome(t, home, "", "reindex")
	mustCode(t, ExitOK, code, "", stderr, "reindex")
	ix := openIndexForTest(t, home)
	if err := ix.SetEmb(nil, "stale-model", 4); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok, err := ix.EmbMeta(); err != nil || !ok {
		t.Fatalf("precondition: emb stamped (ok=%v err=%v)", ok, err)
	}
	_ = ix.Close()

	code, out, stderr := runHome(t, home, "", "reindex")
	mustCode(t, ExitOK, code, out, stderr, "reindex off")
	if !strings.Contains(out, "embedding: off") {
		t.Fatalf("reindex output = %s", out)
	}
	ix2 := openIndexForTest(t, home)
	defer ix2.Close()
	ix = ix2
	if _, _, _, ok, err := ix.EmbMeta(); err != nil || ok {
		t.Fatalf("off-mode index must carry no emb: ok=%v err=%v", ok, err)
	}
}

// TestOffModeIdentity is the spec gate: outputs are byte-identical when the
// embedding block is absent vs explicitly off, for search --scores and the
// user-prompt hook recall.
func TestOffModeIdentity(t *testing.T) {
	absent := bareHome(t)
	explicit := bareHome(t)
	writeFile(t, explicit, "schema/taxonomy.json", strings.Replace(
		string(readFileForTest(t, filepath.Join(absent, "schema", "taxonomy.json"))),
		"{\n", "{\n  \"embedding\": {\"provider\": \"off\"},\n", 1))

	for _, argv := range [][]string{
		{"search", "cache verify index", "--scores"},
		{"search", "การตรวจ cache", "--scores", "--limit", "5"},
		{"hook", "--event", "user-prompt", "--query", "verify cache index latency", "--format", "text"},
	} {
		c1, o1, e1 := runHome(t, absent, "", argv...)
		c2, o2, e2 := runHome(t, explicit, "", argv...)
		if c1 != c2 || o1 != o2 || e1 != e2 {
			t.Fatalf("off-mode identity broken for %v:\n--- absent (code %d)\n%s%s\n--- explicit off (code %d)\n%s%s",
				argv, c1, o1, e1, c2, o2, e2)
		}
	}
}

// helpers ------------------------------------------------------------

func readFileForTest(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func schemaOfForTest(t *testing.T, home string) (schema.Schema, error) {
	t.Helper()
	return schema.LoadSchema(home)
}

func openIndexForTest(t *testing.T, home string) *index.Index {
	t.Helper()
	ix, err := index.Open(index.IndexPath(home))
	if err != nil {
		t.Fatal(err)
	}
	return ix
}
