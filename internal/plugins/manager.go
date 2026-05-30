package plugins

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/caxqueiroz/cax/internal/config"
)

// Contributions is the flat aggregate of every enabled plugin's contributed
// extension points. The agent rebuild path consumes this directly.
type Contributions struct {
	SkillDirs  []string
	AgentDirs  []string
	MCPServers []config.MCPServerConfig
	LSPServers []config.LSPServerConfig
	Hooks      []HookEntry
	Commands   []PluginCommand
}

// PluginCounts breaks down what a discovered plugin contributes, surfaced via
// /plugin list and channel.Status.
type PluginCounts struct {
	Skills, Agents, MCP, LSP, Hooks, Commands int
}

// PluginInfo is a discovered plugin's metadata.
type PluginInfo struct {
	Name    string
	Version string
	Source  string
	Dir     string
	Enabled bool
	Counts  PluginCounts
}

// CloneFunc is the seam used by Install. Production wires
// `exec.CommandContext(ctx, "git", "clone", "--depth=1", url, dest).Run()`;
// tests pass a fake.
type CloneFunc func(ctx context.Context, gitURL, dest string) error

// Manager owns plugin discovery, state, and mutations. Safe for concurrent
// use: every mutation holds a single mutex; Load takes the same mutex briefly
// to read the state file but does not hold it across the (read-only) scan.
type Manager struct {
	cfg       config.PluginsConfig
	stateFile string
	clone     CloneFunc

	mu sync.Mutex
}

// New constructs a Manager. stateFile is typically `~/.czcli/plugins.json`
// (caller resolves ~/). clone may be nil if Install will never be called.
func New(cfg config.PluginsConfig, stateFile string, clone CloneFunc) *Manager {
	return &Manager{cfg: cfg, stateFile: stateFile, clone: clone}
}

// Load discovers, parses, and aggregates all plugins. Per-plugin errors are
// logged and never fail the whole Load. Returns a deterministic ordering
// (plugins sorted by name across all dirs combined).
func (m *Manager) Load(_ context.Context) (Contributions, []PluginInfo, error) {
	if !m.cfg.Enabled {
		return Contributions{}, nil, nil
	}
	m.mu.Lock()
	st, _ := readState(m.stateFile)
	m.mu.Unlock()

	type discovered struct {
		dir, name string
	}
	var found []discovered
	for _, root := range m.cfg.Dirs {
		entries, err := os.ReadDir(root)
		if err != nil {
			if !os.IsNotExist(err) {
				slog.Warn("plugins: read dir", "dir", root, "error", err)
			}
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			pdir := filepath.Join(root, e.Name())
			if _, err := os.Stat(filepath.Join(pdir, ".claude-plugin", "plugin.json")); err != nil {
				continue
			}
			found = append(found, discovered{dir: pdir, name: e.Name()})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].name < found[j].name })

	var contrib Contributions
	var infos []PluginInfo
	seen := make(map[string]bool, len(found))
	for _, d := range found {
		if seen[d.name] {
			slog.Warn("plugins: duplicate plugin name across dirs; keeping first", "name", d.name, "dir", d.dir)
			continue
		}
		seen[d.name] = true

		manifest, err := ReadManifest(d.dir)
		if err != nil {
			slog.Warn("plugins: read manifest", "dir", d.dir, "error", err)
			continue
		}
		// State: explicit disabled wins; absent => enabled.
		enabled := true
		source := "local"
		if entry, ok := st[manifest.Name]; ok {
			enabled = entry.Enabled
			if entry.Source != "" {
				source = entry.Source
			}
		}

		info := PluginInfo{
			Name:    manifest.Name,
			Version: manifest.Version,
			Source:  source,
			Dir:     d.dir,
			Enabled: enabled,
		}

		if !enabled {
			infos = append(infos, info)
			continue
		}

		mcps := ReadMCPServers(d.dir, manifest.Name)
		lsps := ReadLSPServers(d.dir, manifest.Name)
		hooks := ReadHooks(d.dir, manifest.Name)
		cmds := ReadCommands(d.dir, manifest.Name)

		skillsDir := filepath.Join(d.dir, "skills")
		agentsDir := filepath.Join(d.dir, "agents")
		skillCount, agentCount := 0, 0
		if entries, err := os.ReadDir(skillsDir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					skillCount++
				}
			}
			contrib.SkillDirs = append(contrib.SkillDirs, skillsDir)
		}
		if entries, err := os.ReadDir(agentsDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() && filepath.Ext(e.Name()) == ".md" {
					agentCount++
				}
			}
			contrib.AgentDirs = append(contrib.AgentDirs, agentsDir)
		}
		contrib.MCPServers = append(contrib.MCPServers, mcps...)
		contrib.LSPServers = append(contrib.LSPServers, lsps...)
		contrib.Hooks = append(contrib.Hooks, hooks...)
		contrib.Commands = append(contrib.Commands, cmds...)

		info.Counts.Skills = skillCount
		info.Counts.Agents = agentCount
		info.Counts.MCP = len(mcps)
		info.Counts.LSP = len(lsps)
		info.Counts.Hooks = len(hooks)
		info.Counts.Commands = len(cmds)
		infos = append(infos, info)
	}
	return contrib, infos, nil
}

