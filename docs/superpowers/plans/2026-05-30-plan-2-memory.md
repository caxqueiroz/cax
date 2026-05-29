# Plan 2: Memory Subsystem — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a standalone, fully unit-tested `internal/memory` package backed by cgo-free SQLite (`modernc.org/sqlite` + `modernc.org/sqlite/vec` vec0) providing persistent message history, rolling summarization, semantic vector recall, token-usage rollups, DB stats, a pluggable `Embedder`, and schedule persistence.

**Architecture:** A single `Store` wraps a `*sql.DB`. `Open` runs an idempotent schema migration (the vec0 table dimension `N` comes from `embedder.Dim()`) and records/verifies the embedding model+dim in a `meta` table, failing fast on mismatch. History/summarization/semantic/usage/schedule operations are plain methods on `Store`; embedding is delegated to a pluggable `Embedder` (OpenAI HTTP impl + a deterministic in-package `fakeEmbedder` for tests). All memory operations are best-effort at the call site but return errors here so callers can log/degrade.

**Tech Stack:** Go 1.24, `database/sql` with blank-imported `modernc.org/sqlite` (driver name `"sqlite"`) and `modernc.org/sqlite/vec` (registers the `vec0` virtual table); `net/http` for the OpenAI embedder; `net/http/httptest` for embedder tests. No cgo, no network in unit tests.

---

## Research findings (authoritative for this plan)

- **Versions (date 2026-05-30):** `modernc.org/sqlite/vec` is published as part of module `modernc.org/sqlite` at **`v1.51.0`** (published 2026-05-28). Pin `modernc.org/sqlite v1.51.0`; the `vec` subpackage ships in the same module/version.
- **Blank import:** Import both `_ "modernc.org/sqlite"` (registers the `"sqlite"` `database/sql` driver) and `_ "modernc.org/sqlite/vec"` (its `init` auto-registers the sqlite-vec extension so the `vec0` virtual table and `vec_*` SQL functions are available). The pkg.go.dev summary's "no blank import required" wording is misleading — the contract and gorse.io reference both use the blank import; we follow that.
- **vec0 table creation:** `CREATE VIRTUAL TABLE IF NOT EXISTS vec_memories USING vec0(memory_id INTEGER PRIMARY KEY, embedding float[N])` where `N` is substituted with `embedder.Dim()` at migration time (the dimension is fixed at creation).
- **Insert vector:** `INSERT INTO vec_memories(memory_id, embedding) VALUES (?, vec_f32(?))` binding the memory id and a JSON-ish vector string like `'[0.1,0.2,0.3]'`. `vec_f32` parses the bound text into a float32 vector. Using a `?` placeholder (not `fmt.Sprintf`) avoids injection; the gorse.io example uses string formatting but parameter binding into `vec_f32(?)` is equivalent and safer.
- **KNN query:** `SELECT m.text, vec_distance_cosine(v.embedding, vec_f32(?)) AS distance, m.created_at FROM vec_memories v JOIN memories m ON m.id = v.memory_id WHERE m.session_id = ? ORDER BY distance LIMIT ?`. Smaller cosine distance = closer. We use the explicit `vec_distance_cosine` brute-force form (per contract) rather than the `MATCH`/`k=` operator form, so we can also filter by `session_id` via the JOIN.
- **Vector serialization:** convert `[]float32` to a `[..]` string with `strconv.FormatFloat(float64(f), 'f', -1, 32)` joined by commas inside brackets (deterministic, no trailing-zero noise).
- **DB file size for `Stats`:** `os.Stat(dbPath)` on the configured `cfg.DBPath`. For `:memory:` or unstat-able paths, return `0` bytes (don't error). Tests use a temp file (`t.TempDir()`) when they assert non-zero size and `:memory:` otherwise.
- **OpenAI embeddings:** `POST {base}/v1/embeddings` (default base `https://api.openai.com`), header `Authorization: Bearer <key>`, JSON body `{"model": "...", "input": ["a","b"], "encoding_format": "float"}`. Response: `{"data":[{"object":"embedding","index":0,"embedding":[...floats...]}, ...], "model":"...", "usage":{"prompt_tokens":N,"total_tokens":N}}`. `data` order matches `input` order (we sort by `index` defensively).

## Dependency on Plan 1 (`internal/config`)

This package imports `github.com/caxqueiroz/czcli/internal/config` for `config.MemoryConfig` and `config.ScheduleConfig` (see shared contracts). **Assume Plan 1 has landed.** If, and only if, Plan 1 has not landed when implementation starts, create a minimal stub at `internal/config/config.go` containing exactly the `MemoryConfig` and `ScheduleConfig` structs from the contracts (verbatim field names/tags) so this package compiles and tests run standalone — do not add `Load` or other Plan-1 types. Prefer using the real Plan 1 package.

## File Structure

```
internal/memory/
├── embedder.go          # Embedder interface, EstimateTokens, OpenAI embedder, vectorString helper
├── embedder_test.go     # fakeEmbedder (shared test helper), OpenAI embedder httptest tests, EstimateTokens tests
├── store.go             # Store, Open (migrate + dim verify), Close, Stats, schema constants
├── store_test.go        # Open/migrate/dim-mismatch/Stats tests
├── history.go           # AppendMessage, LoadWindow
├── history_test.go
├── summarizer.go        # Summarizer interface, MaybeSummarize
├── summarizer_test.go   # fakeSummarizer + tests
├── semantic.go          # AddMemory, Recall
├── semantic_test.go
├── usage.go             # RecordUsage, UsageRollups, usage types
├── usage_test.go
├── schedules.go         # ListSchedules, UpsertSchedule, SetLastRun
└── schedules_test.go
```

`go.mod` must already declare `module github.com/caxqueiroz/czcli` and `go 1.24` (created by Plan 1). If absent, run `go mod init github.com/caxqueiroz/czcli` and `go mod edit -go=1.24` before Task 1.

---

### Task 1 — Embedder interface, EstimateTokens, fakeEmbedder, vector serialization

Establish the `Embedder` contract, the shared approximate tokenizer, the test fake used by ALL later tasks, and the `[]float32`→`'[..]'` helper used by the semantic layer.

**Files:**
- `internal/memory/embedder.go` (new)
- `internal/memory/embedder_test.go` (new)

- [ ] **Failing test** — write `internal/memory/embedder_test.go`:

```go
package memory

import (
	"context"
	"math"
	"testing"
)

// fakeEmbedder is a deterministic, network-free Embedder used by all memory tests.
// It hashes each input string into a fixed-dim float32 vector via FNV-1a, so equal
// strings always produce identical vectors and different strings differ.
type fakeEmbedder struct {
	dim   int
	model string
}

func newFakeEmbedder(dim int) *fakeEmbedder { return &fakeEmbedder{dim: dim, model: "fake-embed"} }

func (f *fakeEmbedder) Dim() int      { return f.dim }
func (f *fakeEmbedder) Model() string { return f.model }

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = hashVector(t, f.dim)
	}
	return out, nil
}

// hashVector turns a string into a deterministic unit-length vector of length dim.
func hashVector(s string, dim int) []float32 {
	v := make([]float32, dim)
	const prime = uint64(1099511628211)
	h := uint64(1469598103934665603)
	for j := 0; j < dim; j++ {
		for k := 0; k < len(s); k++ {
			h ^= uint64(s[k])
			h *= prime
		}
		h ^= uint64(j + 1)
		h *= prime
		// map to [-1, 1)
		v[j] = float32(int64(h%2000)-1000) / 1000.0
	}
	// normalize to unit length so cosine distance is well-behaved
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	norm := math.Sqrt(sum)
	if norm == 0 {
		v[0] = 1
		return v
	}
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
	return v
}

func TestEstimateTokens(t *testing.T) {
	cases := map[string]int{
		"":     1,        // len 0 -> 0/4 + 1
		"abcd": 2,        // len 4 -> 4/4 + 1
		"abc":  1,        // len 3 -> 3/4 + 1
	}
	for in, want := range cases {
		if got := EstimateTokens(in); got != want {
			t.Fatalf("EstimateTokens(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestVectorString(t *testing.T) {
	got := vectorString([]float32{0.5, -0.25, 0})
	want := "[0.5,-0.25,0]"
	if got != want {
		t.Fatalf("vectorString = %q, want %q", got, want)
	}
}

func TestFakeEmbedderDeterministic(t *testing.T) {
	fe := newFakeEmbedder(8)
	a, err := fe.Embed(context.Background(), []string{"hello", "hello", "world"})
	if err != nil {
		t.Fatal(err)
	}
	if fe.Dim() != 8 || len(a[0]) != 8 {
		t.Fatalf("dim mismatch: Dim=%d len=%d", fe.Dim(), len(a[0]))
	}
	for i := range a[0] {
		if a[0][i] != a[1][i] {
			t.Fatalf("same input produced different vectors at %d", i)
		}
	}
	same := true
	for i := range a[0] {
		if a[0][i] != a[2][i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("different inputs produced identical vectors")
	}
}
```

- [ ] **Run → expect FAIL:** `go test ./internal/memory/ -run 'TestEstimateTokens|TestVectorString|TestFakeEmbedder'` → fails to compile (`EstimateTokens`, `vectorString`, `Embedder` undefined).

- [ ] **Minimal impl** — write `internal/memory/embedder.go`:

```go
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
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

func (e *OpenAIEmbedder) Dim() int      { return e.dim }
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
	defer resp.Body.Close()

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
```

- [ ] **Run → expect PASS:** `go test ./internal/memory/ -run 'TestEstimateTokens|TestVectorString|TestFakeEmbedder'`.

- [ ] **Commit:** `feat(memory): add Embedder interface, EstimateTokens, OpenAI embedder, fake embedder`.

---

### Task 2 — OpenAI embedder via httptest (no network)

Verify the OpenAI embedder builds the correct request and parses the response, using `httptest.Server`.

**Files:**
- `internal/memory/embedder_test.go` (extend)

- [ ] **Failing test** — append to `internal/memory/embedder_test.go`:

```go
import_block_marker_OpenAITest // (remove this line; add the imports below to the existing import block)
```

Add these imports to the existing `import (...)` block at the top of the file: `"encoding/json"`, `"net/http"`, `"net/http/httptest"`, `"reflect"`. Then append:

```go
func TestOpenAIEmbedderRequestAndParse(t *testing.T) {
	var gotAuth, gotPath, gotModel string
	var gotInput []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		var body embeddingsRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		gotModel = body.Model
		gotInput = body.Input
		// respond out of order to exercise index sorting
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data":[
				{"object":"embedding","index":1,"embedding":[0.4,0.5,0.6]},
				{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]}
			],
			"model":"text-embedding-3-small",
			"usage":{"prompt_tokens":4,"total_tokens":4}
		}`))
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder(srv.URL, "text-embedding-3-small", "sk-test", 3)
	vecs, err := e.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if gotPath != "/v1/embeddings" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotModel != "text-embedding-3-small" {
		t.Fatalf("model = %q", gotModel)
	}
	if !reflect.DeepEqual(gotInput, []string{"first", "second"}) {
		t.Fatalf("input = %v", gotInput)
	}
	if !reflect.DeepEqual(vecs[0], []float32{0.1, 0.2, 0.3}) {
		t.Fatalf("vec[0] (index 0) = %v", vecs[0])
	}
	if !reflect.DeepEqual(vecs[1], []float32{0.4, 0.5, 0.6}) {
		t.Fatalf("vec[1] (index 1) = %v", vecs[1])
	}
}

func TestOpenAIEmbedderHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()
	e := NewOpenAIEmbedder(srv.URL, "m", "k", 3)
	if _, err := e.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("expected error on HTTP 429")
	}
}
```

> Note: the `import_block_marker_OpenAITest` line is a directive to the implementer, not code — delete it and instead add the listed imports to the existing import block.

- [ ] **Run → expect FAIL:** `go test ./internal/memory/ -run TestOpenAIEmbedder` (fails first to compile if imports missing, then drive to green).

- [ ] **Minimal impl:** none required beyond Task 1 — the embedder already exists. If the test fails, fix the test imports only. (This task is purely additional coverage.)

- [ ] **Run → expect PASS:** `go test ./internal/memory/ -run TestOpenAIEmbedder`.

- [ ] **Commit:** `test(memory): cover OpenAI embedder request build and response parsing`.

---

### Task 3 — Store: Open, schema migration, dim verification, Close, Stats

Open the DB, run the schema (substituting `N`), persist+verify embed model/dim in `meta`, and expose `Stats`.

**Files:**
- `internal/memory/store.go` (new)
- `internal/memory/store_test.go` (new)

- [ ] **Failing test** — write `internal/memory/store_test.go`:

```go
package memory

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/caxqueiroz/czcli/internal/config"
)

