# Plan 6: Skills + MCP — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire dive v1.7.0's top-level `skill` package for markdown-discovered Skills and rewrite `internal/mcp` over the separately-versioned `experimental/mcp` module so czcli can connect real stdio/HTTP MCP servers, then make `agent.Build` accept skills + MCP tools and ship `agent.Rebuild` for hot-reload.

**Architecture:** `skill.Loader` is a `dive.Extension`; `internal/skills` wraps it over project + home + `extraDirs` (plugin contributions later). `experimental/mcp.Manager` owns server connections (stdio via `command/args/env`, HTTP via `url`), exposes adapted `dive.Tool`s; `internal/mcp.Connect` returns `([]dive.Tool, []ServerInfo, error)` and writes OAuth tokens to a file-backed `*FileOAuthTokenStore`. `agent.Build` registers the skill loader as an `Extension` and appends MCP tools; `agent.Rebuild` swaps the inner `*dive.Agent` under a mutex for hot-reload.

**Tech Stack:** dive v1.7.0 (`skill`, `dive.Extension`, `dive.AgentOptions.Extensions`), `github.com/deepnoodle-ai/dive/experimental/mcp` v1.7.0 (separate go.mod: `Manager`, `ServerConfig`, `FileOAuthTokenStore`, `ToolAdapter`), `log/slog`.

---

## File Structure

```
internal/
├── config/config.go                MODIFY  add SkillsConfig; Config.Skills field; defaults+validate; +_test.go cases
├── skills/skills.go                NEW     Load(cfg, extraDirs) → *LoadResult{Loader *skill.Loader, Names []string}
├── skills/skills_test.go           NEW     temp .dive/skills/<name>/SKILL.md → assert Names + extraDirs merge
├── mcp/mcp.go                      REWRITE Connect(ctx, []MCPServerConfig, tokenStorePath) → ([]dive.Tool,[]ServerInfo,error)
├── mcp/mcp_test.go                 NEW     fake stdio command + bad config → assert ServerInfo + Last Error
├── channel/channel.go              MODIFY  add SkillCount/SkillNames/MCPServerCount/MCPServerNames/LSPServerCount/LSPLanguages/PluginCount/PluginNames/HookCount
├── channel/cli/commands.go         MODIFY  add /skills + /mcp dispatch + handlers
├── channel/cli/commands_test.go    MODIFY  assert /skills and /mcp output
├── agent/agent.go                  MODIFY  Build(ctx,cfg,store,model,*skills.LoadResult,mcpTools) ; new Rebuild(...) under mutex
├── agent/subagents.go              MODIFY  drop mcp.Connect call (mcpTools now flow in from Build)
├── agent/agent_test.go             MODIFY  Build with skills+mcpTools; Rebuild swaps tools; Status carries Skill/MCP names
cmd/czcli/main.go                   MODIFY  load skills + mcp at startup; pass into agent.Build
config.example.yaml                 MODIFY  add skills: section; document mcp: as real (stdio/url)
go.mod / go.sum                     MODIFY  add github.com/deepnoodle-ai/dive/experimental/mcp v1.7.0
```

---

### Task 1: Add `SkillsConfig` to `internal/config`

**Files:**
- MODIFY `internal/config/config.go`
- MODIFY `internal/config/config_test.go`

- [ ] Write failing test `TestLoadAppliesSkillsDefaults` in `internal/config/config_test.go` asserting that when `skills:` is absent the loaded `cfg.Skills` has `Enabled=true` and `Dirs=[".dive/skills", "<HOME>/.dive/skills"]`.

  ```go
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
      want := []string{".dive/skills", filepath.Join(home, ".dive/skills")}
      if len(cfg.Skills.Dirs) != len(want) {
          t.Fatalf("Skills.Dirs len = %d, want %d (%v)", len(cfg.Skills.Dirs), len(want), cfg.Skills.Dirs)
      }
      for i, d := range want {
          if cfg.Skills.Dirs[i] != d {
              t.Errorf("Skills.Dirs[%d] = %q, want %q", i, cfg.Skills.Dirs[i], d)
          }
      }
  }
  ```

- [ ] Run `go test ./internal/config/...` and observe FAIL (undefined `cfg.Skills`).

- [ ] Add `SkillsConfig` and the `Skills` field to `Config`, then expand defaults in `applyDefaults`. Final edits in `internal/config/config.go`:

  Add to `Config` struct (after `MCP`):
  ```go
  	Skills     SkillsConfig     `yaml:"skills"`
  ```

  Add new type below `MCPServerConfig`:
  ```go
  // SkillsConfig configures dive's skill loader.
  type SkillsConfig struct {
      Enabled bool     `yaml:"enabled"`
      Dirs    []string `yaml:"dirs"` // defaults: [".dive/skills", "~/.dive/skills"]
  }
  ```

  Extend `applyDefaults` (replace whole func body):
  ```go
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
  }

  // applySkillDefaults expands "~" entries and falls back to the two default
  // directories when none are configured. Skills are enabled by default; set
  // skills.enabled: false in YAML to opt out.
  func applySkillDefaults(s *SkillsConfig) {
      if !s.Enabled && len(s.Dirs) == 0 {
          // Distinguish "field omitted" from "user set enabled: false".
          // YAML default for bool is false; we treat omission as enabled.
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
  ```

- [ ] Run `go test ./internal/config/...` and observe PASS.

