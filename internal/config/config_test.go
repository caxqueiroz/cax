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
	// Subagents defaults are covered by TestLoadAppliesSubagentsAndCommandsDefaults.
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
memory: {db_path: ~/.cax/memory.db}
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(home, ".cax", "memory.db")
	if cfg.Memory.DBPath != want {
		t.Fatalf("DBPath = %q, want %q", cfg.Memory.DBPath, want)
	}
	if strings.HasPrefix(cfg.Memory.DBPath, "~") {
		t.Fatalf("DBPath still contains ~: %q", cfg.Memory.DBPath)
	}
}

func TestProviderEnvRefResolves(t *testing.T) {
	t.Setenv("CAX_TEST_OPENAI_KEY", "sk-test-123")
	path := writeYAML(t, `
providers:
  - {name: openai, model: gpt-5.4, api_key_env: CAX_TEST_OPENAI_KEY}
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

func TestLoadAppliesSkillsDefaults(t *testing.T) {
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
	if !cfg.Skills.Enabled {
		t.Errorf("Skills.Enabled default = false, want true")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	want := []string{filepath.Join(home, ".cax/skills"), ".cax/skills"}
	if len(cfg.Skills.Dirs) != len(want) {
		t.Fatalf("Skills.Dirs len = %d, want %d (%v)", len(cfg.Skills.Dirs), len(want), cfg.Skills.Dirs)
	}
	for i, d := range want {
		if cfg.Skills.Dirs[i] != d {
			t.Errorf("Skills.Dirs[%d] = %q, want %q", i, cfg.Skills.Dirs[i], d)
		}
	}
}

func TestLoadExampleConfig(t *testing.T) {
	cfg, err := Load("example.yaml")
	if err != nil {
		t.Fatalf("Load(example.yaml): %v", err)
	}
	if len(cfg.Providers) != 2 {
		t.Fatalf("providers = %d, want 2 (openai enabled, bedrock disabled)", len(cfg.Providers))
	}
	if cfg.Providers[0].Name != "openai" || !cfg.Providers[0].IsEnabled() {
		t.Fatalf("providers[0] = %+v, want enabled openai", cfg.Providers[0])
	}
	if cfg.Providers[1].Name != "bedrock" || cfg.Providers[1].IsEnabled() {
		t.Fatalf("providers[1] = %+v, want disabled bedrock", cfg.Providers[1])
	}
	if cfg.Memory.TokenBudget != 8000 || cfg.Memory.RecallK != 5 {
		t.Fatalf("memory defaults wrong: %+v", cfg.Memory)
	}
}

func TestLoadPluginsDefaultsAndTildeExpansion(t *testing.T) {
	path := writeYAML(t, `
providers:
  - {name: openai, model: gpt-5.4, api_key_env: OPENAI_API_KEY}
embeddings: {provider: openai, model: text-embedding-3-small, dim: 1536, api_key_env: OPENAI_API_KEY}
memory: {db_path: /tmp/x.db}
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Plugins.Enabled {
		t.Fatalf("Plugins.Enabled default = false, want true")
	}
	if len(cfg.Plugins.Dirs) != 2 {
		t.Fatalf("Plugins.Dirs len = %d, want 2 (%v)", len(cfg.Plugins.Dirs), cfg.Plugins.Dirs)
	}
	home, _ := os.UserHomeDir()
	wantUser := filepath.Join(home, ".cax", "plugins")
	if cfg.Plugins.Dirs[0] != wantUser {
		t.Errorf("Plugins.Dirs[0] = %q, want %q", cfg.Plugins.Dirs[0], wantUser)
	}
	if !strings.HasSuffix(cfg.Plugins.Dirs[1], ".cax/plugins") {
		t.Errorf("Plugins.Dirs[1] = %q, want suffix .cax/plugins", cfg.Plugins.Dirs[1])
	}
}

func TestLoadPluginsOverride(t *testing.T) {
	path := writeYAML(t, `
providers:
  - {name: openai, model: gpt-5.4, api_key_env: OPENAI_API_KEY}
embeddings: {provider: openai, model: text-embedding-3-small, dim: 1536, api_key_env: OPENAI_API_KEY}
memory: {db_path: /tmp/x.db}
plugins:
  enabled: false
  dirs: [~/my-plugins, ./pkg/plugins]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Plugins.Enabled {
		t.Errorf("Plugins.Enabled = true, want false (override)")
	}
	if len(cfg.Plugins.Dirs) != 2 {
		t.Fatalf("Plugins.Dirs len = %d, want 2", len(cfg.Plugins.Dirs))
	}
	home, _ := os.UserHomeDir()
	if cfg.Plugins.Dirs[0] != filepath.Join(home, "my-plugins") {
		t.Errorf("Plugins.Dirs[0] = %q, want home-expanded", cfg.Plugins.Dirs[0])
	}
	if cfg.Plugins.Dirs[1] != "./pkg/plugins" {
		t.Errorf("Plugins.Dirs[1] = %q, want unchanged", cfg.Plugins.Dirs[1])
	}
}

func TestConfigCLIThemeField(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	yaml := `persona: x
providers:
  - name: openai
    model: gpt-4o-mini
    api_key_env: OPENAI_API_KEY
embeddings:
  provider: openai
  model: text-embedding-3-small
  dim: 1536
  api_key_env: OPENAI_API_KEY
memory:
  db_path: /tmp/czcli.db
cli:
  theme: dracula
`
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CLI.Theme != "dracula" {
		t.Fatalf("CLI.Theme = %q", cfg.CLI.Theme)
	}
}

func TestConfigCLIThemeDefaults(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	yaml := `persona: x
providers:
  - name: openai
    model: gpt-4o-mini
    api_key_env: OPENAI_API_KEY
embeddings:
  provider: openai
  model: text-embedding-3-small
  dim: 1536
  api_key_env: OPENAI_API_KEY
memory:
  db_path: /tmp/czcli.db
`
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CLI.Theme != "" {
		t.Fatalf("CLI.Theme default = %q want empty", cfg.CLI.Theme)
	}
}

func TestLoadAppliesSubagentsAndCommandsDefaults(t *testing.T) {
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
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	wantSub := []string{filepath.Join(home, ".cax/agents"), ".cax/agents"}
	if len(cfg.Subagents.Dirs) != len(wantSub) {
		t.Fatalf("Subagents.Dirs = %v, want %v", cfg.Subagents.Dirs, wantSub)
	}
	for i, d := range wantSub {
		if cfg.Subagents.Dirs[i] != d {
			t.Errorf("Subagents.Dirs[%d] = %q, want %q", i, cfg.Subagents.Dirs[i], d)
		}
	}
	wantCmd := []string{filepath.Join(home, ".cax/commands"), ".cax/commands"}
	if len(cfg.Commands.Dirs) != len(wantCmd) {
		t.Fatalf("Commands.Dirs = %v, want %v", cfg.Commands.Dirs, wantCmd)
	}
	for i, d := range wantCmd {
		if cfg.Commands.Dirs[i] != d {
			t.Errorf("Commands.Dirs[%d] = %q, want %q", i, cfg.Commands.Dirs[i], d)
		}
	}
	if !cfg.Commands.Enabled {
		t.Errorf("Commands.Enabled default = false, want true")
	}
	if !cfg.Subagents.Enabled {
		t.Errorf("Subagents.Enabled default = false, want true")
	}
}

func TestLoadMigratesLegacySubagentsDir(t *testing.T) {
	path := writeYAML(t, `
providers:
  - {name: openai, model: gpt-5.4, api_key_env: OPENAI_API_KEY}
embeddings: {provider: openai, model: text-embedding-3-small, dim: 1536, api_key_env: OPENAI_API_KEY}
memory: {db_path: /tmp/x.db}
subagents:
  enabled: true
  dir: ./legacy/agents
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Subagents.Dirs) != 1 || cfg.Subagents.Dirs[0] != "./legacy/agents" {
		t.Fatalf("Subagents.Dirs = %v, want [./legacy/agents] (migrated from singular dir)", cfg.Subagents.Dirs)
	}
}

func TestLoadDirsWinsOverLegacyDir(t *testing.T) {
	path := writeYAML(t, `
providers:
  - {name: openai, model: gpt-5.4, api_key_env: OPENAI_API_KEY}
embeddings: {provider: openai, model: text-embedding-3-small, dim: 1536, api_key_env: OPENAI_API_KEY}
memory: {db_path: /tmp/x.db}
subagents:
  enabled: true
  dir: ./legacy/agents
  dirs: [./modern/agents]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Subagents.Dirs) != 1 || cfg.Subagents.Dirs[0] != "./modern/agents" {
		t.Fatalf("Subagents.Dirs = %v, want [./modern/agents] (Dirs wins)", cfg.Subagents.Dirs)
	}
}

func TestLoadSubagentsAndCommandsTildeExpansion(t *testing.T) {
	path := writeYAML(t, `
providers:
  - {name: openai, model: gpt-5.4, api_key_env: OPENAI_API_KEY}
embeddings: {provider: openai, model: text-embedding-3-small, dim: 1536, api_key_env: OPENAI_API_KEY}
memory: {db_path: /tmp/x.db}
subagents:
  dirs: [~/agents-home, ./agents-local]
commands:
  dirs: [~/cmds-home, ./cmds-local]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	home, _ := os.UserHomeDir()
	if cfg.Subagents.Dirs[0] != filepath.Join(home, "agents-home") || cfg.Subagents.Dirs[1] != "./agents-local" {
		t.Errorf("Subagents.Dirs = %v, want [%s, ./agents-local]", cfg.Subagents.Dirs, filepath.Join(home, "agents-home"))
	}
	if cfg.Commands.Dirs[0] != filepath.Join(home, "cmds-home") || cfg.Commands.Dirs[1] != "./cmds-local" {
		t.Errorf("Commands.Dirs = %v, want [%s, ./cmds-local]", cfg.Commands.Dirs, filepath.Join(home, "cmds-home"))
	}
}

func TestLoadLSPSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
providers:
  - {name: openai, model: gpt-x, api_key_env: K}
embeddings: {provider: openai, model: e, dim: 8, api_key_env: K}
memory: {db_path: ` + filepath.Join(dir, "m.db") + `}
lsp:
  enabled: true
  servers:
    - name: gopls
      command: gopls
      args: []
      languages: [go]
      root_patterns: [go.mod]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.LSP.Enabled {
		t.Error("LSP.Enabled = false; want true")
	}
	if len(cfg.LSP.Servers) != 1 || cfg.LSP.Servers[0].Name != "gopls" {
		t.Fatalf("LSP.Servers = %+v", cfg.LSP.Servers)
	}
	if len(cfg.LSP.Servers[0].Languages) == 0 || cfg.LSP.Servers[0].Languages[0] != "go" {
		t.Fatalf("Languages = %+v", cfg.LSP.Servers[0].Languages)
	}
}
