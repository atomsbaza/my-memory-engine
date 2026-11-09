// Package embed implements the optional semantic-embedding layer: a Provider
// interface, the disabled ("off") default, and a local Ollama provider.
//
// The engine is local-only by default (NFR-NET-01): off mode performs no
// network calls and no indexing, keeping retrieval byte-identical to the
// pre-embedding engine. Ollama mode adds one local HTTP round trip per query
// and a batched embed pass at reindex time; nothing ever leaves the machine
// (remote providers are explicitly out of scope).
//
// The binary vector codec (little-endian float32 blobs) and cosine similarity
// live in internal/vec so the index package can use them without gaining a
// network-client dependency.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/atomsbaza/my-memory-engine/internal/schema"
)

// Mode names accepted in schema/taxonomy.json's embedding block.
const (
	ModeOff    = schema.EmbeddingProviderOff
	ModeOllama = schema.EmbeddingProviderOllama

	// DefaultModel is the embedding model used when the config block omits
	// one. nomic-embed-text is a 768-dim local model (274 MB).
	DefaultModel = schema.EmbeddingDefaultModel

	// DefaultBaseURL is Ollama's default local listen address.
	DefaultBaseURL = schema.EmbeddingDefaultBaseURL

	// MaxBatch is the most texts sent to Ollama in one /api/embed request.
	MaxBatch = 64

	// RequestTimeout bounds each embed HTTP request.
	RequestTimeout = 60 * time.Second
)

// Provider produces one embedding vector per input text. Implementations are
// safe for concurrent use. Dim may return 0 when the dimension is not known
// until the first call (the Ollama provider); callers that need a concrete
// dimension read it from the index meta stamp instead.
type Provider interface {
	Name() string
	Model() string
	Dim() int
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Config is the parsed taxonomy.json embedding block. The type lives in the
// schema package (network-free layering); the alias keeps embed call sites
// readable.
type Config = schema.EmbeddingConfig

// NewFromConfig returns the provider the config selects, or nil for off/empty
// (nil = the engine's no-embedding default: no network, no dense stream).
func NewFromConfig(cfg Config) Provider {
	switch strings.TrimSpace(strings.ToLower(cfg.Provider)) {
	case "", ModeOff:
		return nil
	case ModeOllama:
		d := cfg.WithDefaults()
		return NewOllama(d.BaseURL, d.Model)
	default:
		// An unknown provider is a config error, not a silent off — return
		// the error as a provider whose first use fails loudly.
		return errProvider{mode: cfg.Provider}
	}
}

// errProvider surfaces an unsupported provider name at first use instead of
// silently degrading to off.
type errProvider struct{ mode string }

func (e errProvider) Name() string { return e.mode }
func (e errProvider) Model() string {
	return ""
}
func (e errProvider) Dim() int { return 0 }
func (e errProvider) Embed(context.Context, []string) ([][]float32, error) {
	return nil, fmt.Errorf("embed: provider ไม่รู้จัก %q (รองรับ: off, ollama)", e.mode)
}

// ---------------------------------------------------------------------------
// Ollama provider
// ---------------------------------------------------------------------------

// Ollama embeds texts through a local Ollama server's /api/embed endpoint.
type Ollama struct {
	baseURL string
	model   string
	client  *http.Client
	dim     int // 0 until the first response fixes it
}

// NewOllama returns a provider for the Ollama server at baseURL (empty =
// DefaultBaseURL) using model (empty = DefaultModel).
func NewOllama(baseURL, model string) *Ollama {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if model == "" {
		model = DefaultModel
	}
	return &Ollama{
		baseURL: baseURL,
		model:   model,
		client:  &http.Client{Timeout: RequestTimeout},
	}
}

// Name identifies the provider kind.
func (o *Ollama) Name() string { return ModeOllama }

// Model is the Ollama model string every request sends.
func (o *Ollama) Model() string { return o.model }

// Dim returns the vector dimension observed so far (0 before the first call).
func (o *Ollama) Dim() int { return o.dim }

// Ping checks the server is reachable (embed status); it costs one tiny embed
// request and also fixes Dim.
func (o *Ollama) Ping(ctx context.Context) error {
	_, err := o.Embed(ctx, []string{"ping"})
	return err
}

// embedRequest is the /api/embed request body.
type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embedResponse is the /api/embed response body.
type embedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float32 `json:"embeddings"`
}

// Embed sends texts to Ollama in batches of MaxBatch and returns one vector
// per text, in order. A connection failure returns an actionable error; a
// malformed or ragged response is a hard error, never silently truncated.
func (o *Ollama) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += MaxBatch {
		end := start + MaxBatch
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := o.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (o *Ollama) embedBatch(ctx context.Context, batch []string) ([][]float32, error) {
	body, err := json.Marshal(embedRequest{Model: o.model, Input: batch})
	if err != nil {
		return nil, fmt.Errorf("embed: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, o.netError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed: ollama %s: HTTP %d (pull ด้วย `ollama pull %s` แล้วลองใหม่)", o.baseURL, resp.StatusCode, o.model)
	}
	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, fmt.Errorf("embed: decode ollama response: %w", err)
	}
	if len(er.Embeddings) != len(batch) {
		return nil, fmt.Errorf("embed: ollama คืน %d vectors จาก %d inputs — ตอบไม่ครบ", len(er.Embeddings), len(batch))
	}
	for i, v := range er.Embeddings {
		if len(v) == 0 {
			return nil, fmt.Errorf("embed: ollama คืน vector ว่าง (input #%d)", i)
		}
		if o.dim == 0 {
			o.dim = len(v)
		} else if len(v) != o.dim {
			return nil, fmt.Errorf("embed: ollama คืน mixed dims (%d กับ %d) — ตอบไม่สม่ำเสมอ", o.dim, len(v))
		}
	}
	return er.Embeddings, nil
}

// netError turns transport failures into the actionable hint. Connection
// refused means ollama is not running on this machine.
func (o *Ollama) netError(err error) error {
	var opErr *net.OpError
	if errors.As(err, &opErr) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("embed: ollama ไม่ทำงาน — เปิดด้วย `brew services start ollama` หรือ `ollama serve` แล้วลองใหม่ (%s: %v)", o.baseURL, err)
	}
	return fmt.Errorf("embed: ollama %s: %w", o.baseURL, err)
}