// openTestStore opens an in-memory store with a fake embedder of the given dim.
func openTestStore(t *testing.T, dim int) *Store {
	t.Helper()
	st, err := Open(config.MemoryConfig{DBPath: ":memory:", TokenBudget: 8000, RecallK: 5}, newFakeEmbedder(dim))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestOpenCreatesSchemaAndMeta(t *testing.T) {
	st := openTestStore(t, 8)
	// meta should record model + dim
	var model, dimStr string
	if err := st.db.QueryRow(`SELECT value FROM meta WHERE key='embed_model'`).Scan(&model); err != nil {
		t.Fatalf("read embed_model: %v", err)
	}
	if err := st.db.QueryRow(`SELECT value FROM meta WHERE key='embed_dim'`).Scan(&dimStr); err != nil {
		t.Fatalf("read embed_dim: %v", err)
	}
	if model != "fake-embed" || dimStr != "8" {
		t.Fatalf("meta = %q/%q, want fake-embed/8", model, dimStr)
	}
	// vec0 table must be queryable
	if _, err := st.db.Exec(`INSERT INTO vec_memories(memory_id, embedding) VALUES (1, vec_f32('[` +
		"0,0,0,0,0,0,0,1" + `]'))`); err != nil {
		t.Fatalf("vec0 insert: %v", err)
	}
}

func TestOpenDimMismatchFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mem.db")
	cfg := config.MemoryConfig{DBPath: path, TokenBudget: 8000, RecallK: 5}

	st, err := Open(cfg, newFakeEmbedder(8))
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	_ = st.Close()

	// Reopen with a different dim -> must error.
	if _, err := Open(cfg, newFakeEmbedder(16)); err == nil {
		t.Fatal("expected dim-mismatch error on reopen")
	}
}

