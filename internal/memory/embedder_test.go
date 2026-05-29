package memory

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
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
		"":     1, // len 0 -> 0/4 + 1
		"abcd": 2, // len 4 -> 4/4 + 1
		"abc":  1, // len 3 -> 3/4 + 1
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()
	e := NewOpenAIEmbedder(srv.URL, "m", "k", 3)
	if _, err := e.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("expected error on HTTP 429")
	}
}