- [ ] Commit:
  ```bash
  git add internal/config/config.go internal/config/config_test.go
  git commit -m "$(cat <<'EOF'
  feat(config): add SkillsConfig with default dirs

  Skills load from .dive/skills and ~/.dive/skills by default; expandHome
  applied per entry. Plan 6 Task 1.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 2: Create `internal/skills` over dive's `skill.Loader`

**Files:**
- NEW `internal/skills/skills.go`
- NEW `internal/skills/skills_test.go`

- [ ] Write failing test `TestLoadDiscoversSkillsFromCfgAndExtraDirs` in `internal/skills/skills_test.go`:

  ```go
  package skills

  import (
      "os"
      "path/filepath"
      "testing"

      "github.com/caxqueiroz/czcli/internal/config"
  )

  func writeSkill(t *testing.T, dir, name, description string) {
      t.Helper()
      sd := filepath.Join(dir, name)
      if err := os.MkdirAll(sd, 0o755); err != nil {
          t.Fatalf("mkdir %s: %v", sd, err)
      }
      body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# " + name + "\n"
      if err := os.WriteFile(filepath.Join(sd, "SKILL.md"), []byte(body), 0o600); err != nil {
          t.Fatalf("write skill: %v", err)
      }
  }

  func TestLoadDiscoversSkillsFromCfgAndExtraDirs(t *testing.T) {
      projDir := t.TempDir()
      extra := t.TempDir()
      writeSkill(t, projDir, "alpha", "Alpha skill")
      writeSkill(t, extra, "beta", "Beta skill")

      res, err := Load(config.SkillsConfig{Enabled: true, Dirs: []string{projDir}}, []string{extra})
      if err != nil {
          t.Fatalf("Load: %v", err)
      }
      if res == nil || res.Loader == nil {
          t.Fatal("Load returned nil result/loader")
      }
      gotNames := map[string]bool{}
      for _, n := range res.Names {
          gotNames[n] = true
      }
      if !gotNames["alpha"] || !gotNames["beta"] {
          t.Errorf("Names = %v, want alpha+beta", res.Names)
      }
  }

  func TestLoadDisabledReturnsEmpty(t *testing.T) {
      res, err := Load(config.SkillsConfig{Enabled: false}, nil)
      if err != nil {
          t.Fatalf("Load: %v", err)
      }
      if res == nil {
          t.Fatal("Load returned nil result when disabled")
      }
      if len(res.Names) != 0 {
          t.Errorf("Names = %v, want empty", res.Names)
      }
      if res.Loader == nil {
          t.Errorf("Loader should be non-nil even when disabled (extension still cleans stale catalog)")
      }
  }
  ```

- [ ] Run `go test ./internal/skills/...` and observe FAIL (package missing).

- [ ] Create `internal/skills/skills.go`:

  ```go
  // Package skills wraps dive's top-level skill.Loader for czcli. It loads
  // skills from cfg.Dirs ∪ extraDirs (plugin-contributed) and returns a
  // *skill.Loader ready to be registered as a dive.Extension on the agent.
  package skills

  import (
      "context"
      "fmt"
      "log/slog"

      "github.com/caxqueiroz/czcli/internal/config"
      "github.com/deepnoodle-ai/dive/skill"
  )

  // LoadResult is the value the agent consumes: the dive skill Loader (which
  // implements dive.Extension) plus the sorted skill names for /skills and
  // channel.Status.
  type LoadResult struct {
      Loader *skill.Loader
      Names  []string
  }

  // logBridge adapts log/slog to dive's skill.Logger so per-provider warnings
  // (e.g. unreadable dir) land in czcli's structured log instead of stderr.
  type logBridge struct{}

  func (logBridge) Debug(msg string, args ...any) { slog.Debug("skills: "+msg, args...) }
  func (logBridge) Warn(msg string, args ...any)  { slog.Warn("skills: "+msg, args...) }

  // Load builds a dive skill.Loader over cfg.Dirs ∪ extraDirs. Disabled config
  // returns an empty (but non-nil) Loader so the catalog-cleanup hook still
  // runs on resumed sessions. Per-skill / per-provider errors are logged and
  // skipped; an aggregate error returns only if every provider failed.
  func Load(cfg config.SkillsConfig, extraDirs []string) (*LoadResult, error) {
      if !cfg.Enabled {
          loader := skill.NewLoader(skill.LoaderOptions{Logger: logBridge{}})
          return &LoadResult{Loader: loader, Names: nil}, nil
      }

      // Merge cfg.Dirs with plugin extraDirs; dive's LoaderOptions takes the
      // first dir as ProjectDir, the second as HomeDir, and the rest via
      // AdditionalPaths. Empty entries are skipped.
      var (
          project string
          home    string
          extra   []string
      )
      all := append([]string(nil), cfg.Dirs...)
      all = append(all, extraDirs...)
      for _, d := range all {
          if d == "" {
              continue
          }
          switch {
          case project == "":
              project = d
          case home == "":
              home = d
          default:
              extra = append(extra, d)
          }
      }

      loader, err := skill.Load(context.Background(), skill.LoaderOptions{
          ProjectDir:      project,
          HomeDir:         home,
          AdditionalPaths: extra,
          Logger:          logBridge{},
      })
      if err != nil {
          return nil, fmt.Errorf("skills: load: %w", err)
      }
      return &LoadResult{Loader: loader, Names: loader.Names()}, nil
  }
  ```

- [ ] Run `go test ./internal/skills/...` and observe PASS.

- [ ] Commit:
  ```bash
  git add internal/skills/skills.go internal/skills/skills_test.go
  git commit -m "$(cat <<'EOF'
  feat(skills): wrap dive skill.Loader over cfg + extraDirs

  Loader implements dive.Extension so the agent registers it via
  AgentOptions.Extensions in Task 5. Plan 6 Task 2.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 3: Pin the `experimental/mcp` module

**Files:**
- MODIFY `go.mod`
- MODIFY `go.sum`

- [ ] Run `go get github.com/deepnoodle-ai/dive/experimental/mcp@v1.7.0` to fetch the separately-tagged module. The tag in upstream is `experimental/mcp/v1.7.0`; the Go toolchain resolves the module path automatically.

- [ ] Run `go mod tidy` and confirm `go.mod` gains a `require github.com/deepnoodle-ai/dive/experimental/mcp v1.7.0` entry (transitively pulling `github.com/mark3labs/mcp-go v0.45.0`).