func TestStats(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mem.db")
	st, err := Open(config.MemoryConfig{DBPath: path, TokenBudget: 8000, RecallK: 5}, newFakeEmbedder(8))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	if _, err := st.AppendMessage(ctx, Message{SessionID: "s1", Role: RoleUser, Content: "hi", Tokens: 1}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if err := st.AddMemory(ctx, "s1", "remember this", 1); err != nil {
		t.Fatalf("AddMemory: %v", err)
	}
	stats, err := st.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.MessageCount != 1 {
		t.Fatalf("MessageCount = %d, want 1", stats.MessageCount)
	}
	if stats.MemoryCount != 1 {
		t.Fatalf("MemoryCount = %d, want 1", stats.MemoryCount)
	}
	if stats.DBSizeBytes <= 0 {
		t.Fatalf("DBSizeBytes = %d, want > 0 for a file-backed DB", stats.DBSizeBytes)
	}
}
```

> `TestStats` depends on `AppendMessage` (Task 4) and `AddMemory` (Task 6). Implement Task 3's non-Stats tests first; run `TestStats` once Tasks 4 and 6 land. Keep `TestStats` in this file but skip running it until then (it references methods not yet written, so it won't compile in isolation — implement `AppendMessage`/`AddMemory` stubs are NOT allowed; instead, gate the order: do Tasks 4 & 6, then `TestStats` compiles). Simplest path: write `store.go` + the two non-Stats tests now, comment out `TestStats` with a `// TODO(task3): enable after Tasks 4 & 6`, then uncomment and verify at the end of Task 6.

- [ ] **Run → expect FAIL:** `go test ./internal/memory/ -run 'TestOpenCreatesSchema|TestOpenDimMismatch'` → fails to compile (`Open`, `Store`, `Message`, roles undefined).

- [ ] **Minimal impl** — write `internal/memory/store.go`:

```go
package memory

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	_ "modernc.org/sqlite"     // registers the "sqlite" database/sql driver
	_ "modernc.org/sqlite/vec" // registers the vec0 virtual table + vec_* functions

	"github.com/caxqueiroz/czcli/internal/config"
)

// Role is a message author role.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// Message is a single stored conversation message.
type Message struct {
	ID        int64
	SessionID string
	Role      Role
	Content   string
	Tokens    int
	CreatedAt time.Time
}

// Stats is a snapshot of memory storage size.
type Stats struct {
	DBSizeBytes  int64
	MessageCount int
	MemoryCount  int
}

// Store is the SQLite-backed memory store.
type Store struct {
	db       *sql.DB
	embedder Embedder
	dim      int
	dbPath   string
}

// Open opens (creating if needed) the SQLite DB, runs the schema migration with
// N = embedder.Dim(), and records/verifies the embedding model+dim in meta.
func Open(cfg config.MemoryConfig, embedder Embedder) (*Store, error) {
	if embedder == nil {
		return nil, errors.New("memory: embedder is required")
	}
	dim := embedder.Dim()
	if dim <= 0 {
		return nil, fmt.Errorf("memory: embedder dim must be positive, got %d", dim)
	}
	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", cfg.DBPath, err)
	}
	// One connection keeps :memory: DBs coherent and avoids vec0 cross-conn issues.
	db.SetMaxOpenConns(1)

	s := &Store{db: db, embedder: embedder, dim: dim, dbPath: cfg.DBPath}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.verifyMeta(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS messages (
		  id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, role TEXT NOT NULL,
		  content TEXT NOT NULL, token_count INTEGER NOT NULL, created_at TIMESTAMP NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS summaries (
		  id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, summary_text TEXT NOT NULL,
		  covers_up_to_msg_id INTEGER NOT NULL, created_at TIMESTAMP NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS memories (
		  id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, text TEXT NOT NULL,
		  source_msg_id INTEGER, created_at TIMESTAMP NOT NULL)`,
		fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS vec_memories USING vec0(memory_id INTEGER PRIMARY KEY, embedding float[%d])`, s.dim),
		`CREATE TABLE IF NOT EXISTS usage (
		  id INTEGER PRIMARY KEY AUTOINCREMENT, ts TIMESTAMP NOT NULL, provider TEXT, model TEXT,
		  input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, kind TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS schedules (
		  id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT UNIQUE NOT NULL, cron_expr TEXT NOT NULL,
		  prompt TEXT NOT NULL, channel TEXT NOT NULL, enabled INTEGER NOT NULL, last_run TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

// verifyMeta writes embed model+dim on first run, or fails fast if the stored dim
// differs from the configured embedder's dim.
func (s *Store) verifyMeta() error {
	var storedDim string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key='embed_dim'`).Scan(&storedDim)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.Exec(`INSERT INTO meta(key, value) VALUES ('embed_model', ?), ('embed_dim', ?)`,
			s.embedder.Model(), strconv.Itoa(s.dim)); err != nil {
			return fmt.Errorf("write meta: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("read meta: %w", err)
	}
	n, convErr := strconv.Atoi(storedDim)
	if convErr != nil {
		return fmt.Errorf("parse stored embed_dim %q: %w", storedDim, convErr)
	}
	if n != s.dim {
		return fmt.Errorf("memory: embed dim mismatch: stored %d, configured %d (re-embed migration required)", n, s.dim)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close db: %w", err)
	}
	return nil
}

// Stats returns DB file size plus message and memory counts.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&st.MessageCount); err != nil {
		return Stats{}, fmt.Errorf("count messages: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories`).Scan(&st.MemoryCount); err != nil {
		return Stats{}, fmt.Errorf("count memories: %w", err)
	}
	if info, err := os.Stat(s.dbPath); err == nil {
		st.DBSizeBytes = info.Size()
	} // :memory: and unstat-able paths -> 0, not an error
	return st, nil
}
```

> Add `"context"` to the `store.go` import block (used by `Stats`).

- [ ] **Run → expect PASS:** `go test ./internal/memory/ -run 'TestOpenCreatesSchema|TestOpenDimMismatch'`.

- [ ] **Commit:** `feat(memory): add Store with schema migration, dim verification, and Stats`.

---

### Task 4 — History: AppendMessage and LoadWindow

Persist messages and load the working window (latest summary + recent messages within `tokenBudget`, selected newest-first, returned chronological).

**Files:**
- `internal/memory/history.go` (new)
- `internal/memory/history_test.go` (new)

- [ ] **Failing test** — write `internal/memory/history_test.go`:

```go
package memory

import (
	"context"
	"testing"
)

func TestAppendAndLoadWindowChronological(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := context.Background()
	for i, txt := range []string{"one", "two", "three"} {
		role := RoleUser
		if i%2 == 1 {
			role = RoleAssistant
		}
		if _, err := st.AppendMessage(ctx, Message{SessionID: "s1", Role: role, Content: txt, Tokens: EstimateTokens(txt)}); err != nil {
			t.Fatalf("AppendMessage %q: %v", txt, err)
		}
	}
	summary, msgs, err := st.LoadWindow(ctx, "s1", 8000)
	if err != nil {
		t.Fatalf("LoadWindow: %v", err)
	}
	if summary != "" {
		t.Fatalf("summary = %q, want empty", summary)
	}
	if len(msgs) != 3 {
		t.Fatalf("len(msgs) = %d, want 3", len(msgs))
	}
	want := []string{"one", "two", "three"}
	for i, m := range msgs {
		if m.Content != want[i] {
			t.Fatalf("msgs[%d] = %q, want %q (chronological)", i, m.Content, want[i])
		}
	}
}

func TestLoadWindowRespectsBudgetNewestFirst(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := context.Background()
	// Each message is 4 tokens (Tokens set explicitly). Budget 8 fits exactly 2 newest.
	for _, txt := range []string{"aaa", "bbb", "ccc"} {
		if _, err := st.AppendMessage(ctx, Message{SessionID: "s1", Role: RoleUser, Content: txt, Tokens: 4}); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}
	_, msgs, err := st.LoadWindow(ctx, "s1", 8)
	if err != nil {
		t.Fatalf("LoadWindow: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2 (budget 8 / 4 each)", len(msgs))
	}
	// newest-first selection, chronological return -> ["bbb","ccc"]
	if msgs[0].Content != "bbb" || msgs[1].Content != "ccc" {
		t.Fatalf("window = [%q,%q], want [bbb,ccc]", msgs[0].Content, msgs[1].Content)
	}
}

func TestLoadWindowSessionScoped(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := context.Background()
	if _, err := st.AppendMessage(ctx, Message{SessionID: "s1", Role: RoleUser, Content: "mine", Tokens: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessage(ctx, Message{SessionID: "other", Role: RoleUser, Content: "theirs", Tokens: 1}); err != nil {
		t.Fatal(err)
	}
	_, msgs, err := st.LoadWindow(ctx, "s1", 8000)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "mine" {
		t.Fatalf("got %d msgs, want only s1's 'mine'", len(msgs))
	}
}
```

- [ ] **Run → expect FAIL:** `go test ./internal/memory/ -run 'TestAppendAndLoadWindow|TestLoadWindow'` → fails to compile (`AppendMessage`, `LoadWindow` undefined).

- [ ] **Minimal impl** — write `internal/memory/history.go`:

```go
package memory

import (
	"context"
	"fmt"
	"time"
)

// AppendMessage persists a message and returns its new id. CreatedAt defaults to now.
func (s *Store) AppendMessage(ctx context.Context, m Message) (int64, error) {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO messages(session_id, role, content, token_count, created_at) VALUES (?, ?, ?, ?, ?)`,
		m.SessionID, string(m.Role), m.Content, m.Tokens, m.CreatedAt)
	if err != nil {
		return 0, fmt.Errorf("insert message: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("message last insert id: %w", err)
	}
	return id, nil
}

// LoadWindow returns the latest summary text (if any) for the session and the most
// recent messages whose cumulative token_count fits within tokenBudget. Messages are
// selected newest-first to honor the budget, then returned in chronological order.
func (s *Store) LoadWindow(ctx context.Context, sessionID string, tokenBudget int) (string, []Message, error) {
	var summary string
	err := s.db.QueryRowContext(ctx,
		`SELECT summary_text FROM summaries WHERE session_id = ? ORDER BY id DESC LIMIT 1`, sessionID).Scan(&summary)
	if err != nil && err.Error() != "sql: no rows in result set" {
		// errors.Is(sql.ErrNoRows) handled below; keep import-light by string check is fragile,
		// so use the proper check:
	}
	// Proper no-rows handling:
	if err != nil {
		summary = "" // no summary yet; non-fatal
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id, role, content, token_count, created_at
		   FROM messages WHERE session_id = ? ORDER BY id DESC`, sessionID)
	if err != nil {
		return "", nil, fmt.Errorf("query window: %w", err)
	}
	defer rows.Close()

	var selected []Message
	total := 0
	for rows.Next() {
		var m Message
		var role string
		if err := rows.Scan(&m.ID, &m.SessionID, &role, &m.Content, &m.Tokens, &m.CreatedAt); err != nil {
			return "", nil, fmt.Errorf("scan window row: %w", err)
		}
		m.Role = Role(role)
		if total+m.Tokens > tokenBudget && len(selected) > 0 {
			break
		}
		selected = append(selected, m)
		total += m.Tokens
	}
	if err := rows.Err(); err != nil {
		return "", nil, fmt.Errorf("window rows: %w", err)
	}
	// selected is newest-first; reverse to chronological.
	for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
		selected[i], selected[j] = selected[j], selected[i]
	}
	return summary, selected, nil
}
```

> Implementation note for the worker: replace the fragile string-compare no-rows handling above with `errors.Is(err, sql.ErrNoRows)`. Concretely, write the summary lookup as:
> ```go
> var summary string
> err := s.db.QueryRowContext(ctx, `SELECT summary_text FROM summaries WHERE session_id = ? ORDER BY id DESC LIMIT 1`, sessionID).Scan(&summary)
> if err != nil && !errors.Is(err, sql.ErrNoRows) {
>     return "", nil, fmt.Errorf("query summary: %w", err)
> }
> if errors.Is(err, sql.ErrNoRows) { summary = "" }
> ```
> and add `"database/sql"` + `"errors"` to the imports. Use this clean version (do not ship the string-compare placeholder).

- [ ] **Run → expect PASS:** `go test ./internal/memory/ -run 'TestAppendAndLoadWindow|TestLoadWindow'`.

- [ ] **Commit:** `feat(memory): add AppendMessage and budget-aware LoadWindow`.

---

### Task 5 — Summarizer interface and MaybeSummarize

Define the `Summarizer` interface and condense the oldest chunk into a `summaries` row when the window exceeds budget.

**Files:**
- `internal/memory/summarizer.go` (new)
- `internal/memory/summarizer_test.go` (new)

- [ ] **Failing test** — write `internal/memory/summarizer_test.go`:

```go
package memory

import (
	"context"
	"strings"
	"testing"
)

// fakeSummarizer concatenates message contents; records how many msgs it saw.
type fakeSummarizer struct {
	calls int
	saw   int
}

func (f *fakeSummarizer) Summarize(_ context.Context, msgs []Message) (string, error) {
	f.calls++
	f.saw = len(msgs)
	parts := make([]string, len(msgs))
	for i, m := range msgs {
		parts[i] = m.Content
	}
	return "SUMMARY:" + strings.Join(parts, "|"), nil
}

func TestMaybeSummarizeUnderBudgetNoop(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := context.Background()
	if _, err := st.AppendMessage(ctx, Message{SessionID: "s1", Role: RoleUser, Content: "hi", Tokens: 2}); err != nil {
		t.Fatal(err)
	}
	fs := &fakeSummarizer{}
	if err := st.MaybeSummarize(ctx, "s1", fs, 8000); err != nil {
		t.Fatalf("MaybeSummarize: %v", err)
	}
	if fs.calls != 0 {
		t.Fatalf("summarizer called %d times, want 0 (under budget)", fs.calls)
	}
}

func TestMaybeSummarizeOverBudgetWritesSummary(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := context.Background()
	// 4 messages, 4 tokens each = 16 tokens; budget 8 is exceeded.
	for _, txt := range []string{"m1", "m2", "m3", "m4"} {
		if _, err := st.AppendMessage(ctx, Message{SessionID: "s1", Role: RoleUser, Content: txt, Tokens: 4}); err != nil {
			t.Fatal(err)
		}
	}
	fs := &fakeSummarizer{}
	if err := st.MaybeSummarize(ctx, "s1", fs, 8); err != nil {
		t.Fatalf("MaybeSummarize: %v", err)
	}
	if fs.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1", fs.calls)
	}
	// Oldest chunk = messages beyond the newest budget-fitting window.
	// budget 8 / 4 = 2 newest kept; 2 oldest (m1,m2) summarized.
	if fs.saw != 2 {
		t.Fatalf("summarized %d msgs, want 2 oldest", fs.saw)
	}
	// A summary row must exist and the working window must now load it.
	summary, msgs, err := st.LoadWindow(ctx, "s1", 8)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(summary, "SUMMARY:m1|m2") {
		t.Fatalf("summary = %q, want prefix SUMMARY:m1|m2", summary)
	}
	if len(msgs) == 0 {
		t.Fatal("expected recent messages still present in window")
	}
}

func TestMaybeSummarizeIdempotentByCoverage(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := context.Background()
	for _, txt := range []string{"m1", "m2", "m3", "m4"} {
		if _, err := st.AppendMessage(ctx, Message{SessionID: "s1", Role: RoleUser, Content: txt, Tokens: 4}); err != nil {
			t.Fatal(err)
		}
	}
	fs := &fakeSummarizer{}
	if err := st.MaybeSummarize(ctx, "s1", fs, 8); err != nil {
		t.Fatal(err)
	}
	// Second call with no new messages beyond coverage -> no new summary.
	if err := st.MaybeSummarize(ctx, "s1", fs, 8); err != nil {
		t.Fatal(err)
	}
	if fs.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1 (idempotent)", fs.calls)
	}
}
```

- [ ] **Run → expect FAIL:** `go test ./internal/memory/ -run TestMaybeSummarize` → fails to compile (`Summarizer`, `MaybeSummarize` undefined).

- [ ] **Minimal impl** — write `internal/memory/summarizer.go`:

```go
package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Summarizer condenses old messages into a single summary string. Implemented in
// Plan 3 by the agent's model; tests use a fake.
type Summarizer interface {
	Summarize(ctx context.Context, msgs []Message) (string, error)
}

