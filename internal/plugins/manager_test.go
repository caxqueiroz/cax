package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/caxqueiroz/cax/internal/config"
)

func writeFullPlugin(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "commands"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "skills", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"),
		[]byte(`{"name":"`+name+`","version":"0.1.0","description":"d"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "commands", "hi.md"),
		[]byte("---\ndescription: say hi\n---\nHi $ARGUMENTS"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"),
		[]byte(`{"mcpServers":{"x":{"command":"./x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "demo", "SKILL.md"),
		[]byte("---\ndescription: d\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestManagerLoadAggregates(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	writeFullPlugin(t, dirA, "alpha")
	writeFullPlugin(t, dirB, "beta")
	statePath := filepath.Join(t.TempDir(), "plugins.json")
	m := New(config.PluginsConfig{Enabled: true, Dirs: []string{dirA, dirB}}, statePath, nil)

	contrib, infos, err := m.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("infos = %d, want 2 (%+v)", len(infos), infos)
	}
	if len(contrib.SkillDirs) != 2 || len(contrib.AgentDirs) != 2 {
		t.Errorf("SkillDirs/AgentDirs not aggregated: %+v", contrib)
	}
	if len(contrib.MCPServers) != 2 {
		t.Errorf("MCPServers = %+v", contrib.MCPServers)
	}
	if len(contrib.Commands) != 2 {
		t.Errorf("Commands = %+v", contrib.Commands)
	}
}

func TestManagerLoadDisabledDropped(t *testing.T) {
	dir := t.TempDir()
	writeFullPlugin(t, dir, "alpha")
	statePath := filepath.Join(t.TempDir(), "plugins.json")
	if err := writeState(statePath, state{"alpha": {Enabled: false, Source: "local"}}); err != nil {
		t.Fatal(err)
	}
	m := New(config.PluginsConfig{Enabled: true, Dirs: []string{dir}}, statePath, nil)
	contrib, infos, _ := m.Load(context.Background())
	if len(contrib.SkillDirs) != 0 || len(contrib.Commands) != 0 {
		t.Errorf("disabled plugin should contribute nothing: %+v", contrib)
	}
	if len(infos) != 1 || infos[0].Enabled {
		t.Errorf("info should still appear but disabled: %+v", infos)
	}
}

func TestManagerLoadSkipsDirsWithoutManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "junk"), 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "plugins.json")
	m := New(config.PluginsConfig{Enabled: true, Dirs: []string{dir}}, statePath, nil)
	contrib, infos, err := m.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(infos) != 0 || len(contrib.Commands) != 0 {
		t.Errorf("no-manifest dir should be skipped: infos=%+v contrib=%+v", infos, contrib)
	}
}

func TestManagerLoadDisabledViaConfig(t *testing.T) {
	dir := t.TempDir()
	writeFullPlugin(t, dir, "alpha")
	statePath := filepath.Join(t.TempDir(), "plugins.json")
	m := New(config.PluginsConfig{Enabled: false, Dirs: []string{dir}}, statePath, nil)
	contrib, infos, _ := m.Load(context.Background())
	if len(infos) != 0 || len(contrib.SkillDirs) != 0 {
		t.Errorf("disabled subsystem should return empty: infos=%+v contrib=%+v", infos, contrib)
	}
}

func TestManagerInstallCallsCloneAndEnables(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "plugins.json")
	cloned := ""
	clone := func(_ context.Context, url, dest string) error {
		cloned = url
		if err := os.MkdirAll(filepath.Join(dest, ".claude-plugin"), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dest, ".claude-plugin", "plugin.json"),
			[]byte(`{"name":"gamma","version":"0.0.1"}`), 0o644)
	}
	m := New(config.PluginsConfig{Enabled: true, Dirs: []string{root}}, statePath, clone)
	info, err := m.Install(context.Background(), "https://github.com/x/gamma", "gamma")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if cloned != "https://github.com/x/gamma" {
		t.Errorf("clone URL = %q", cloned)
	}
	if info.Name != "gamma" || !info.Enabled || info.Source != "https://github.com/x/gamma" {
		t.Errorf("info = %+v", info)
	}
	st, _ := readState(statePath)
	if !st["gamma"].Enabled || st["gamma"].Source != "https://github.com/x/gamma" {
		t.Errorf("state = %+v", st["gamma"])
	}
}

func TestManagerInstallRefusesExisting(t *testing.T) {
	root := t.TempDir()
	writeFullPlugin(t, root, "alpha")
	statePath := filepath.Join(t.TempDir(), "plugins.json")
	clone := func(context.Context, string, string) error { return nil }
	m := New(config.PluginsConfig{Enabled: true, Dirs: []string{root}}, statePath, clone)
	if _, err := m.Install(context.Background(), "git://x", "alpha"); err == nil {
		t.Fatal("expected error when target dir already exists")
	}
}

func TestManagerEnableDisable(t *testing.T) {
	root := t.TempDir()
	writeFullPlugin(t, root, "alpha")
	statePath := filepath.Join(t.TempDir(), "plugins.json")
	m := New(config.PluginsConfig{Enabled: true, Dirs: []string{root}}, statePath, nil)
	if err := m.Disable("alpha"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	st, _ := readState(statePath)
	if st["alpha"].Enabled {
		t.Error("Disable did not persist")
	}
	if err := m.Enable("alpha"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	st, _ = readState(statePath)
	if !st["alpha"].Enabled {
		t.Error("Enable did not persist")
	}
}

func TestManagerRemoveDeletesAndForgetsState(t *testing.T) {
	root := t.TempDir()
	writeFullPlugin(t, root, "alpha")
	statePath := filepath.Join(t.TempDir(), "plugins.json")
	m := New(config.PluginsConfig{Enabled: true, Dirs: []string{root}}, statePath, nil)
	if err := m.Disable("alpha"); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove("alpha"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "alpha")); !os.IsNotExist(err) {
		t.Errorf("plugin dir not removed: %v", err)
	}
	st, _ := readState(statePath)
	if _, has := st["alpha"]; has {
		t.Error("state entry not cleared")
	}
}

func TestManagerRemoveUnknown(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "plugins.json")
	m := New(config.PluginsConfig{Enabled: true, Dirs: []string{root}}, statePath, nil)
	if err := m.Remove("ghost"); err == nil {
		t.Fatal("Remove of unknown plugin should error")
	}
}
