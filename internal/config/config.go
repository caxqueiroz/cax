// Package config loads and validates cax's YAML configuration.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Config is the top-level cax configuration.
type Config struct {
	Persona    string            `yaml:"persona"`
	Providers  []ProviderConfig  `yaml:"providers"`
	ModelRoles map[string]string `yaml:"model_roles,omitempty"` // role → provider name; see ModelRoleAgent/Cheap/...
	Embeddings EmbeddingsConfig  `yaml:"embeddings"`
	Memory     MemoryConfig      `yaml:"memory"`
	Tools      ToolsConfig       `yaml:"tools"`
	Subagents  SubagentsConfig   `yaml:"subagents"`
	MCP        MCPConfig         `yaml:"mcp"`
	Skills     SkillsConfig      `yaml:"skills"`
	Plugins    PluginsConfig     `yaml:"plugins"`
	Commands   CommandsConfig    `yaml:"commands"`
	LSP        LSPConfig         `yaml:"lsp"`
	CLI        CLIConfig         `yaml:"cli"`
	Schedules  []ScheduleConfig  `yaml:"schedules"`
}

// Canonical role names for the model router. Roles unset in config fall back
// to ModelRoleCheap, which falls back to ModelRoleAgent.
const (
	ModelRoleAgent           = "agent"            // primary user-facing agent loop
	ModelRoleCheap           = "cheap"            // default for utility roles below
	ModelRoleSummarizer      = "summarizer"       // rolling-summary LLM call
	ModelRoleFactExtractor   = "fact_extractor"   // mem0-style fact extraction
	ModelRoleSubagentDefault = "subagent_default" // dive sub-agent factory default
)

// CLIConfig configures the TUI channel. Theme is the initial theme name;
// resolution falls back to ~/.cax/state.json, then a terminal-adapted
// default-{dark,light}.
type CLIConfig struct {
	Theme string `yaml:"theme"`
	// ShowStreaming controls whether incremental assistant text appears in
	// the conversation as it streams. When false (recommended for quieter
	// sessions) you see only the spinner during the turn and the full reply
	// lands at once on completion. Defaults to true to match prior behaviour.
	ShowStreaming *bool `yaml:"show_streaming,omitempty"`
}

// StreamingEnabled returns the resolved show_streaming value. Defaults true
// when not configured.
func (c CLIConfig) StreamingEnabled() bool {
	if c.ShowStreaming == nil {
		return true
	}
	return *c.ShowStreaming
}

// ProviderConfig configures one LLM provider in fallback order.
//
// Name is a UNIQUE LABEL used by ModelRoles to reference this entry.
// Type is the provider implementation: "openai" | "bedrock". When omitted, it
// defaults to Name lowercased so single-provider configs (the historical
// shape: name: openai) keep working without a Type field. Multi-model
// configs MUST set Type per entry — e.g. two openai entries named
// "openai-main" and "openai-cheap" both with type: openai.
type ProviderConfig struct {
	Name      string `yaml:"name"`
	Type      string `yaml:"type,omitempty"`
	Model     string `yaml:"model"`
	BaseURL   string `yaml:"base_url"`    // bedrock: KrakenD endpoint
	TokenEnv  string `yaml:"token_env"`   // bedrock: env var holding the x-api-key value
	APIKeyEnv string `yaml:"api_key_env"` // openai: env var holding the API key
	MaxTokens int    `yaml:"max_tokens"`  // default 4096 if 0
	// Enabled toggles this provider. Pointer so an absent field defaults to
	// "enabled" (backward compatible). Set `enabled: false` to skip without
	// deleting the entry.
	Enabled *bool `yaml:"enabled,omitempty"`
}

// EffectiveType returns Type if set, else Name lowercased. Used by the
// validator and BuildModel to dispatch on provider implementation.
func (p ProviderConfig) EffectiveType() string {
	if p.Type != "" {
		return strings.ToLower(strings.TrimSpace(p.Type))
	}
	return strings.ToLower(strings.TrimSpace(p.Name))
}