// MaybeSummarize checks whether the session's working window exceeds tokenBudget.
// If so, it summarizes the oldest chunk (messages that fall outside the newest
// budget-fitting window and are not already covered by a prior summary) into a new
// summaries row. Raw messages are retained; the window is conceptually trimmed
// because LoadWindow injects the latest summary at the top of context.
func (s *Store) MaybeSummarize(ctx context.Context, sessionID string, sum Summarizer, tokenBudget int) error {
	// Highest message id already covered by an existing summary (0 if none).
	var coveredUpTo int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(covers_up_to_msg_id), 0) FROM summaries WHERE session_id = ?`, sessionID).Scan(&coveredUpTo)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read summary coverage: %w", err)
	}

	// Load all uncovered messages, newest-first, to compute the budget split.
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id, role, content, token_count, created_at
		   FROM messages WHERE session_id = ? AND id > ? ORDER BY id DESC`, sessionID, coveredUpTo)
	if err != nil {
		return fmt.Errorf("query uncovered messages: %w", err)
	}
	defer rows.Close()

	var newestFirst []Message
	total := 0
	for rows.Next() {
		var m Message
		var role string
		if err := rows.Scan(&m.ID, &m.SessionID, &role, &m.Content, &m.Tokens, &m.CreatedAt); err != nil {
			return fmt.Errorf("scan uncovered: %w", err)
		}
		m.Role = Role(role)
		newestFirst = append(newestFirst, m)
		total += m.Tokens
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("uncovered rows: %w", err)
	}

	if total <= tokenBudget {
		return nil // window fits; nothing to summarize
	}

	// Walk newest-first accumulating up to budget; the remainder (older) is the chunk.
	kept := 0
	acc := 0
	for _, m := range newestFirst {
		if acc+m.Tokens > tokenBudget && kept > 0 {
			break
		}
		acc += m.Tokens
		kept++
	}
	if kept >= len(newestFirst) {
		return nil // everything fit after all
	}
	// Oldest chunk = entries after the kept newest ones; convert to chronological.
	chunkNewestFirst := newestFirst[kept:]
	chunk := make([]Message, len(chunkNewestFirst))
	for i, m := range chunkNewestFirst {
		chunk[len(chunkNewestFirst)-1-i] = m
	}
	if len(chunk) == 0 {
		return nil
	}

	text, err := sum.Summarize(ctx, chunk)
	if err != nil {
		return fmt.Errorf("summarize chunk: %w", err)
	}
	coversUpTo := chunk[len(chunk)-1].ID // highest id in the summarized chunk
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO summaries(session_id, summary_text, covers_up_to_msg_id, created_at) VALUES (?, ?, ?, ?)`,
		sessionID, text, coversUpTo, time.Now().UTC()); err != nil {
		return fmt.Errorf("insert summary: %w", err)
	}
	return nil
}
```

