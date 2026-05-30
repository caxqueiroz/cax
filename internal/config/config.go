// Package config loads and validates czcli's YAML configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the top-level czcli configuration.
type Config struct {
	Persona    string           `yaml:"persona"`
	Providers  []ProviderConfig `yaml:"providers"`
	Embeddings EmbeddingsConfig `yaml:"embeddings"`
	Memory     MemoryConfig     `yaml:"memory"`
	Tools      ToolsConfig      `yaml:"tools"`
	Subagents  SubagentsConfig  `yaml:"subagents"`
	MCP        MCPConfig        `yaml:"mcp"`
	Skills     SkillsConfig     `yaml:"skills"`
	Plugins    PluginsConfig    `yaml:"plugins"`
	LSP        LSPConfig        `yaml:"lsp"`
	Schedules  []ScheduleConfig `yaml:"schedules"`
}

// ProviderConfig configures one LLM provider in fallback order.
type ProviderConfig struct {
	Name      string `yaml:"name"` // "bedrock" | "openai"
	Model     string `yaml:"model"`
	BaseURL   string `yaml:"base_url"`    // bedrock: KrakenD endpoint
	TokenEnv  string `yaml:"token_env"`   // bedrock: env var holding the x-api-key value
	APIKeyEnv string `yaml:"api_key_env"` // openai: env var holding the API key
	MaxTokens int    `yaml:"max_tokens"`  // default 4096 if 0
}

// EmbeddingsConfig configures the embedding model.
type EmbeddingsConfig struct {
	Provider  string `yaml:"provider"` // "openai" | "bedrock"
	Model     string `yaml:"model"`
	Dim       int    `yaml:"dim"`
	BaseURL   string `yaml:"base_url"`
	APIKeyEnv string `yaml:"api_key_env"`
	TokenEnv  string `yaml:"token_env"`
}

// MemoryConfig configures the SQLite memory store.
type MemoryConfig struct {
	DBPath      string `yaml:"db_path"`      // "~" expanded
	TokenBudget int    `yaml:"token_budget"` // default 8000
	RecallK     int    `yaml:"recall_k"`     // default 5
}

// ToolsConfig toggles built-in tool groups.
type ToolsConfig struct {
	WebEnabled     bool `yaml:"web_enabled"`
	FilesEnabled   bool `yaml:"files_enabled"`
	BashEnabled    bool `yaml:"bash_enabled"`
	RequireConfirm bool `yaml:"require_confirm"`
}

// SubagentsConfig configures sub-agent personas.
type SubagentsConfig struct {
	Enabled bool   `yaml:"enabled"`
	Dir     string `yaml:"dir"` // default ".dive/agents"
}

// MCPConfig lists MCP servers.
type MCPConfig struct {
	Servers []MCPServerConfig `yaml:"servers"`
}

// MCPServerConfig configures one MCP server (stdio or URL).
type MCPServerConfig struct {
	Name    string            `yaml:"name"`
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args"`
	URL     string            `yaml:"url"`
	Env     map[string]string `yaml:"env,omitempty"`     // stdio env vars; also lets plugin .mcp.json env pass through
	Headers map[string]string `yaml:"headers,omitempty"` // HTTP headers; also lets plugin .mcp.json headers pass through
}

// SkillsConfig configures dive's skill loader.
type SkillsConfig struct {
	Enabled bool     `yaml:"enabled"`
	Dirs    []string `yaml:"dirs"` // defaults: [".dive/skills", "~/.dive/skills"]
}

// PluginsConfig configures Claude Code-compatible plugin discovery.
// Defaults: enabled, dirs=[~/.czcli/plugins, .czcli/plugins]. ~/ is expanded.
type PluginsConfig struct {
	Enabled bool     `yaml:"enabled"`
	Dirs    []string `yaml:"dirs"`
}

// LSPConfig configures the Language Server Protocol manager (Plan 8). When
// disabled, no servers are spawned even if Servers is non-empty. Plugins may
// contribute LSPServerConfigs that are merged with this list at startup.
type LSPConfig struct {
	Enabled bool              `yaml:"enabled"`
	Servers []LSPServerConfig `yaml:"servers"`
}

// LSPServerConfig configures a Language Server Protocol server (stdio). Used
// both by the user-config lsp.servers list and by plugins'
// .claude-plugin/lsp.json contributions.
type LSPServerConfig struct {
	Name         string   `yaml:"name"`
	Command      string   `yaml:"command"`
	Args         []string `yaml:"args"`
	Languages    []string `yaml:"languages"`
	RootPatterns []string `yaml:"root_patterns"`
}