- [ ] Run `go build ./...` and observe it still compiles (mcp module isn't imported yet, just downloaded).

- [ ] Commit:
  ```bash
  git add go.mod go.sum
  git commit -m "$(cat <<'EOF'
  chore(deps): pin dive experimental/mcp v1.7.0

  Separately-versioned module providing the MCP Manager + tool adapter
  consumed by internal/mcp in Task 4. Plan 6 Task 3.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 4: Rewrite `internal/mcp` over the real Manager

**Files:**
- REWRITE `internal/mcp/mcp.go`
- NEW `internal/mcp/mcp_test.go`

- [ ] Write failing test `internal/mcp/mcp_test.go`:

  ```go
  package mcp

  import (
      "context"
      "path/filepath"
      "testing"

      "github.com/caxqueiroz/czcli/internal/config"
  )

  // TestConnectEmptyReturnsNoTools is the trivial path — no servers, no work.
  func TestConnectEmptyReturnsNoTools(t *testing.T) {
      tools, infos, err := Connect(context.Background(), nil, filepath.Join(t.TempDir(), "tokens.json"))
      if err != nil {
          t.Fatalf("Connect: %v", err)
      }
      if len(tools) != 0 || len(infos) != 0 {
          t.Errorf("tools=%d infos=%d, want both 0", len(tools), len(infos))
      }
  }

  // TestConnectStdioFailureRecorded confirms a bogus stdio server is reported
  // as a not-connected ServerInfo with a LastError, never aborts other servers.
  func TestConnectStdioFailureRecorded(t *testing.T) {
      servers := []config.MCPServerConfig{
          {Name: "bogus", Command: "/nonexistent/binary-xyz", Args: []string{"--help"}},
      }
      tokens := filepath.Join(t.TempDir(), "tokens.json")
      tools, infos, err := Connect(context.Background(), servers, tokens)
      if err != nil {
          t.Fatalf("Connect: %v", err)
      }
      if len(tools) != 0 {
          t.Errorf("tools = %d, want 0 (server should fail to start)", len(tools))
      }
      if len(infos) != 1 {
          t.Fatalf("infos len = %d, want 1", len(infos))
      }
      got := infos[0]
      if got.Name != "bogus" || got.Transport != "stdio" || got.Connected {
          t.Errorf("got %+v, want Name=bogus Transport=stdio Connected=false", got)
      }
      if got.LastError == "" {
          t.Errorf("LastError empty, want non-empty failure reason")
      }
  }

  // TestConnectUnknownTransport rejects a server with neither Command nor URL.
  func TestConnectUnknownTransport(t *testing.T) {
      servers := []config.MCPServerConfig{{Name: "blank"}}
      tokens := filepath.Join(t.TempDir(), "tokens.json")
      _, infos, err := Connect(context.Background(), servers, tokens)
      if err != nil {
          t.Fatalf("Connect: %v", err)
      }
      if len(infos) != 1 || infos[0].Connected || infos[0].LastError == "" {
          t.Fatalf("got %+v, want one failed ServerInfo with LastError set", infos)
      }
  }
  ```

- [ ] Run `go test ./internal/mcp/...` and observe FAIL (Connect signature mismatch).

- [ ] Rewrite `internal/mcp/mcp.go`:

  ```go
  // Package mcp connects configured MCP servers (stdio or HTTP) using dive's
  // experimental/mcp module and exposes their tools as dive.Tool. OAuth tokens
  // are persisted to a file-backed FileOAuthTokenStore so they survive across
  // czcli runs.
  package mcp

  import (
      "context"
      "fmt"
      "log/slog"
      "sort"

      "github.com/caxqueiroz/czcli/internal/config"
      "github.com/deepnoodle-ai/dive"
      divemcp "github.com/deepnoodle-ai/dive/experimental/mcp"
  )

  // ServerInfo describes the runtime status of a configured MCP server. The
  // CLI's /mcp slash command and channel.Status read this struct.
  type ServerInfo struct {
      Name      string
      Transport string // "stdio" | "http"
      Connected bool
      ToolCount int
      LastError string
  }

  // Connect initializes every configured MCP server best-effort. Stdio servers
  // require Command; HTTP servers require URL. Per-server errors are logged
  // and surfaced via ServerInfo.LastError; the function only returns an error
  // for configuration-layer failures (e.g. inability to ensure the token-store
  // directory).
  //
  // tokenStorePath is the on-disk path used for any server that opts into
  // OAuth (currently keyed via OAuth on the dive ServerConfig). Today no
  // czcli MCPServerConfig fields surface OAuth, so the path is reserved for
  // when plugins or per-server config expose it (Plan 7+).
  func Connect(ctx context.Context, servers []config.MCPServerConfig, tokenStorePath string) ([]dive.Tool, []ServerInfo, error) {
      if len(servers) == 0 {
          return nil, nil, nil
      }

      // Eagerly construct the file token store so the directory exists before
      // any server starts an OAuth flow. We do not return its error: a missing
      // tokens dir is best-effort; servers that don't use OAuth continue.
      if _, terr := divemcp.NewFileOAuthTokenStore(tokenStorePath); terr != nil {
          slog.Warn("mcp: failed to prepare token store; OAuth servers will fall back to memory", "path", tokenStorePath, "err", terr)
      }

      manager := divemcp.NewManager()

      var (
          serverConfigs []*divemcp.ServerConfig
          infos         []ServerInfo
      )

      // Stage 1: translate czcli config → dive ServerConfig, dropping invalid
      // entries with a failed ServerInfo so callers see why each was skipped.
      for _, s := range servers {
          transport, dcfg, err := toServerConfig(s)
          info := ServerInfo{Name: s.Name, Transport: transport}
          if err != nil {
              info.LastError = err.Error()
              slog.Warn("mcp: invalid server config", "name", s.Name, "err", err)
              infos = append(infos, info)
              continue
          }
          serverConfigs = append(serverConfigs, dcfg)
          infos = append(infos, info)
      }

      // Stage 2: initialize one-by-one (Manager.InitializeServers aborts on
      // first error in v1.7; we want best-effort, so call per-server).
      for i, info := range infos {
          if info.LastError != "" {
              continue
          }
          cfg := lookupConfig(serverConfigs, info.Name)
          if cfg == nil {
              continue
          }
          if err := manager.InitializeServers(ctx, []*divemcp.ServerConfig{cfg}); err != nil {
              info.Connected = false
              info.LastError = err.Error()
              slog.Warn("mcp: server init failed", "name", info.Name, "err", err)
              infos[i] = info
              continue
          }
          serverTools := manager.GetToolsByServer(info.Name)
          info.Connected = manager.IsServerConnected(info.Name)
          info.ToolCount = len(serverTools)
          infos[i] = info
      }

      // Stage 3: collect adapted tools, sorted for determinism.
      toolMap := manager.GetAllTools()
      toolNames := make([]string, 0, len(toolMap))
      for n := range toolMap {
          toolNames = append(toolNames, n)
      }
      sort.Strings(toolNames)
      tools := make([]dive.Tool, 0, len(toolNames))
      for _, n := range toolNames {
          tools = append(tools, toolMap[n])
      }
      return tools, infos, nil
  }

  // toServerConfig maps czcli's MCPServerConfig into dive's ServerConfig and
  // selects the transport. Stdio wins when both Command and URL are set; that
  // matches the dive client's own dispatch.
  func toServerConfig(s config.MCPServerConfig) (string, *divemcp.ServerConfig, error) {
      switch {
      case s.Command != "":
          return "stdio", &divemcp.ServerConfig{
              Name:    s.Name,
              Type:    "stdio",
              Command: s.Command,
              Args:    s.Args,
          }, nil
      case s.URL != "":
          return "http", &divemcp.ServerConfig{
              Name: s.Name,
              Type: "http",
              URL:  s.URL,
          }, nil
      default:
          return "", nil, fmt.Errorf("mcp server %q: neither command nor url set", s.Name)
      }
  }

  // lookupConfig finds the ServerConfig for a given server name. The slice is
  // expected to be short (single-digit), linear scan is fine.
  func lookupConfig(configs []*divemcp.ServerConfig, name string) *divemcp.ServerConfig {
      for _, c := range configs {
          if c.Name == name {
              return c
          }
      }
      return nil
  }
  ```

- [ ] Run `go test ./internal/mcp/...` and observe PASS.

- [ ] Commit:
  ```bash
  git add internal/mcp/mcp.go internal/mcp/mcp_test.go
  git commit -m "$(cat <<'EOF'
  feat(mcp): real Connect over dive experimental/mcp Manager

  Replaces the no-op stub with stdio + HTTP transports, file-backed OAuth
  token store, and best-effort per-server error reporting via ServerInfo.
  Plan 6 Task 4.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 5: Extend `channel.Status` with extensibility fields

**Files:**
- MODIFY `internal/channel/channel.go`

- [ ] Add the new fields to `Status` so Plans 6–9 can populate them without further package edits. There is no behavior change yet; this is a pure data-shape extension.

  Replace the existing `Status` block in `internal/channel/channel.go` with:
  ```go
  // Status is the always-on dashboard snapshot the TUI renders.
  type Status struct {
      Provider         string
      Model            string
      OnFallback       bool
      FallbackIndex    int
      ContextTokens    int
      ContextBudget    int
      Usage            UsageRollup
      MemSizeBytes     int64
      MessageCount     int
      MemoryCount      int
      ToolNames        []string
      SubagentNames    []string
      RunningSubagents []string

      // Extensibility (Plans 6–9). Empty until the relevant plan populates them.
      SkillCount     int
      SkillNames     []string
      MCPServerCount int
      MCPServerNames []string
      LSPServerCount int
      LSPLanguages   []string
      PluginCount    int
      PluginNames    []string
      HookCount      int
  }
  ```

- [ ] Run `go build ./...` and observe PASS (no existing code reads the new fields yet).
- [ ] Run `go test ./...` and observe PASS.

- [ ] Commit:
  ```bash
  git add internal/channel/channel.go
  git commit -m "$(cat <<'EOF'
  feat(channel): add Skill/MCP/LSP/Plugin/Hook fields to Status

  Locks the Status shape for Plans 6–9 so later plans never touch the
  channel package again. Plan 6 Task 5.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 6: Extend `agent.Build` with skills + MCP, drop the in-agent `mcp.Connect`

**Files:**
- MODIFY `internal/agent/agent.go`
- MODIFY `internal/agent/subagents.go`
- MODIFY `internal/agent/agent_test.go`

- [ ] Write failing test `TestBuildAcceptsSkillsAndMCPTools` in `internal/agent/agent_test.go` (append):

  ```go
  func TestBuildAcceptsSkillsAndMCPTools(t *testing.T) {
      cfg := minimalCfg(t)
      store := openTestStore(t, cfg)
      defer store.Close()

      model := fakeStreamingLLM("dummy")

      // Build a skills LoadResult from a temp dir with one SKILL.md.
      sdir := t.TempDir()
      if err := os.MkdirAll(filepath.Join(sdir, "ping"), 0o755); err != nil {
          t.Fatalf("mkdir: %v", err)
      }
      if err := os.WriteFile(filepath.Join(sdir, "ping", "SKILL.md"),
          []byte("---\nname: ping\ndescription: Ping skill\n---\n# Ping\n"), 0o600); err != nil {
          t.Fatalf("write skill: %v", err)
      }
      skillRes, err := skills.Load(config.SkillsConfig{Enabled: true, Dirs: []string{sdir}}, nil)
      if err != nil {
          t.Fatalf("skills.Load: %v", err)
      }

      // A trivial fake MCP tool to assert it appears in the tool set.
      mcpTool := fakeTool("mcp_echo")

      a, err := agent.Build(context.Background(), cfg, store, model, skillRes, []dive.Tool{mcpTool})
      if err != nil {
          t.Fatalf("Build: %v", err)
      }
      st, err := a.Status(context.Background())
      if err != nil {
          t.Fatalf("Status: %v", err)
      }
      gotTool := false
      for _, n := range st.ToolNames {
          if n == "mcp_echo" {
              gotTool = true
          }
      }
      if !gotTool {
          t.Errorf("ToolNames = %v, want it to include mcp_echo", st.ToolNames)
      }
      if st.SkillCount != 1 || len(st.SkillNames) != 1 || st.SkillNames[0] != "ping" {
          t.Errorf("Skill fields = (%d,%v), want (1,[ping])", st.SkillCount, st.SkillNames)
      }
      if st.MCPServerCount != 0 {
          // We did not pass server infos; Build must default cleanly to 0.
          t.Errorf("MCPServerCount = %d, want 0", st.MCPServerCount)
      }
  }
  ```

  Note `minimalCfg`, `openTestStore`, `fakeStreamingLLM`, `fakeTool` already exist in `internal/agent/testsupport_test.go` from Plans 1–5; if any are missing, add them in the same test file using the patterns already there.

- [ ] Run `go test ./internal/agent/...` and observe FAIL (Build signature mismatch).

- [ ] Update `internal/agent/agent.go`:
  - Add imports `"sync"` (already present), `"github.com/caxqueiroz/czcli/internal/skills"`, `"github.com/deepnoodle-ai/dive/skill"`.
  - Add a `*skill.Loader` field + a `sync.RWMutex` for atomic swap.
  - Rewrite `Build` and add `Rebuild`.

  Replace the existing `Assistant` struct and `Build`:

  ```go
  // Assistant wraps a configured dive.Agent plus the dependencies needed to run
  // turns, report status, and summarize memory. The inner *dive.Agent is
  // swappable via Rebuild under buildMu so hot-reload survives in-flight turns.
  type Assistant struct {
      store *memory.Store
      model llm.StreamingLLM
      cfg   *config.Config

      buildMu  sync.RWMutex
      agent    *dive.Agent
      tools    []dive.Tool
      skillRes *skills.LoadResult

      subagentNames []string

      mu      sync.Mutex
      running map[string]int // running sub-agent task descriptions -> ref count
  }

  // Build assembles the dive.Agent with skills registered as a dive.Extension
  // (v1.7 wires skill.Loader through AgentOptions.Extensions), MCP tools, and
  // the existing memory hooks. Plan 6 introduces the skills + mcpTools params;
  // Plans 7–9 will extend this further with plugin contributions, LSP tools,
  // and the hook dispatcher — each plan passes through whatever Plan 6 owns.
  func Build(
      ctx context.Context,
      cfg *config.Config,
      store *memory.Store,
      model llm.StreamingLLM,
      skillRes *skills.LoadResult,
      mcpTools []dive.Tool,
  ) (*Assistant, error) {
      if cfg == nil {
          return nil, fmt.Errorf("agent: nil config")
      }
      if model == nil {
          return nil, fmt.Errorf("agent: nil model")
      }

      builtins, err := tools.Registry(cfg.Tools, store)
      if err != nil {
          return nil, fmt.Errorf("agent: build tool registry: %w", err)
      }

      a := &Assistant{
          store:    store,
          model:    model,
          cfg:      cfg,
          tools:    builtins,
          skillRes: skillRes,
          running:  make(map[string]int),
      }

      // MCP tools come from cmd/czcli/main.go via mcp.Connect; mcp.Connect is
      // no longer called inside augmentTools.
      a.tools = append(a.tools, mcpTools...)

      // Sub-agents stay inside augmentTools but no longer reach into mcp.
      if err := a.augmentTools(ctx, model); err != nil {
          return nil, fmt.Errorf("agent: augment tools: %w", err)
      }

      dialog := tools.ConfirmDialog(cfg.Tools.RequireConfirm, os.Stdin, os.Stdout)
      deps := &hookDeps{
          store:        store,
          cfg:          cfg,
          dialog:       dialog,
          summarizerFn: func() memory.Summarizer { return a.Summarizer() },
      }

      opts := dive.AgentOptions{
          Name:         "czcli",
          SystemPrompt: cfg.Persona,
          Model:        model,
          Tools:        a.tools,
          Hooks: dive.Hooks{
              PreGeneration:  []dive.PreGenerationHook{deps.preGeneration},
              PreToolUse:     []dive.PreToolUseHook{deps.preToolUse},
              PostGeneration: []dive.PostGenerationHook{deps.postGeneration},
          },
      }
      if skillRes != nil && skillRes.Loader != nil {
          opts.Extensions = append(opts.Extensions, skillRes.Loader)
      }

      diveAgent, err := dive.NewAgent(opts)
      if err != nil {
          return nil, fmt.Errorf("agent: new dive agent: %w", err)
      }
      a.agent = diveAgent
      return a, nil
  }

  // Rebuild swaps the inner *dive.Agent atomically. In-flight turns finish
  // under the existing agent; the next turn picks up the new one. Plans 7–9
  // call this after /plugin install|enable|disable mutations.
  func (a *Assistant) Rebuild(
      ctx context.Context,
      cfg *config.Config,
      skillRes *skills.LoadResult,
      mcpTools []dive.Tool,
  ) error {
      next, err := Build(ctx, cfg, a.store, a.model, skillRes, mcpTools)
      if err != nil {
          return fmt.Errorf("agent: rebuild: %w", err)
      }
      a.buildMu.Lock()
      a.agent = next.agent
      a.tools = next.tools
      a.skillRes = next.skillRes
      a.subagentNames = next.subagentNames
      a.cfg = cfg
      a.buildMu.Unlock()
      return nil
  }
  ```

  Replace `Handle` to acquire the read lock around `a.agent`:
  ```go
  func (a *Assistant) Handle(ctx context.Context, msg channel.Message, emit channel.EventSink) (channel.Reply, error) {
      if emit == nil {
          emit = func(channel.StreamEvent) {}
      }
      var mu sync.Mutex

      callback := func(_ context.Context, item *dive.ResponseItem) error {
          mu.Lock()
          defer mu.Unlock()
          switch item.Type {
          case dive.ResponseItemTypeModelEvent:
              ev := item.Event
              if ev != nil && ev.Type == llm.EventTypeContentBlockDelta && ev.Delta != nil &&
                  ev.Delta.Type == llm.EventDeltaTypeText && ev.Delta.Text != "" {
                  emit(channel.StreamEvent{Type: "text", Text: ev.Delta.Text})
              }
          case dive.ResponseItemTypeToolCall:
              if item.ToolCall != nil {
                  if item.ToolCall.Name == subagentToolName {
                      a.subagentStarted(item.ToolCall.Name)
                      emit(channel.StreamEvent{Type: "subagent_start", Text: item.ToolCall.Name})
                  } else {
                      emit(channel.StreamEvent{Type: "tool_start", Text: item.ToolCall.Name})
                  }
              }
          case dive.ResponseItemTypeToolCallResult:
              if item.ToolCallResult != nil {
                  if item.ToolCallResult.Name == subagentToolName {
                      a.subagentEnded(item.ToolCallResult.Name)
                      emit(channel.StreamEvent{Type: "subagent_end", Text: item.ToolCallResult.Name})
                  } else {
                      emit(channel.StreamEvent{Type: "tool_end", Text: item.ToolCallResult.Name})
                  }
              }
          }
          return nil
      }

      a.buildMu.RLock()
      diveAgent := a.agent
      a.buildMu.RUnlock()

      resp, err := diveAgent.CreateResponse(ctx,
          dive.WithInput(msg.Text),
          dive.WithValue("session_id", msg.SessionID),
          dive.WithValue("user_input", msg.Text),
          dive.WithEventCallback(callback),
      )
      if err != nil {
          emit(channel.StreamEvent{Type: "error", Text: err.Error()})
          return channel.Reply{}, fmt.Errorf("agent: create response: %w", err)
      }
      return channel.Reply{Text: resp.OutputText()}, nil
  }
  ```

  Extend `Status` to populate the new fields (replace the existing body):
  ```go
  func (a *Assistant) Status(ctx context.Context) (channel.Status, error) {
      a.buildMu.RLock()
      tools := a.tools
      skillRes := a.skillRes
      subagentNames := a.subagentNames
      a.buildMu.RUnlock()

      st := channel.Status{
          Provider:         a.model.Name(),
          Model:            a.model.Name(),
          ContextBudget:    a.cfg.Memory.TokenBudget,
          ToolNames:        toolNamesOf(tools),
          SubagentNames:    subagentNames,
          RunningSubagents: a.runningSubagents(),
      }
      if st.ContextBudget == 0 {
          st.ContextBudget = 8000
      }
      if reporter, ok := a.model.(ActiveReporter); ok {
          idx, name := reporter.Active()
          st.Model = name
          st.Provider = name
          st.FallbackIndex = idx
          st.OnFallback = idx > 0
      }
      if skillRes != nil {
          st.SkillCount = len(skillRes.Names)
          st.SkillNames = append([]string(nil), skillRes.Names...)
      }
      if stats, err := a.store.Stats(ctx); err == nil {
          st.MemSizeBytes = stats.DBSizeBytes
          st.MessageCount = stats.MessageCount
          st.MemoryCount = stats.MemoryCount
      }
      if roll, err := a.store.UsageRollups(ctx); err == nil {
          st.Usage = channel.UsageRollup{
              Day:   channel.UsageTotals{InputTokens: roll.Day.InputTokens, OutputTokens: roll.Day.OutputTokens},
              Week:  channel.UsageTotals{InputTokens: roll.Week.InputTokens, OutputTokens: roll.Week.OutputTokens},
              Month: channel.UsageTotals{InputTokens: roll.Month.InputTokens, OutputTokens: roll.Month.OutputTokens},
          }
      }
      return st, nil
  }

  // toolNamesOf returns the names of the given tools in registry order.
  func toolNamesOf(tools []dive.Tool) []string {
      names := make([]string, 0, len(tools))
      for _, tl := range tools {
          names = append(names, tl.Name())
      }
      return names
  }
  ```

  Delete the now-unused `(a *Assistant) toolNames()` method.

- [ ] Update `internal/agent/subagents.go` to stop calling `mcp.Connect` (mcp tools now flow in via `Build`). Replace the function body's MCP block:

  ```go
  func (a *Assistant) augmentTools(ctx context.Context, model llm.StreamingLLM) error {
      // MCP tools are appended in Build before augmentTools runs.
      if !a.cfg.Subagents.Enabled {
          return nil
      }

      defs := map[string]*subagent.Definition{
          "GeneralPurpose": subagent.GeneralPurpose,
          "Explore":        subagent.Explore,
          "Plan":           subagent.Plan,
      }
      if dir := a.cfg.Subagents.Dir; dir != "" {
          loader := &subagent.FileLoader{Directories: []string{dir}}
          loaded, lerr := loader.Load(ctx)
          if lerr != nil {
              slog.Warn("subagent file loader failed", "dir", dir, "err", lerr)
          } else {
              for name, def := range loaded {
                  defs[name] = def
              }
          }
      }

      parentTools := append([]dive.Tool(nil), a.tools...)

      factory := func(_ context.Context, name string, def *subagent.Definition, pt []dive.Tool) (*dive.Agent, error) {
          filtered := subagent.FilterTools(def, pt)
          sa, ferr := dive.NewAgent(dive.AgentOptions{
              Name:         name,
              SystemPrompt: def.Prompt,
              Model:        model,
              Tools:        filtered,
          })
          if ferr != nil {
              return nil, fmt.Errorf("agent: build subagent %q: %w", name, ferr)
          }
          return sa, nil
      }

      runs := orchestration.NewRuns()
      agentTool := orchestration.NewAgentTool(orchestration.AgentToolOptions{
          Subagents:    defs,
          AgentFactory: factory,
          ParentTools:  parentTools,
          Runs:         runs,
      })
      taskStop := orchestration.NewTaskStopTool(orchestration.TaskStopToolOptions{Runs: runs})
      a.tools = append(a.tools, agentTool, taskStop)
      a.subagentNames = sortedNames(defs)
      return nil
  }
  ```

  Drop the now-unused `import "github.com/caxqueiroz/czcli/internal/mcp"` from `subagents.go`.

- [ ] Run `go test ./internal/agent/...` and observe PASS.

- [ ] Commit:
  ```bash
  git add internal/agent/agent.go internal/agent/subagents.go internal/agent/agent_test.go
  git commit -m "$(cat <<'EOF'
  feat(agent): wire skills extension + MCP tools through Build; add Rebuild

  Build accepts *skills.LoadResult and pre-connected MCP tools; the skill
  Loader registers as a dive.Extension. Rebuild swaps the inner *dive.Agent
  atomically for hot-reload (Plans 7+). Plan 6 Task 6.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 7: Add `/skills` and `/mcp` slash commands

**Files:**
- MODIFY `internal/channel/cli/commands.go`
- MODIFY `internal/channel/cli/commands_test.go`

- [ ] Write failing tests in `internal/channel/cli/commands_test.go` (append):

  ```go
  func TestCmdSkillsLists(t *testing.T) {
      m := model{hasStatus: true, status: channel.Status{SkillCount: 2, SkillNames: []string{"alpha", "beta"}}}
      out, quit := m.handleCommand("/skills")
      if quit {
          t.Fatalf("/skills should not quit")
      }
      if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") || !strings.Contains(out, "2") {
          t.Errorf("/skills out = %q, want alpha+beta+count", out)
      }
  }

  func TestCmdSkillsEmpty(t *testing.T) {
      m := model{hasStatus: true, status: channel.Status{}}
      out, _ := m.handleCommand("/skills")
      if !strings.Contains(out, "no skills") {
          t.Errorf("/skills empty out = %q, want 'no skills...'", out)
      }
  }

  func TestCmdMCPLists(t *testing.T) {
      m := model{hasStatus: true, status: channel.Status{
          MCPServerCount: 2,
          MCPServerNames: []string{"git", "github"},
      }}
      out, _ := m.handleCommand("/mcp")
      if !strings.Contains(out, "git") || !strings.Contains(out, "github") {
          t.Errorf("/mcp out = %q, want server names", out)
      }
  }

  func TestCmdMCPEmpty(t *testing.T) {
      m := model{hasStatus: true, status: channel.Status{}}
      out, _ := m.handleCommand("/mcp")
      if !strings.Contains(out, "no mcp") {
          t.Errorf("/mcp empty out = %q, want 'no mcp...'", out)
      }
  }
  ```

- [ ] Run `go test ./internal/channel/cli/...` and observe FAIL (unknown commands).

- [ ] In `internal/channel/cli/commands.go` add the cases to the switch in `handleCommand` and add the handlers below. Edit the switch:

  Replace the existing switch body in `handleCommand` with:
  ```go
      switch name {
      case "quit", "exit":
          return "", true
      case "stats":
          return m.cmdStats(), false
      case "tools":
          return m.cmdTools(), false
      case "agents":
          return m.cmdAgents(), false
      case "schedule":
          return m.cmdSchedule(args), false
      case "model":
          return m.cmdModel(), false
      case "skills":
          return m.cmdSkills(), false
      case "mcp":
          return m.cmdMCP(), false
      default:
          return fmt.Sprintf("unknown command /%s — try /stats /tools /agents /schedule /model /skills /mcp", name), false
      }
  ```

  Append at the bottom of `commands.go`:
  ```go
  // cmdSkills renders the loaded skill catalog. Plan 7 will let plugins
  // contribute extra dirs; that flows in transparently through Status.
  func (m model) cmdSkills() string {
      if !m.hasStatus {
          return "skills unavailable (no status yet)"
      }
      s := m.status
      if s.SkillCount == 0 {
          return "no skills loaded (configure skills.dirs in config.yaml)"
      }
      return fmt.Sprintf("skills (%d): %s", s.SkillCount, strings.Join(s.SkillNames, ", "))
  }

  // cmdMCP renders the configured MCP servers and their connection state.
  func (m model) cmdMCP() string {
      if !m.hasStatus {
          return "mcp unavailable (no status yet)"
      }
      s := m.status
      if s.MCPServerCount == 0 {
          return "no mcp servers configured (add entries under mcp.servers in config.yaml)"
      }
      return fmt.Sprintf("mcp servers (%d): %s", s.MCPServerCount, strings.Join(s.MCPServerNames, ", "))
  }
  ```

- [ ] Run `go test ./internal/channel/cli/...` and observe PASS.

- [ ] Commit:
  ```bash
  git add internal/channel/cli/commands.go internal/channel/cli/commands_test.go
  git commit -m "$(cat <<'EOF'
  feat(cli): add /skills and /mcp slash commands

  Both render counts + names from channel.Status; further detail (server
  transport, last error) lands once Plan 7 plumbs MCPServerInfo through.
  Plan 6 Task 7.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 8: Surface MCP server names in `Status` from `main.go`

**Files:**
- MODIFY `internal/agent/agent.go`
- MODIFY `cmd/czcli/main.go`
- MODIFY `internal/agent/agent_test.go`

`Build` accepts MCP tools but `Status` has nowhere to read MCP server *names* from — they live in the `[]mcp.ServerInfo` returned alongside the tools. Pass the infos into `Build` and surface them in `Status`.

- [ ] Write failing test `TestStatusReportsMCPServerNames` in `internal/agent/agent_test.go`:

  ```go
  func TestStatusReportsMCPServerNames(t *testing.T) {
      cfg := minimalCfg(t)
      store := openTestStore(t, cfg)
      defer store.Close()
      model := fakeStreamingLLM("dummy")
      infos := []mcp.ServerInfo{
          {Name: "git", Transport: "stdio", Connected: true, ToolCount: 3},
          {Name: "github", Transport: "http", Connected: false, LastError: "auth"},
      }
      a, err := agent.BuildWithMCPInfos(context.Background(), cfg, store, model, nil, nil, infos)
      if err != nil {
          t.Fatalf("Build: %v", err)
      }
      st, err := a.Status(context.Background())
      if err != nil {
          t.Fatalf("Status: %v", err)
      }
      if st.MCPServerCount != 2 {
          t.Errorf("MCPServerCount = %d, want 2", st.MCPServerCount)
      }
      wantNames := map[string]bool{"git": true, "github": true}
      for _, n := range st.MCPServerNames {
          delete(wantNames, n)
      }
      if len(wantNames) != 0 {
          t.Errorf("MCPServerNames missing %v (got %v)", wantNames, st.MCPServerNames)
      }
  }
  ```

- [ ] Run `go test ./internal/agent/...` and observe FAIL (`BuildWithMCPInfos` undefined).

- [ ] In `internal/agent/agent.go` add `BuildWithMCPInfos` and have `Build` delegate. Store the infos on the Assistant and read them in `Status`:

  Add field to `Assistant`:
  ```go
      mcpServerNames []string
  ```

  Add new constructor and refactor `Build` to delegate:
  ```go
  // Build keeps the narrow signature that Plan 6 owns. Callers that already
  // know their MCP ServerInfos should use BuildWithMCPInfos.
  func Build(
      ctx context.Context,
      cfg *config.Config,
      store *memory.Store,
      model llm.StreamingLLM,
      skillRes *skills.LoadResult,
      mcpTools []dive.Tool,
  ) (*Assistant, error) {
      return BuildWithMCPInfos(ctx, cfg, store, model, skillRes, mcpTools, nil)
  }

  // BuildWithMCPInfos is Build plus the MCP ServerInfos so Status can render
  // server names without re-querying the manager. Plan 7 will use it for
  // plugin-contributed servers; cmd/czcli/main.go uses it from Task 9.
  func BuildWithMCPInfos(
      ctx context.Context,
      cfg *config.Config,
      store *memory.Store,
      model llm.StreamingLLM,
      skillRes *skills.LoadResult,
      mcpTools []dive.Tool,
      mcpInfos []mcp.ServerInfo,
  ) (*Assistant, error) {
      // (body identical to the previous Build, plus a single line that records
      // mcp server names from mcpInfos before returning a.)
      // ... existing assembly ...
  ```

  At the end of the assembly (just before `return a, nil`), add:
  ```go
      names := make([]string, 0, len(mcpInfos))
      for _, info := range mcpInfos {
          names = append(names, info.Name)
      }
      a.mcpServerNames = names
  ```

  In `Status`, extend the MCP block:
  ```go
      st.MCPServerCount = len(a.mcpServerNames)
      st.MCPServerNames = append([]string(nil), a.mcpServerNames...)
  ```

  Update `Rebuild` to take MCP infos too:
  ```go
  func (a *Assistant) Rebuild(
      ctx context.Context,
      cfg *config.Config,
      skillRes *skills.LoadResult,
      mcpTools []dive.Tool,
      mcpInfos []mcp.ServerInfo,
  ) error {
      next, err := BuildWithMCPInfos(ctx, cfg, a.store, a.model, skillRes, mcpTools, mcpInfos)
      if err != nil {
          return fmt.Errorf("agent: rebuild: %w", err)
      }
      a.buildMu.Lock()
      a.agent = next.agent
      a.tools = next.tools
      a.skillRes = next.skillRes
      a.subagentNames = next.subagentNames
      a.mcpServerNames = next.mcpServerNames
      a.cfg = cfg
      a.buildMu.Unlock()
      return nil
  }
  ```

  Add the import: `"github.com/caxqueiroz/czcli/internal/mcp"`.

- [ ] Run `go test ./internal/agent/...` and observe PASS.

- [ ] Commit:
  ```bash
  git add internal/agent/agent.go internal/agent/agent_test.go
  git commit -m "$(cat <<'EOF'
  feat(agent): expose MCP server names via BuildWithMCPInfos

  Plain Build remains the contract surface; BuildWithMCPInfos is the
  superset main.go and Rebuild use to populate Status.MCPServerNames.
  Plan 6 Task 8.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 9: Wire startup in `cmd/czcli/main.go`

**Files:**
- MODIFY `cmd/czcli/main.go`

- [ ] Edit `cmd/czcli/main.go` to load skills and MCP before building the assistant, and pass them into `agent.BuildWithMCPInfos`.

  Add imports:
  ```go
      "path/filepath"

      "github.com/caxqueiroz/czcli/internal/mcp"
      "github.com/caxqueiroz/czcli/internal/skills"
  ```

  Replace the section between `model, err := agent.BuildModel(cfg)` and `assistant, err := agent.Build(...)` with:

  ```go
      model, err := agent.BuildModel(cfg)
      if err != nil {
          return fmt.Errorf("build model: %w", err)
      }

      // Load skills (best-effort: missing dirs are logged, not fatal).
      skillRes, err := skills.Load(cfg.Skills, nil)
      if err != nil {
          slog.Warn("skills: load failed; continuing without skills", "err", err)
          skillRes = nil
      }

      // Connect MCP servers (best-effort: per-server errors land in ServerInfo).
      tokenStorePath := mcpTokenPath()
      mcpTools, mcpInfos, err := mcp.Connect(ctx, cfg.MCP.Servers, tokenStorePath)
      if err != nil {
          slog.Warn("mcp: connect failed; continuing without MCP tools", "err", err)
      }

      assistant, err := agent.BuildWithMCPInfos(ctx, cfg, store, model, skillRes, mcpTools, mcpInfos)
      if err != nil {
          return fmt.Errorf("build assistant: %w", err)
      }
  ```

  Add the helper near the bottom of the file:
  ```go
  // mcpTokenPath returns the default OAuth token-store path under the user's
  // home dir, falling back to a process-local file when home is unresolvable.
  func mcpTokenPath() string {
      home, err := os.UserHomeDir()
      if err != nil {
          return filepath.Join(os.TempDir(), "czcli-mcp-tokens.json")
      }
      return filepath.Join(home, ".czcli", "mcp-tokens.json")
  }
  ```

- [ ] Run `go build ./...` and observe PASS.
- [ ] Run `go test ./...` and observe PASS.

- [ ] Commit:
  ```bash
  git add cmd/czcli/main.go
  git commit -m "$(cat <<'EOF'
  feat(main): load skills + connect MCP servers at startup

  Best-effort skill load + mcp.Connect feed into agent.BuildWithMCPInfos.
  OAuth tokens persist to ~/.czcli/mcp-tokens.json. Plan 6 Task 9.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 10: Document Skills + real MCP in `config.example.yaml`

**Files:**
- MODIFY `config.example.yaml`

- [ ] Replace the `mcp:` block and append a `skills:` block. New tail of the file:

  ```yaml
  # MCP servers (real connections via dive experimental/mcp).
  # Each server uses stdio when `command` is set, HTTP when `url` is set.
  mcp:
    servers: []
    # Example stdio server (filesystem MCP):
    # - name: fs
    #   command: npx
    #   args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
    # Example HTTP server:
    # - name: remote
    #   url: https://mcp.example.com/sse

  # Skills (markdown-defined agent capabilities).
  # Loaded from cfg.Dirs (defaults: .dive/skills and ~/.dive/skills) plus any
  # plugin-contributed directories (Plan 7).
  skills:
    enabled: true
    dirs:
      - .dive/skills
      - ~/.dive/skills

  schedules: []
  ```

  (Remove the previous trailing `mcp: servers: []` and `schedules: []` lines.)

- [ ] Run `go test ./internal/config/...` (the existing `TestLoadExampleConfig` test parses this file) and observe PASS.

- [ ] Commit:
  ```bash
  git add config.example.yaml
  git commit -m "$(cat <<'EOF'
  docs(config): document skills section and real MCP server entries

  MCP is no longer a no-op: stdio (command) and HTTP (url) examples added,
  plus the new skills: block. Plan 6 Task 10.

  Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 11: Final verification

**Files:** none changed.

- [ ] Run `go mod tidy` and observe no diff (deps already pinned in Task 3).
- [ ] Run `go test ./...` and observe PASS across all packages.
- [ ] Run `golangci-lint run ./...` and observe no lints.
- [ ] Run `go build ./...` and observe a clean build.

No commit (verification only).
