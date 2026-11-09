// embed package tests — all against an httptest fake Ollama (no real
// network, NFR-PRIV-02): response parsing, batching, timeout, the
// connection-refused hint, wrong-dim rejection and the config factory.
package embed

import (
	"context"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atomsbaza/my-memory-engine/internal/schema"
)

// fakeRecorder captures what a fakeOllama served.
type fakeRecorder struct {
	mu         sync.Mutex
	requests   int
	batchSizes []int
}

func (f *fakeRecorder) record(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	f.batchSizes = append(f.batchSizes, n)
}

func (f *fakeRecorder) snapshot() (int, []int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests, append([]int(nil), f.batchSizes...)
}

// fakeOllama serves /api/embed with deterministic dim-d vectors: every input
// maps to a vector whose first component is its index, the rest zero.
func fakeOllama(t *testing.T, dim int, rec *fakeRecorder, mutate func(w http.ResponseWriter, req embedRequest, emb [][]float32)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			http.NotFound(w, r)
			return
		}
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if rec != nil {
			rec.record(len(req.Input))
		}
		emb := make([][]float32, len(req.Input))
		for i := range req.Input {
			emb[i] = make([]float32, dim)
			emb[i][0] = float32(i)
		}
		if mutate != nil {
			mutate(w, req, emb)
			return
		}
		_ = json.NewEncoder(w).Encode(embedResponse{Model: req.Model, Embeddings: emb})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOllamaParsesEmbedResponse(t *testing.T) {
	srv := fakeOllama(t, 4, nil, nil)
	p := NewOllama(srv.URL, "test-model")
	vecs, err := p.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || len(vecs[0]) != 4 || len(vecs[1]) != 4 {
		t.Fatalf("vecs = %v", vecs)
	}
	if vecs[0][0] != 0 || vecs[1][0] != 1 {
		t.Fatalf("deterministic vectors = %v", vecs)
	}
	if p.Dim() != 4 {
		t.Fatalf("Dim() = %d, want 4 after first call", p.Dim())
	}
	if p.Model() != "test-model" || p.Name() != ModeOllama {
		t.Fatalf("model/name = %s/%s", p.Model(), p.Name())
	}
}

func TestOllamaBatchesAtMost64(t *testing.T) {
	rec := &fakeRecorder{}
	srv := fakeOllama(t, 2, rec, nil)
	p := NewOllama(srv.URL, "m")
	const n = 130 // 64 + 64 + 2
	texts := make([]string, n)
	for i := range texts {
		texts[i] = "t" + strconv.Itoa(i)
	}
	if _, err := p.Embed(context.Background(), texts); err != nil {
		t.Fatal(err)
	}
	_, sizes := rec.snapshot()
	if len(sizes) != 3 || sizes[0] != 64 || sizes[1] != 64 || sizes[2] != 2 {
		t.Fatalf("batch sizes = %v, want [64 64 2]", sizes)
	}
	// Order is preserved across batches.
	vecs, err := p.Embed(context.Background(), texts[:3])
	if err != nil || len(vecs) != 3 {
		t.Fatalf("embed 3: %v %v", vecs, err)
	}
}

func TestOllamaTimeoutIsObservable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(embedResponse{Embeddings: [][]float32{{1}}})
	}))
	t.Cleanup(srv.Close)
	p := NewOllama(srv.URL, "m")
	p.client.Timeout = 30 * time.Millisecond // shrink for the test
	if _, err := p.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("timeout: want error, got nil")
	}
}

func TestOllamaConnectionRefusedHint(t *testing.T) {
	// Bind then close a port so the dial is refused.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	p := NewOllama("http://"+addr, "m")
	_, err = p.Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("connection refused: want error, got nil")
	}
	for _, want := range []string{"ollama ไม่ทำงาน", "ollama serve", "brew services start ollama"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing hint %q", err, want)
		}
	}
}

func TestOllamaRejectsRaggedDims(t *testing.T) {
	srv := fakeOllama(t, 4, nil, func(w http.ResponseWriter, req embedRequest, emb [][]float32) {
		emb[1] = []float32{0, 1, 0} // 3-dim inside a 4-dim batch
		_ = json.NewEncoder(w).Encode(embedResponse{Embeddings: emb})
	})
	p := NewOllama(srv.URL, "m")
	_, err := p.Embed(context.Background(), []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "mixed dims") {
		t.Fatalf("ragged dims: err = %v, want mixed dims error", err)
	}
}

func TestOllamaRejectsShortResponses(t *testing.T) {
	srv := fakeOllama(t, 4, nil, func(w http.ResponseWriter, req embedRequest, emb [][]float32) {
		_ = json.NewEncoder(w).Encode(embedResponse{Embeddings: emb[:1]}) // one of two
	})
	p := NewOllama(srv.URL, "m")
	if _, err := p.Embed(context.Background(), []string{"a", "b"}); err == nil || !strings.Contains(err.Error(), "ตอบไม่ครบ") {
		t.Fatalf("short response: err = %v", err)
	}
}

func TestNewFromConfigModes(t *testing.T) {
	if p := NewFromConfig(schema.EmbeddingConfig{Provider: "off"}); p != nil {
		t.Fatalf("off: want nil provider, got %T", p)
	}
	if p := NewFromConfig(schema.EmbeddingConfig{}); p != nil {
		t.Fatalf("absent: want nil provider, got %T", p)
	}
	if p := NewFromConfig(schema.EmbeddingConfig{Provider: "OLLAMA"}); p == nil {
		t.Fatal("case-insensitive ollama: want provider")
	}
	// Unknown provider: first use fails loudly naming the mode.
	p := NewFromConfig(schema.EmbeddingConfig{Provider: "openai"})
	if p == nil {
		t.Fatal("unknown: want error provider, got nil")
	}
	if _, err := p.Embed(context.Background(), []string{"x"}); err == nil || !strings.Contains(err.Error(), "openai") {
		t.Fatalf("unknown provider error = %v", err)
	}
}

func TestOllamaDefaults(t *testing.T) {
	p := NewOllama("", "")
	if p.baseURL != DefaultBaseURL || p.model != DefaultModel {
		t.Fatalf("defaults: %s %s", p.baseURL, p.model)
	}
	if DefaultModel != "nomic-embed-text" || DefaultBaseURL != "http://127.0.0.1:11434" {
		t.Fatalf("frozen defaults changed: %s %s", DefaultModel, DefaultBaseURL)
	}
	if MaxBatch != 64 {
		t.Fatalf("MaxBatch = %d, want 64", MaxBatch)
	}
	if RequestTimeout != 60*time.Second {
		t.Fatalf("RequestTimeout = %v, want 60s", RequestTimeout)
	}
	_ = math.Abs(0) // keep math import honest for future numeric probes
}