// IsEnabled reports whether the provider should be wired into the fallback
// chain. Default (Enabled == nil) is true.
func (p ProviderConfig) IsEnabled() bool {
	return p.Enabled == nil || *p.Enabled
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
//
// Mode picks the retrieval strategy:
//   - "snippets" (default): legacy behavior — embed raw turns, retrieve top-K
//     snippets at each turn, plus a rolling LLM summary.
//   - "facts": mem0-style — an LLM extractor runs each turn to derive discrete
//     facts ("user prefers X"). PreGeneration injects retrieved facts instead
//     of raw snippets. Snippet table still grows for back-compat but is not
//     queried.
//   - "both": extract facts AND retrieve snippets; inject both.
//
// FactExtractorRole names the model role (see ModelRole* constants) used by
// the extractor. Defaults to ModelRoleFactExtractor → ModelRoleCheap. Pick a
// cheap model — extraction runs every turn when Mode != snippets.
type MemoryConfig struct {
	DBPath             string `yaml:"db_path"`      // "~" expanded
	TokenBudget        int    `yaml:"token_budget"` // default 8000
	RecallK            int    `yaml:"recall_k"`     // default 5
	Mode               string `yaml:"mode,omitempty"`
	FactExtractorRole  string `yaml:"fact_extractor_role,omitempty"`
}

// Memory mode constants.
const (
	MemoryModeSnippets = "snippets"
	MemoryModeFacts    = "facts"
	MemoryModeBoth     = "both"
)

// EffectiveMode returns Mode lowercased, defaulting to MemoryModeSnippets.
func (m MemoryConfig) EffectiveMode() string {
	mode := strings.ToLower(strings.TrimSpace(m.Mode))
	switch mode {
	case MemoryModeFacts, MemoryModeBoth:
		return mode
	default:
		return MemoryModeSnippets
	}
}

// EffectiveFactExtractorRole returns the configured role, defaulting to
// ModelRoleFactExtractor.
func (m MemoryConfig) EffectiveFactExtractorRole() string {
	if r := strings.TrimSpace(m.FactExtractorRole); r != "" {
		return r
	}
	return ModelRoleFactExtractor
}

// ToolsConfig toggles built-in tool groups.
//
// WorkspaceDirs scopes filesystem access for the Bash/Write/Edit/Read/Glob/
// Grep tools. The FIRST entry is the writable workspace root; any additional
// entries are added as read-only allowed paths (dive's PathValidator only
// supports one writable root). Tilde and env vars are expanded. If empty,
// cax defaults to [$HOME] so anything under the user's home is read/write.
type ToolsConfig struct {
	WebEnabled     bool     `yaml:"web_enabled"`
	FilesEnabled   bool     `yaml:"files_enabled"`
	BashEnabled    bool     `yaml:"bash_enabled"`
	RequireConfirm bool     `yaml:"require_confirm"`
	WorkspaceDirs  []string `yaml:"workspace_dirs"`
}

// SubagentsConfig configures sub-agent personas.
// Dirs is the canonical list of search directories; Dir is the legacy
// singular field kept only for one-release backward compatibility. If Dir is
// non-empty and Dirs is empty at Load time, Dirs is set to []string{Dir} and a
// one-shot slog.Warn is emitted via warnLegacySubagentsDir.
type SubagentsConfig struct {
	Enabled bool     `yaml:"enabled"`
	Dirs    []string `yaml:"dirs"`          // default: [~/.cax/agents, .cax/agents]
	Dir     string   `yaml:"dir,omitempty"` // DEPRECATED: legacy singular; migrated into Dirs at Load time.
}

// CommandsConfig configures user-level slash-command discovery (Plan 11).
// Enabled defaults to true when the section is omitted; Dirs defaults to
// [~/.cax/commands, .cax/commands] with tilde expansion applied at Load.
type CommandsConfig struct {
	Enabled bool     `yaml:"enabled"`
	Dirs    []string `yaml:"dirs"`
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
	Dirs    []string `yaml:"dirs"` // defaults: ["~/.cax/skills", ".cax/skills"]
}

// PluginsConfig configures Claude Code-compatible plugin discovery.
// Defaults: enabled, dirs=[~/.cax/plugins, .cax/plugins]. ~/ is expanded.
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
	applySubagentDefaults(&cfg.Subagents)
	for i := range cfg.Providers {
		if cfg.Providers[i].MaxTokens == 0 {
			cfg.Providers[i].MaxTokens = 4096
		}
	}
	applySkillDefaults(&cfg.Skills)
	applyPluginDefaults(&cfg.Plugins)
	applyCommandsDefaults(&cfg.Commands)
}

var subagentsDirWarnOnce sync.Once

