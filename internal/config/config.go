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
	Name    string   `yaml:"name"`
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
	URL     string   `yaml:"url"`
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