> Design note: `coversUpTo` is the highest message id of the oldest chunk. Subsequent `MaybeSummarize` calls only consider messages with `id > coveredUpTo`, making the operation idempotent and ensuring each chunk is summarized once. `LoadWindow` already returns the latest summary, so the window is conceptually trimmed without deleting raw rows.

- [ ] **Run → expect PASS:** `go test ./internal/memory/ -run TestMaybeSummarize`.

- [ ] **Commit:** `feat(memory): add Summarizer interface and rolling MaybeSummarize`.

---

### Task 6 — Semantic: AddMemory and Recall (vec0 KNN)

Embed text and store it in `memories` + `vec_memories`; recall via `vec_distance_cosine` KNN.

**Files:**
- `internal/memory/semantic.go` (new)
- `internal/memory/semantic_test.go` (new)

- [ ] **Failing test** — write `internal/memory/semantic_test.go`:

```go
package memory

import (
	"context"
	"testing"
)

func TestAddMemoryAndRecallOrdering(t *testing.T) {
	st := openTestStore(t, 16)
	ctx := context.Background()
	texts := []string{
		"the cat sat on the mat",
		"quantum chromodynamics lattice gauge theory",
		"my cat likes to sit on warm laps",
	}
	for i, txt := range texts {
		if err := st.AddMemory(ctx, "s1", txt, int64(i+1)); err != nil {
			t.Fatalf("AddMemory %q: %v", txt, err)
		}
	}
	// Recall with the exact text of one stored memory -> it must be the closest (distance ~0).
	got, err := st.Recall(ctx, "s1", "the cat sat on the mat", 3)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("Recall returned no results")
	}
	if got[0].Text != "the cat sat on the mat" {
		t.Fatalf("closest = %q, want exact match first", got[0].Text)
	}
	// Distances must be non-decreasing (KNN ordering).
	for i := 1; i < len(got); i++ {
		if got[i].Distance < got[i-1].Distance {
			t.Fatalf("distances not sorted: %v then %v", got[i-1].Distance, got[i].Distance)
		}
	}
	// The exact match should be ~0 distance.
	if got[0].Distance > 1e-3 {
		t.Fatalf("exact-match distance = %v, want ~0", got[0].Distance)
	}
}

func TestRecallRespectsK(t *testing.T) {
	st := openTestStore(t, 16)
	ctx := context.Background()
	for i, txt := range []string{"alpha", "beta", "gamma", "delta", "epsilon"} {
		if err := st.AddMemory(ctx, "s1", txt, int64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.Recall(ctx, "s1", "alpha", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (k)", len(got))
	}
}

func TestRecallSessionScoped(t *testing.T) {
	st := openTestStore(t, 16)
	ctx := context.Background()
	if err := st.AddMemory(ctx, "s1", "mine secret", 1); err != nil {
		t.Fatal(err)
	}
	if err := st.AddMemory(ctx, "other", "their secret", 2); err != nil {
		t.Fatal(err)
	}
	got, err := st.Recall(ctx, "s1", "secret", 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		if r.Text == "their secret" {
			t.Fatal("recall leaked another session's memory")
		}
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (only s1)", len(got))
	}
}
```

- [ ] **Run → expect FAIL:** `go test ./internal/memory/ -run 'TestAddMemoryAndRecall|TestRecall'` → fails to compile (`AddMemory`, `Recall`, `Recalled` undefined).

- [ ] **Minimal impl** — write `internal/memory/semantic.go`:

```go
package memory

import (
	"context"
	"fmt"
	"time"
)

// Recalled is a semantic-recall hit.
type Recalled struct {
	Text      string
	Distance  float64
	CreatedAt time.Time
}

// AddMemory embeds text and stores it in memories + vec_memories. sourceMsgID may be 0.
func (s *Store) AddMemory(ctx context.Context, sessionID, text string, sourceMsgID int64) error {
	vecs, err := s.embedder.Embed(ctx, []string{text})
	if err != nil {
		return fmt.Errorf("embed memory: %w", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != s.dim {
		return fmt.Errorf("embed memory: bad vector shape len=%d dim=%d", len(vecs), s.dim)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin memory tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var srcArg any
	if sourceMsgID != 0 {
		srcArg = sourceMsgID
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO memories(session_id, text, source_msg_id, created_at) VALUES (?, ?, ?, ?)`,
		sessionID, text, srcArg, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("insert memory: %w", err)
	}
	memID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("memory last insert id: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO vec_memories(memory_id, embedding) VALUES (?, vec_f32(?))`,
		memID, vectorString(vecs[0])); err != nil {
		return fmt.Errorf("insert vector: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit memory tx: %w", err)
	}
	return nil
}

