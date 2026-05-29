package config

import (
	"os"
	"path/filepath"
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
