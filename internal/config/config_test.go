package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	return p
}

func TestLoadAppliesDefaults(t *testing.T) {
	path := writeYAML(t, `
providers:
  - {name: openai, model: gpt-5.4, api_key_env: OPENAI_API_KEY}
embeddings: {provider: openai, model: text-embedding-3-small, dim: 1536, api_key_env: OPENAI_API_KEY}
memory: {db_path: /tmp/czcli/memory.db}
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Memory.TokenBudget != 8000 {
		t.Errorf("TokenBudget default = %d, want 8000", cfg.Memory.TokenBudget)
	}
	if cfg.Memory.RecallK != 5 {
		t.Errorf("RecallK default = %d, want 5", cfg.Memory.RecallK)
	}
	if cfg.Providers[0].MaxTokens != 4096 {
		t.Errorf("provider MaxTokens default = %d, want 4096", cfg.Providers[0].MaxTokens)
	}
	if cfg.Subagents.Dir != ".dive/agents" {
		t.Errorf("Subagents.Dir default = %q, want .dive/agents", cfg.Subagents.Dir)
	}
}

func TestLoadValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "no providers",
			body: "embeddings: {provider: openai, dim: 1536}\nmemory: {db_path: /tmp/x.db}\n",
			want: "at least one provider",
		},
		{
			name: "openai missing api_key_env",
			body: "providers:\n  - {name: openai, model: gpt-5.4}\nembeddings: {provider: openai, dim: 1536}\nmemory: {db_path: /tmp/x.db}\n",
			want: "api_key_env is required",
		},
		{
			name: "bedrock missing base_url",
			body: "providers:\n  - {name: bedrock, model: claude, token_env: T}\nembeddings: {provider: openai, dim: 1536}\nmemory: {db_path: /tmp/x.db}\n",
			want: "base_url is required",
		},
		{
			name: "bedrock missing token_env",
			body: "providers:\n  - {name: bedrock, model: claude, base_url: http://k}\nembeddings: {provider: openai, dim: 1536}\nmemory: {db_path: /tmp/x.db}\n",
			want: "token_env is required",
		},
		{
			name: "unknown provider",
			body: "providers:\n  - {name: cohere, model: c}\nembeddings: {provider: openai, dim: 1536}\nmemory: {db_path: /tmp/x.db}\n",
			want: "unknown name",
		},
		{
			name: "provider missing model",
			body: "providers:\n  - {name: openai, api_key_env: K}\nembeddings: {provider: openai, dim: 1536}\nmemory: {db_path: /tmp/x.db}\n",
			want: "model is required",
		},
		{
			name: "embeddings missing provider",
			body: "providers:\n  - {name: openai, model: g, api_key_env: K}\nembeddings: {dim: 1536}\nmemory: {db_path: /tmp/x.db}\n",
			want: "embeddings.provider is required",
		},
		{
			name: "embeddings bad dim",
			body: "providers:\n  - {name: openai, model: g, api_key_env: K}\nembeddings: {provider: openai, dim: 0}\nmemory: {db_path: /tmp/x.db}\n",
			want: "embeddings.dim must be > 0",
		},
		{
			name: "memory missing db_path",
			body: "providers:\n  - {name: openai, model: g, api_key_env: K}\nembeddings: {provider: openai, dim: 1536}\nmemory: {token_budget: 8000}\n",
			want: "memory.db_path is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeYAML(t, tc.body)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

func TestLoadExpandsHomeInDBPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	path := writeYAML(t, `
providers:
  - {name: openai, model: gpt-5.4, api_key_env: OPENAI_API_KEY}
embeddings: {provider: openai, dim: 1536}
memory: {db_path: ~/.czcli/memory.db}
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(home, ".czcli", "memory.db")
	if cfg.Memory.DBPath != want {
		t.Fatalf("DBPath = %q, want %q", cfg.Memory.DBPath, want)
	}
	if strings.HasPrefix(cfg.Memory.DBPath, "~") {
		t.Fatalf("DBPath still contains ~: %q", cfg.Memory.DBPath)
	}
}

func TestProviderEnvRefResolves(t *testing.T) {
	t.Setenv("CZCLI_TEST_OPENAI_KEY", "sk-test-123")
	path := writeYAML(t, `
providers:
  - {name: openai, model: gpt-5.4, api_key_env: CZCLI_TEST_OPENAI_KEY}
embeddings: {provider: openai, dim: 1536}
memory: {db_path: /tmp/czcli/memory.db}
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := os.Getenv(cfg.Providers[0].APIKeyEnv)
	if got != "sk-test-123" {
		t.Fatalf("resolved env value = %q, want sk-test-123", got)
	}
}
