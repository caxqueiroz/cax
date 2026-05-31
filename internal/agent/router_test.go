package agent

import (
	"context"
	"testing"

	"github.com/deepnoodle-ai/dive/llm"

	"github.com/caxqueiroz/cax/internal/config"
)

// stubLLM is a no-op llm.StreamingLLM used to assert what Router.For returns.
type stubLLM struct{ name string }

func (s *stubLLM) Generate(_ context.Context, _ ...llm.Option) (*llm.Response, error) {
	return nil, nil
}
func (s *stubLLM) Stream(_ context.Context, _ ...llm.Option) (llm.StreamIterator, error) {
	return nil, nil
}
func (s *stubLLM) Name() string { return s.name }

func TestRouter_AgentRoleAlwaysReturnsChain(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.ProviderConfig{
			{Name: "p1", Type: "openai", Model: "x", APIKeyEnv: "K"},
		},
	}
	chain := &stubLLM{name: "chain"}
	r, err := NewRouter(cfg, chain)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	if got := r.For(config.ModelRoleAgent); got != chain {
		t.Fatalf("agent role: want chain, got %v", got)
	}
	if got := r.For(""); got != chain {
		t.Fatalf("empty role: want chain, got %v", got)
	}
}

func TestRouter_RoleFallsThroughCheapToAgent(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.ProviderConfig{
			{Name: "p1", Type: "openai", Model: "x", APIKeyEnv: "K"},
		},
		ModelRoles: map[string]string{}, // no roles configured
	}
	chain := &stubLLM{name: "chain"}
	r, _ := NewRouter(cfg, chain)
	if got := r.For(config.ModelRoleSummarizer); got != chain {
		t.Fatalf("unmapped role should fall back to chain, got %v", got)
	}
}

func TestRouter_RoleResolvesToNamedProvider(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.ProviderConfig{
			{Name: "openai-main", Type: "openai", Model: "gpt-5", APIKeyEnv: "K"},
			{Name: "openai-cheap", Type: "openai", Model: "gpt-5-nano", APIKeyEnv: "K"},
		},
		ModelRoles: map[string]string{
			config.ModelRoleSummarizer: "openai-cheap",
		},
	}
	chain := &stubLLM{name: "chain"}
	r, _ := NewRouter(cfg, chain)
	got := r.For(config.ModelRoleSummarizer)
	if got == nil || got == chain {
		t.Fatalf("summarizer role: want a distinct provider, got %v", got)
	}
	// Same role twice → same cached instance.
	if got2 := r.For(config.ModelRoleSummarizer); got2 != got {
		t.Fatalf("summarizer role: not cached (got two distinct instances)")
	}
}

func TestRouter_CheapFallback(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.ProviderConfig{
			{Name: "openai-main", Type: "openai", Model: "gpt-5", APIKeyEnv: "K"},
			{Name: "openai-cheap", Type: "openai", Model: "gpt-5-nano", APIKeyEnv: "K"},
		},
		ModelRoles: map[string]string{
			// Only "cheap" is mapped. Other utility roles should hit it via fallback.
			config.ModelRoleCheap: "openai-cheap",
		},
	}
	chain := &stubLLM{name: "chain"}
	r, _ := NewRouter(cfg, chain)
	cheap := r.For(config.ModelRoleCheap)
	if cheap == nil || cheap == chain {
		t.Fatal("cheap role: want distinct provider")
	}
	// Summarizer not mapped → falls through to cheap.
	if summ := r.For(config.ModelRoleSummarizer); summ != cheap {
		t.Fatal("summarizer should fall through to cheap")
	}
	// Fact extractor likewise.
	if fe := r.For(config.ModelRoleFactExtractor); fe != cheap {
		t.Fatal("fact_extractor should fall through to cheap")
	}
}

func TestConfig_DuplicateProviderNameRejected(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.ProviderConfig{
			{Name: "p", Type: "openai", Model: "a", APIKeyEnv: "K"},
			{Name: "p", Type: "openai", Model: "b", APIKeyEnv: "K"},
		},
		Embeddings: config.EmbeddingsConfig{Provider: "openai", Dim: 1536},
		Memory:     config.MemoryConfig{DBPath: "/tmp/x.db"},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected duplicate-name error")
	}
}

func TestConfig_ModelRolesMustReferenceKnownProvider(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.ProviderConfig{
			{Name: "p1", Type: "openai", Model: "x", APIKeyEnv: "K"},
		},
		ModelRoles: map[string]string{"summarizer": "does-not-exist"},
		Embeddings: config.EmbeddingsConfig{Provider: "openai", Dim: 1536},
		Memory:     config.MemoryConfig{DBPath: "/tmp/x.db"},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected unknown-role-target error")
	}
}
