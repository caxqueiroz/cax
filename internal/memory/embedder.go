package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/caxqueiroz/cax/internal/config"
)

// Embedder turns text into fixed-dimension vectors. Implementations are pluggable.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dim() int
	Model() string
}

// EstimateTokens is the shared approximate tokenizer (len/4 + 1). Good enough for
// budgeting/usage display; swap for a real tokenizer later if needed.
func EstimateTokens(s string) int {
	return len(s)/4 + 1
}

// vectorString serializes a float32 vector into the '[..]' form vec_f32 parses.
func vectorString(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// NewEmbedder builds an Embedder from config. It is the single constructor other
// packages (e.g. cmd/cax/main.go) call. The API key is read from the configured
// environment variable (APIKeyEnv, falling back to TokenEnv for parity with the
// provider config). Only the "openai" provider is supported in the MVP; "bedrock"
// embeddings are a documented post-MVP follow-up.
func NewEmbedder(cfg config.EmbeddingsConfig) (Embedder, error) {
	if cfg.Dim <= 0 {
		return nil, fmt.Errorf("memory: embeddings dim must be positive, got %d", cfg.Dim)
	}
	switch cfg.Provider {
	case "openai":
		if cfg.Model == "" {
			return nil, fmt.Errorf("memory: embeddings model is required for openai")
		}
		key := apiKeyFromEnv(cfg.APIKeyEnv, cfg.TokenEnv)
		return NewOpenAIEmbedder(cfg.BaseURL, cfg.Model, key, cfg.Dim), nil
	case "bedrock":
		return nil, fmt.Errorf("memory: bedrock embeddings not yet supported")
	default:
		return nil, fmt.Errorf("memory: unknown embeddings provider %q (want openai)", cfg.Provider)
	}
}

// apiKeyFromEnv resolves the API key, preferring APIKeyEnv then TokenEnv.
func apiKeyFromEnv(apiKeyEnv, tokenEnv string) string {
	if apiKeyEnv != "" {
		if v := os.Getenv(apiKeyEnv); v != "" {
			return v
		}
	}
	if tokenEnv != "" {
		return os.Getenv(tokenEnv)
	}
	return ""
}

// OpenAIEmbedder calls the OpenAI /v1/embeddings endpoint.
type OpenAIEmbedder struct {
	baseURL string
	model   string
	dim     int
	apiKey  string
	client  *http.Client
}

// NewOpenAIEmbedder builds an embedder. baseURL defaults to https://api.openai.com.
func NewOpenAIEmbedder(baseURL, model, apiKey string, dim int) *OpenAIEmbedder {
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	return &OpenAIEmbedder{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		dim:     dim,
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Dim returns the embedding dimension.
func (e *OpenAIEmbedder) Dim() int { return e.dim }

// Model returns the embedding model name.
func (e *OpenAIEmbedder) Model() string { return e.model }

type embeddingsRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	EncodingFormat string   `json:"encoding_format"`
}

type embeddingsResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// Embed calls the OpenAI embeddings endpoint and returns one vector per input,
// in input order.
func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(embeddingsRequest{Model: e.model, Input: texts, EncodingFormat: "float"})
	if err != nil {
		return nil, fmt.Errorf("marshal embeddings request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new embeddings request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do embeddings request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		return nil, fmt.Errorf("embeddings http %d: %s", resp.StatusCode, buf.String())
	}

	var parsed embeddingsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode embeddings response: %w", err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings count mismatch: got %d want %d", len(parsed.Data), len(texts))
	}
	// Defensive: sort by index so order matches input.
	sort.Slice(parsed.Data, func(i, j int) bool { return parsed.Data[i].Index < parsed.Data[j].Index })
	out := make([][]float32, len(parsed.Data))
	for i, d := range parsed.Data {
		if len(d.Embedding) != e.dim {
			return nil, fmt.Errorf("embedding dim mismatch: got %d want %d", len(d.Embedding), e.dim)
		}
		out[i] = d.Embedding
	}
	return out, nil
}