// warnLegacySubagentsDir logs a one-shot deprecation warning the first time a
// config file with the legacy singular `subagents.dir` is loaded.
func warnLegacySubagentsDir(dir string) {
	subagentsDirWarnOnce.Do(func() {
		slog.Warn("config: subagents.dir (singular) is deprecated; migrate to subagents.dirs",
			"legacy_value", dir,
			"migrated_to", []string{dir})
	})
}

// applySubagentDefaults migrates the legacy singular Dir into Dirs (with a
// one-shot warning), then falls back to the two cax-namespaced defaults
// when nothing is configured. Tilde-expansion is applied to the final list.
func applySubagentDefaults(s *SubagentsConfig) {
	if !s.Enabled && len(s.Dirs) == 0 && s.Dir == "" {
		// Field omission == enabled by default (mirrors Skills/Plugins).
		s.Enabled = true
	}
	if len(s.Dirs) == 0 && s.Dir != "" {
		warnLegacySubagentsDir(s.Dir)
		s.Dirs = []string{s.Dir}
	}
	if len(s.Dirs) == 0 {
		s.Dirs = []string{"~/.cax/agents", ".cax/agents"}
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

// applyCommandsDefaults mirrors applySkillDefaults: omission of the
// `commands:` block enables discovery in the two cax-namespaced roots.
func applyCommandsDefaults(c *CommandsConfig) {
	if !c.Enabled && len(c.Dirs) == 0 {
		c.Enabled = true
	}
	if len(c.Dirs) == 0 {
		c.Dirs = []string{"~/.cax/commands", ".cax/commands"}
	}
	expanded := make([]string, 0, len(c.Dirs))
	for _, d := range c.Dirs {
		e, err := expandHome(d)
		if err != nil || e == "" {
			continue
		}
		expanded = append(expanded, e)
	}
	c.Dirs = expanded
}

// applyPluginDefaults mirrors applySkillDefaults: full omission of the
// `plugins:` block enables discovery in the two default roots. Explicit
// `enabled: false` or explicit `dirs:` are honored verbatim.
func applyPluginDefaults(p *PluginsConfig) {
	if !p.Enabled && len(p.Dirs) == 0 {
		p.Enabled = true
	}
	if len(p.Dirs) == 0 {
		p.Dirs = []string{"~/.cax/plugins", ".cax/plugins"}
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
		s.Dirs = []string{"~/.cax/skills", ".cax/skills"}
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

// Validate runs the same checks Load runs after parsing. Exposed so other
// packages can validate a hand-built Config without re-parsing YAML.
func Validate(cfg *Config) error { return validate(cfg) }

func validate(cfg *Config) error {
	if len(cfg.Providers) == 0 {
		return fmt.Errorf("config: at least one provider is required")
	}
	enabledCount := 0
	names := make(map[string]int, len(cfg.Providers))
	for i, p := range cfg.Providers {
		if p.Name == "" {
			return fmt.Errorf("config: providers[%d]: name is required", i)
		}
		if prev, ok := names[p.Name]; ok {
			return fmt.Errorf("config: providers[%d]: duplicate name %q (also at index %d)", i, p.Name, prev)
		}
		names[p.Name] = i
		if !p.IsEnabled() {
			// Disabled providers are skipped at build time; don't enforce
			// field requirements so users can keep template entries.
			continue
		}
		enabledCount++
		switch p.EffectiveType() {
		case "openai":
			if p.APIKeyEnv == "" {
				return fmt.Errorf("config: providers[%d] (%s): api_key_env is required", i, p.Name)
			}
		case "bedrock":
			if p.BaseURL == "" {
				return fmt.Errorf("config: providers[%d] (%s): base_url is required", i, p.Name)
			}
			if p.TokenEnv == "" {
				return fmt.Errorf("config: providers[%d] (%s): token_env is required", i, p.Name)
			}
		default:
			return fmt.Errorf("config: providers[%d] (%s): unknown type %q (want openai|bedrock)", i, p.Name, p.EffectiveType())
		}
		if p.Model == "" {
			return fmt.Errorf("config: providers[%d] (%s): model is required", i, p.Name)
		}
	}
	if enabledCount == 0 {
		return fmt.Errorf("config: at least one provider must be enabled (set enabled: true)")
	}
	// Validate model_roles point at known provider names.
	for role, name := range cfg.ModelRoles {
		if _, ok := names[name]; !ok {
			return fmt.Errorf("config: model_roles[%q] references unknown provider name %q", role, name)
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
