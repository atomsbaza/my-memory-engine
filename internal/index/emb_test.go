// emb-table tests (semantic-search mode): SetEmb/EmbMeta round trip,
// DenseSearch max-per-record cosine with deterministic 4-dim vectors, ragged
// rejection, DropEmb's provable off state, and Rebuild's emb preservation.
package index

import (
	"strings"
	"testing"

	"github.com/atomsbaza/my-memory-engine/internal/store"
	"github.com/atomsbaza/my-memory-engine/internal/vec"
)

// openFixture opens a rebuilt index over the shared fixture home copy.
func openEmbFixture(t *testing.T) *Index {
	t.Helper()
	home := copyHome(t)
	recs, err := store.Scan(home)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := Open(IndexPath(home))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ix.Close() })
	if err := ix.Rebuild(recs); err != nil {
		t.Fatal(err)
	}
	return ix
}

func TestSetEmbAndMetaRoundTrip(t *testing.T) {
	ix := openEmbFixture(t)
	rows := []EmbRow{
		{ID: "lesson.0a0a0a04", ChunkIdx: 0, Vec: vec.Encode([]float32{1, 0, 0, 0})},
		{ID: "lesson.0a0a0a04", ChunkIdx: 1, Vec: vec.Encode([]float32{0, 1, 0, 0})},
		{ID: "knowledge.0a0a0a01", ChunkIdx: 0, Vec: vec.Encode([]float32{0, 0, 1, 0})},
	}
	if err := ix.SetEmb(rows, "nomic-embed-text", 4); err != nil {
		t.Fatal(err)
	}
	model, dim, count, ok, err := ix.EmbMeta()
	if err != nil || !ok {
		t.Fatalf("EmbMeta: ok=%v err=%v", ok, err)
	}
	if model != "nomic-embed-text" || dim != 4 || count != 3 {
		t.Fatalf("meta = %q %d %d", model, dim, count)
	}
}

func TestSetEmbRejectsRaggedDims(t *testing.T) {
	ix := openEmbFixture(t)
	rows := []EmbRow{
		{ID: "a", ChunkIdx: 0, Vec: vec.Encode([]float32{1, 0, 0, 0})},
		{ID: "b", ChunkIdx: 0, Vec: vec.Encode([]float32{1, 0, 0})}, // 3-dim
	}
	if err := ix.SetEmb(rows, "m", 4); err == nil || !strings.Contains(err.Error(), "dim") {
		t.Fatalf("ragged: err = %v", err)
	}
	if err := ix.SetEmb(rows, "m", 0); err == nil {
		t.Fatal("dim 0: want error")
	}
}

func TestDenseSearchMaxPerRecord(t *testing.T) {
	ix := openEmbFixture(t)
	rows := []EmbRow{
		// r1: chunk 0 orthogonal, chunk 1 aligned → best 1.0 (max wins).
		{ID: "lesson.r1", ChunkIdx: 0, Vec: vec.Encode([]float32{0, 1, 0, 0})},
		{ID: "lesson.r1", ChunkIdx: 1, Vec: vec.Encode([]float32{1, 0, 0, 0})},
		// r2: only orthogonal chunks → 0.
		{ID: "lesson.r2", ChunkIdx: 0, Vec: vec.Encode([]float32{0, 0, 1, 0})},
		// r3: partially aligned → 0.7071.
		{ID: "lesson.r3", ChunkIdx: 0, Vec: vec.Encode([]float32{1, 1, 0, 0})},
	}
	if err := ix.SetEmb(rows, "test-model", 4); err != nil {
		t.Fatal(err)
	}
	got, err := ix.DenseSearch([]float32{1, 0, 0, 0}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "lesson.r1" || got[1] != "lesson.r3" || got[2] != "lesson.r2" {
		t.Fatalf("dense order = %v, want [r1 r3 r2]", got)
	}
	if limited, _ := ix.DenseSearch([]float32{1, 0, 0, 0}, 1); len(limited) != 1 || limited[0] != "lesson.r1" {
		t.Fatalf("dense limit 1 = %v", limited)
	}
}

func TestDenseSearchRejectsWrongDimQuery(t *testing.T) {
	ix := openEmbFixture(t)
	if err := ix.SetEmb([]EmbRow{{ID: "a", ChunkIdx: 0, Vec: vec.Encode([]float32{1, 0, 0, 0})}}, "m", 4); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.DenseSearch([]float32{1, 0, 0}, 10); err == nil || !strings.Contains(err.Error(), "dim") {
		t.Fatalf("wrong-dim query: err = %v", err)
	}
}

func TestRebuildPreservesEmbAndDropEmbClears(t *testing.T) {
	ix := openEmbFixture(t)
	if err := ix.SetEmb([]EmbRow{{ID: "a", ChunkIdx: 0, Vec: vec.Encode([]float32{1, 0, 0, 0})}}, "keep-model", 4); err != nil {
		t.Fatal(err)
	}
	recs, err := store.Scan(copyHome(t))
	if err != nil {
		t.Fatal(err)
	}
	// A lexical rebuild (the stop-hook path) must not discard embeddings.
	if err := ix.Rebuild(recs); err != nil {
		t.Fatal(err)
	}
	model, dim, _, ok, err := ix.EmbMeta()
	if err != nil || !ok || model != "keep-model" || dim != 4 {
		t.Fatalf("after rebuild: ok=%v model=%q dim=%d err=%v", ok, model, dim, err)
	}
	// DropEmb is the provable off state: no table, no stamps.
	if err := ix.DropEmb(); err != nil {
		t.Fatal(err)
	}
	if err := ix.DropEmb(); err != nil {
		t.Fatalf("DropEmb must be idempotent: %v", err)
	}
	_, _, _, ok, err = ix.EmbMeta()
	if err != nil || ok {
		t.Fatalf("after DropEmb: ok=%v err=%v", ok, err)
	}
	var n int
	if err := ix.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='emb'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("emb table still present: n=%d err=%v", n, err)
	}
}

// copyHome is the writable fixture used by the emb tests (mirror of the
// index_test helper over the same tree).
func copyHome(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	copyTree(t, fixtureHome, dst)
	return dst
}