// Recall embeds query and returns the top-k closest memories for the session,
// ordered by ascending cosine distance.
func (s *Store) Recall(ctx context.Context, sessionID, query string, k int) ([]Recalled, error) {
	if k <= 0 {
		k = 5
	}
	vecs, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != s.dim {
		return nil, fmt.Errorf("embed query: bad vector shape len=%d dim=%d", len(vecs), s.dim)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT m.text, vec_distance_cosine(v.embedding, vec_f32(?)) AS distance, m.created_at
		   FROM vec_memories v
		   JOIN memories m ON m.id = v.memory_id
		  WHERE m.session_id = ?
		  ORDER BY distance
		  LIMIT ?`,
		vectorString(vecs[0]), sessionID, k)
	if err != nil {
		return nil, fmt.Errorf("recall query: %w", err)
	}
	defer rows.Close()

	var out []Recalled
	for rows.Next() {
		var r Recalled
		if err := rows.Scan(&r.Text, &r.Distance, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan recall row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recall rows: %w", err)
	}
	return out, nil
}
```

> vec0 note: the KNN form here filters by `session_id` via a JOIN against `memories`, using the explicit `vec_distance_cosine(...)` brute-force form rather than the `embedding MATCH ? AND k = ?` operator form (the operator form does not support arbitrary `WHERE` filters on joined columns in older vec0 builds). The query vector is bound as a `?` placeholder into `vec_f32(?)`, avoiding string injection.

- [ ] **Run → expect PASS:** `go test ./internal/memory/ -run 'TestAddMemoryAndRecall|TestRecall'`.

- [ ] **Re-enable `TestStats`** in `store_test.go` (uncomment) now that `AppendMessage` and `AddMemory` exist; run `go test ./internal/memory/ -run TestStats` → expect PASS.

- [ ] **Commit:** `feat(memory): add AddMemory and vec0 cosine Recall`.

---

### Task 7 — Usage: RecordUsage and UsageRollups

Record token usage rows and compute 1d/1w/1m rollups.

**Files:**
- `internal/memory/usage.go` (new)
- `internal/memory/usage_test.go` (new)

- [ ] **Failing test** — write `internal/memory/usage_test.go`:

```go
package memory

import (
	"context"
	"testing"
	"time"
)

func TestRecordUsageAndRollups(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := context.Background()
	now := time.Now().UTC()

	// Insert rows at controlled timestamps directly to test the windows precisely.
	insert := func(ts time.Time, in, out int) {
		t.Helper()
		if _, err := st.db.ExecContext(ctx,
			`INSERT INTO usage(ts, provider, model, input_tokens, output_tokens, kind) VALUES (?, 'openai', 'm', ?, ?, 'chat')`,
			ts, in, out); err != nil {
			t.Fatalf("insert usage: %v", err)
		}
	}
	insert(now.Add(-1*time.Hour), 10, 1)    // within day, week, month
	insert(now.Add(-3*24*time.Hour), 20, 2) // within week, month
	insert(now.Add(-10*24*time.Hour), 40, 4) // within month only
	insert(now.Add(-40*24*time.Hour), 80, 8) // outside all windows

	got, err := st.UsageRollups(ctx)
	if err != nil {
		t.Fatalf("UsageRollups: %v", err)
	}
	if got.Day.InputTokens != 10 || got.Day.OutputTokens != 1 {
		t.Fatalf("Day = %+v, want {10,1}", got.Day)
	}
	if got.Week.InputTokens != 30 || got.Week.OutputTokens != 3 {
		t.Fatalf("Week = %+v, want {30,3}", got.Week)
	}
	if got.Month.InputTokens != 70 || got.Month.OutputTokens != 7 {
		t.Fatalf("Month = %+v, want {70,7}", got.Month)
	}
}

func TestRecordUsageWritesRow(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := context.Background()
	if err := st.RecordUsage(ctx, "openai", "gpt", 5, 6, UsageChat); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	got, err := st.UsageRollups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Day.InputTokens != 5 || got.Day.OutputTokens != 6 {
		t.Fatalf("Day = %+v, want {5,6}", got.Day)
	}
}
```

- [ ] **Run → expect FAIL:** `go test ./internal/memory/ -run 'TestRecordUsage'` → fails to compile (`RecordUsage`, `UsageRollups`, `UsageKind`, `UsageRollup`, `UsageChat` undefined).

- [ ] **Minimal impl** — write `internal/memory/usage.go`:

```go
package memory

import (
	"context"
	"fmt"
	"time"
)

// UsageKind classifies a usage row.
type UsageKind string

const (
	UsageChat      UsageKind = "chat"
	UsageEmbedding UsageKind = "embedding"
	UsageSummary   UsageKind = "summary"
	UsageSubagent  UsageKind = "subagent"
)

// UsageTotals are summed input/output token counts.
type UsageTotals struct {
	InputTokens  int
	OutputTokens int
}

// UsageRollup holds 1d/1w/1m totals.
type UsageRollup struct {
	Day   UsageTotals
	Week  UsageTotals
	Month UsageTotals
}

// RecordUsage appends a usage row stamped at now (UTC).
func (s *Store) RecordUsage(ctx context.Context, provider, model string, in, out int, kind UsageKind) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO usage(ts, provider, model, input_tokens, output_tokens, kind) VALUES (?, ?, ?, ?, ?, ?)`,
		time.Now().UTC(), provider, model, in, out, string(kind)); err != nil {
		return fmt.Errorf("insert usage: %w", err)
	}
	return nil
}

// UsageRollups sums input/output tokens over the last 1 day, 7 days, and 30 days.
func (s *Store) UsageRollups(ctx context.Context) (UsageRollup, error) {
	now := time.Now().UTC()
	sumSince := func(since time.Time) (UsageTotals, error) {
		var tot UsageTotals
		err := s.db.QueryRowContext(ctx,
			`SELECT COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0)
			   FROM usage WHERE ts >= ?`, since).Scan(&tot.InputTokens, &tot.OutputTokens)
		if err != nil {
			return UsageTotals{}, fmt.Errorf("sum usage since %s: %w", since, err)
		}
		return tot, nil
	}
	day, err := sumSince(now.Add(-24 * time.Hour))
	if err != nil {
		return UsageRollup{}, err
	}
	week, err := sumSince(now.Add(-7 * 24 * time.Hour))
	if err != nil {
		return UsageRollup{}, err
	}
	month, err := sumSince(now.Add(-30 * 24 * time.Hour))
	if err != nil {
		return UsageRollup{}, err
	}
	return UsageRollup{Day: day, Week: week, Month: month}, nil
}
```

- [ ] **Run → expect PASS:** `go test ./internal/memory/ -run TestRecordUsage`.

- [ ] **Commit:** `feat(memory): add RecordUsage and 1d/1w/1m UsageRollups`.

---

### Task 8 — Schedule persistence: ListSchedules, UpsertSchedule, SetLastRun

Persist scheduler config to the `schedules` table.

**Files:**
- `internal/memory/schedules.go` (new)
- `internal/memory/schedules_test.go` (new)

- [ ] **Failing test** — write `internal/memory/schedules_test.go`:

```go
package memory