// ScheduleConfig defines a cron-scheduled prompt.
type ScheduleConfig struct {
	Name    string `yaml:"name"`
	Cron    string `yaml:"cron"`
	Prompt  string `yaml:"prompt"`
	Channel string `yaml:"channel"`
	Enabled bool   `yaml:"enabled"`
}

// Load reads YAML from path, applies defaults, expands "~" in Memory.DBPath,
// and validates the result.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	applyDefaults(&cfg)
	expanded, err := expandHome(cfg.Memory.DBPath)
	if err != nil {
		return nil, fmt.Errorf("expand db_path: %w", err)
	}
	cfg.Memory.DBPath = expanded
	for i, d := range cfg.Plugins.Dirs {
		ed, err := expandHome(d)
		if err != nil {
			return nil, fmt.Errorf("expand plugins.dirs[%d]: %w", i, err)
		}
		cfg.Plugins.Dirs[i] = ed
	}
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Memory.TokenBudget == 0 {
		cfg.Memory.TokenBudget = 8000
	}
	if cfg.Memory.RecallK == 0 {
		cfg.Memory.RecallK = 5
	}
	if cfg.Subagents.Dir == "" {
		cfg.Subagents.Dir = ".dive/agents"
	}
	for i := range cfg.Providers {
		if cfg.Providers[i].MaxTokens == 0 {
			cfg.Providers[i].MaxTokens = 4096
		}
	}
	applySkillDefaults(&cfg.Skills)
	applyPluginDefaults(&cfg.Plugins)
}

// applyPluginDefaults mirrors applySkillDefaults: full omission of the
// `plugins:` block enables discovery in the two default roots. Explicit
// `enabled: false` or explicit `dirs:` are honored verbatim.
func applyPluginDefaults(p *PluginsConfig) {
	if !p.Enabled && len(p.Dirs) == 0 {
		p.Enabled = true
	}
	if len(p.Dirs) == 0 {
		p.Dirs = []string{"~/.czcli/plugins", ".czcli/plugins"}
	}
}

// applySkillDefaults expands "~" entries and falls back to the two default
// directories when none are configured. Skills are enabled by default; set
// skills.enabled: false in YAML to opt out.
func applySkillDefaults(s *SkillsConfig) {
	if !s.Enabled && len(s.Dirs) == 0 {
		// Distinguish "field omitted" from "user set enabled: false".
		// YAML default for bool is false; we treat omission (no dirs either)
		// as enabled.
		s.Enabled = true
	}
	if len(s.Dirs) == 0 {
		s.Dirs = []string{".dive/skills", "~/.dive/skills"}
	}
	expanded := make([]string, 0, len(s.Dirs))
	for _, d := range s.Dirs {
		e, err := expandHome(d)
		if err != nil || e == "" {
			continue
		}
		expanded = append(expanded, e)
	}
	s.Dirs = expanded
}

// expandHome replaces a leading "~" with the user's home directory.
func expandHome(p string) (string, error) {
	if p == "" {
		return p, nil
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		if p == "~" {
			return home, nil
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

func validate(cfg *Config) error {
	if len(cfg.Providers) == 0 {
		return fmt.Errorf("config: at least one provider is required")
	}
	for i, p := range cfg.Providers {
		switch p.Name {
		case "openai":
			if p.APIKeyEnv == "" {
				return fmt.Errorf("config: providers[%d] (openai): api_key_env is required", i)
			}
		case "bedrock":
			if p.BaseURL == "" {
				return fmt.Errorf("config: providers[%d] (bedrock): base_url is required", i)
			}
			if p.TokenEnv == "" {
				return fmt.Errorf("config: providers[%d] (bedrock): token_env is required", i)
			}
		default:
			return fmt.Errorf("config: providers[%d]: unknown name %q (want openai|bedrock)", i, p.Name)
		}
		if p.Model == "" {
			return fmt.Errorf("config: providers[%d] (%s): model is required", i, p.Name)
		}
	}
	if cfg.Embeddings.Provider == "" {
		return fmt.Errorf("config: embeddings.provider is required")
	}
	if cfg.Embeddings.Dim <= 0 {
		return fmt.Errorf("config: embeddings.dim must be > 0")
	}
	if cfg.Memory.DBPath == "" {
		return fmt.Errorf("config: memory.db_path is required")
	}
	return nil
}