// targetDir is the path the manager will use to install a new plugin under
// the first writable user-scope dir (the first entry in cfg.Dirs).
func (m *Manager) targetDir(name string) (string, error) {
	if len(m.cfg.Dirs) == 0 {
		return "", fmt.Errorf("plugins: no dirs configured")
	}
	return filepath.Join(m.cfg.Dirs[0], name), nil
}

// Install clones gitURL into m.cfg.Dirs[0]/<name>, parses its manifest, and
// records {enabled:true, source:gitURL} in the state file. Returns the
// resulting PluginInfo. Fails if the target directory already exists, the
// clone fails, or the cloned tree has no valid manifest.
func (m *Manager) Install(ctx context.Context, gitURL, name string) (PluginInfo, error) {
	if m.clone == nil {
		return PluginInfo{}, fmt.Errorf("plugins: install requires a CloneFunc")
	}
	if name == "" {
		return PluginInfo{}, fmt.Errorf("plugins: install requires a plugin name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	dest, err := m.targetDir(name)
	if err != nil {
		return PluginInfo{}, err
	}
	if _, err := os.Stat(dest); err == nil {
		return PluginInfo{}, fmt.Errorf("plugins: target %s already exists", dest)
	} else if !os.IsNotExist(err) {
		return PluginInfo{}, fmt.Errorf("plugins: stat target %s: %w", dest, err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return PluginInfo{}, fmt.Errorf("plugins: mkdir parent: %w", err)
	}
	if err := m.clone(ctx, gitURL, dest); err != nil {
		_ = os.RemoveAll(dest)
		return PluginInfo{}, fmt.Errorf("plugins: clone %s: %w", gitURL, err)
	}
	manifest, err := ReadManifest(dest)
	if err != nil {
		_ = os.RemoveAll(dest)
		return PluginInfo{}, fmt.Errorf("plugins: validate cloned manifest: %w", err)
	}

	st, _ := readState(m.stateFile)
	st[manifest.Name] = stateEntry{Enabled: true, Source: gitURL}
	if err := writeState(m.stateFile, st); err != nil {
		return PluginInfo{}, fmt.Errorf("plugins: write state: %w", err)
	}
	return PluginInfo{
		Name: manifest.Name, Version: manifest.Version,
		Source: gitURL, Dir: dest, Enabled: true,
	}, nil
}

// Enable flips the state for name to enabled. Idempotent: enabling an
// already-enabled plugin is not an error. Does NOT require the plugin to be
// discovered (caller can enable a plugin that's about to be added).
func (m *Manager) Enable(name string) error {
	return m.setEnabled(name, true)
}

// Disable flips the state for name to disabled. Idempotent.
func (m *Manager) Disable(name string) error {
	return m.setEnabled(name, false)
}

func (m *Manager) setEnabled(name string, enabled bool) error {
	if name == "" {
		return fmt.Errorf("plugins: name required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st, _ := readState(m.stateFile)
	entry := st[name]
	entry.Enabled = enabled
	st[name] = entry
	return writeState(m.stateFile, st)
}

// Remove deletes the plugin directory and clears its state entry. Errors if
// the plugin is not found in any cfg.Dirs.
func (m *Manager) Remove(name string) error {
	if name == "" {
		return fmt.Errorf("plugins: name required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	var dir string
	for _, root := range m.cfg.Dirs {
		candidate := filepath.Join(root, name)
		if _, err := os.Stat(candidate); err == nil {
			dir = candidate
			break
		}
	}
	if dir == "" {
		return fmt.Errorf("plugins: %q not found in any plugins dir", name)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("plugins: remove %s: %w", dir, err)
	}
	st, _ := readState(m.stateFile)
	delete(st, name)
	return writeState(m.stateFile, st)
}