import (
	"context"
	"testing"
	"time"

	"github.com/caxqueiroz/czcli/internal/config"
)

func TestUpsertAndListSchedules(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := context.Background()

	sc := config.ScheduleConfig{Name: "daily", Cron: "0 9 * * *", Prompt: "good morning", Channel: "cli", Enabled: true}
	if err := st.UpsertSchedule(ctx, sc); err != nil {
		t.Fatalf("UpsertSchedule: %v", err)
	}
	list, err := st.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len = %d, want 1", len(list))
	}
	got := list[0]
	if got.Name != "daily" || got.Cron != "0 9 * * *" || got.Prompt != "good morning" || got.Channel != "cli" || !got.Enabled {
		t.Fatalf("schedule = %+v", got)
	}

	// Upsert same name updates in place (no duplicate).
	sc.Prompt = "rise and shine"
	sc.Enabled = false
	if err := st.UpsertSchedule(ctx, sc); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	list, err = st.ListSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("after update len = %d, want 1 (upsert, not insert)", len(list))
	}
	if list[0].Prompt != "rise and shine" || list[0].Enabled {
		t.Fatalf("updated schedule = %+v", list[0])
	}
}

func TestSetLastRun(t *testing.T) {
	st := openTestStore(t, 8)
	ctx := context.Background()
	if err := st.UpsertSchedule(ctx, config.ScheduleConfig{Name: "job", Cron: "* * * * *", Prompt: "p", Channel: "cli", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	when := time.Now().UTC().Truncate(time.Second)
	if err := st.SetLastRun(ctx, "job", when); err != nil {
		t.Fatalf("SetLastRun: %v", err)
	}
	var stored time.Time
	if err := st.db.QueryRow(`SELECT last_run FROM schedules WHERE name='job'`).Scan(&stored); err != nil {
		t.Fatalf("read last_run: %v", err)
	}
	if !stored.UTC().Equal(when) {
		t.Fatalf("last_run = %v, want %v", stored.UTC(), when)
	}
}
```

- [ ] **Run → expect FAIL:** `go test ./internal/memory/ -run 'TestUpsertAndList|TestSetLastRun'` → fails to compile (`UpsertSchedule`, `ListSchedules`, `SetLastRun` undefined).

- [ ] **Minimal impl** — write `internal/memory/schedules.go`:

```go
package memory

import (
	"context"
	"fmt"
	"time"

	"github.com/caxqueiroz/czcli/internal/config"
)

// ListSchedules returns all persisted schedules ordered by name.
func (s *Store) ListSchedules(ctx context.Context) ([]config.ScheduleConfig, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, cron_expr, prompt, channel, enabled FROM schedules ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("query schedules: %w", err)
	}
	defer rows.Close()

	var out []config.ScheduleConfig
	for rows.Next() {
		var sc config.ScheduleConfig
		var enabled int
		if err := rows.Scan(&sc.Name, &sc.Cron, &sc.Prompt, &sc.Channel, &enabled); err != nil {
			return nil, fmt.Errorf("scan schedule: %w", err)
		}
		sc.Enabled = enabled != 0
		out = append(out, sc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("schedule rows: %w", err)
	}
	return out, nil
}

// UpsertSchedule inserts a schedule or updates it in place when the name exists.
func (s *Store) UpsertSchedule(ctx context.Context, sc config.ScheduleConfig) error {
	enabled := 0
	if sc.Enabled {
		enabled = 1
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO schedules(name, cron_expr, prompt, channel, enabled)
		      VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		      cron_expr = excluded.cron_expr,
		      prompt    = excluded.prompt,
		      channel   = excluded.channel,
		      enabled   = excluded.enabled`,
		sc.Name, sc.Cron, sc.Prompt, sc.Channel, enabled); err != nil {
		return fmt.Errorf("upsert schedule %q: %w", sc.Name, err)
	}
	return nil
}

// SetLastRun records the last-run timestamp for the named schedule.
func (s *Store) SetLastRun(ctx context.Context, name string, t time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE schedules SET last_run = ? WHERE name = ?`, t.UTC(), name)
	if err != nil {
		return fmt.Errorf("set last_run for %q: %w", name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set last_run rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("set last_run: no schedule named %q", name)
	}
	return nil
}
```

- [ ] **Run → expect PASS:** `go test ./internal/memory/ -run 'TestUpsertAndList|TestSetLastRun'`.

- [ ] **Commit:** `feat(memory): add schedule persistence (List/Upsert/SetLastRun)`.

---

### Task 9 — Full suite, tidy, lint

- [ ] **Run full package tests:** `go test ./internal/memory/...` → expect ALL PASS.
- [ ] **Tidy modules:** `go mod tidy` (pulls `modernc.org/sqlite v1.51.0` and transitive deps). Verify `go.mod` pins `modernc.org/sqlite v1.51.0`.
- [ ] **Vet + lint:** `go vet ./internal/memory/...` and `golangci-lint run ./internal/memory/...` → no errors. Fix any unused-import/`errcheck`/`gofmt` issues.
- [ ] **Race check (optional but recommended):** `go test -race ./internal/memory/...` → PASS.
- [ ] **Commit:** `chore(memory): tidy modules and satisfy linters for memory package`.

---

## Verification checklist (all must hold)

- [ ] All contract method signatures match VERBATIM (`Open`, `Close`, `AppendMessage`, `LoadWindow`, `MaybeSummarize`, `AddMemory`, `Recall`, `RecordUsage`, `UsageRollups`, `Stats`, `ListSchedules`, `UpsertSchedule`, `SetLastRun`, plus `Embedder`/`Summarizer` interfaces and `EstimateTokens`).
- [ ] Contract types used verbatim: `Role`/constants, `Message`, `Recalled`, `UsageKind`/constants, `UsageTotals`, `UsageRollup`, `Stats`, `Embedder`, `Summarizer`, `Store`.
- [ ] Schema matches the contract DDL exactly; vec0 `N` = `embedder.Dim()`.
- [ ] `internal/config` imported (not redefined) for `MemoryConfig`/`ScheduleConfig`.
- [ ] No network in unit tests (OpenAI embedder via `httptest`; recall/summarize via `fakeEmbedder`/`fakeSummarizer`).
- [ ] Tests deterministic; `:memory:` used except where `Stats` size assertion needs a temp file.
