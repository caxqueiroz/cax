package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakePlugins is an in-memory pluginBackend for testing the /plugin command.
type fakePlugins struct {
	items        map[string]PluginListItem
	installErr   error
	installedURL string
	rebuilds     int
	listErr      error
}

func newFakePlugins() *fakePlugins {
	return &fakePlugins{items: map[string]PluginListItem{}}
}

func (f *fakePlugins) List(context.Context) ([]PluginListItem, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]PluginListItem, 0, len(f.items))
	for _, p := range f.items {
		out = append(out, p)
	}
	return out, nil
}

func (f *fakePlugins) Install(_ context.Context, gitURL, name string) error {
	if f.installErr != nil {
		return f.installErr
	}
	f.installedURL = gitURL
	if name == "" {
		name = "from-git"
	}
	f.items[name] = PluginListItem{Name: name, Source: gitURL, Enabled: true}
	return nil
}

func (f *fakePlugins) Enable(_ context.Context, name string) error {
	p, ok := f.items[name]
	if !ok {
		return errors.New("not found")
	}
	p.Enabled = true
	f.items[name] = p
	return nil
}

func (f *fakePlugins) Disable(_ context.Context, name string) error {
	p, ok := f.items[name]
	if !ok {
		return errors.New("not found")
	}
	p.Enabled = false
	f.items[name] = p
	return nil
}

func (f *fakePlugins) Remove(_ context.Context, name string) error {
	if _, ok := f.items[name]; !ok {
		return errors.New("not found")
	}
	delete(f.items, name)
	return nil
}

func (f *fakePlugins) Rebuild(context.Context) error {
	f.rebuilds++
	return nil
}

func TestPluginNoBackend(t *testing.T) {
	m := newModel(80, 24)
	out := m.cmdPlugin("list")
	if !strings.Contains(out, "not available") {
		t.Errorf("expected unavailable, got %q", out)
	}
}

func TestPluginListEmpty(t *testing.T) {
	m := newModel(80, 24)
	m.plugins = newFakePlugins()
	out := m.cmdPlugin("list")
	if !strings.Contains(out, "no plugins") {
		t.Errorf("got %q", out)
	}
}

func TestPluginListRenders(t *testing.T) {
	fk := newFakePlugins()
	fk.items["foo"] = PluginListItem{Name: "foo", Version: "1.0", Source: "git://x", Enabled: true, SkillCount: 2, MCPCount: 1}
	fk.items["bar"] = PluginListItem{Name: "bar", Version: "0.1", Source: "local", Enabled: false}
	m := newModel(80, 24)
	m.plugins = fk
	out := m.cmdPlugin("list")
	for _, want := range []string{"foo", "1.0", "git://x", "on", "bar", "off"} {
		if !strings.Contains(out, want) {
			t.Errorf("list missing %q in:\n%s", want, out)
		}
	}
}

func TestPluginInstallTriggersRebuild(t *testing.T) {
	fk := newFakePlugins()
	m := newModel(80, 24)
	m.plugins = fk
	out := m.cmdPlugin("install https://github.com/x/foo foo")
	if !strings.Contains(out, "installed") {
		t.Errorf("install output = %q", out)
	}
	if fk.installedURL != "https://github.com/x/foo" {
		t.Errorf("install URL = %q", fk.installedURL)
	}
	if fk.rebuilds != 1 {
		t.Errorf("rebuilds after install = %d, want 1", fk.rebuilds)
	}
}

func TestPluginInferredName(t *testing.T) {
	fk := newFakePlugins()
	m := newModel(80, 24)
	m.plugins = fk
	m.cmdPlugin("install https://github.com/x/foo.git")
	if _, has := fk.items["foo"]; !has {
		t.Errorf("expected inferred name 'foo', got items=%+v", fk.items)
	}
}

func TestPluginEnableDisableRemoveRebuild(t *testing.T) {
	fk := newFakePlugins()
	fk.items["foo"] = PluginListItem{Name: "foo", Enabled: false}
	m := newModel(80, 24)
	m.plugins = fk

	m.cmdPlugin("enable foo")
	if !fk.items["foo"].Enabled {
		t.Error("enable did not flip")
	}
	if fk.rebuilds != 1 {
		t.Errorf("rebuilds after enable = %d", fk.rebuilds)
	}

	m.cmdPlugin("disable foo")
	if fk.items["foo"].Enabled {
		t.Error("disable did not flip")
	}
	if fk.rebuilds != 2 {
		t.Errorf("rebuilds after disable = %d", fk.rebuilds)
	}

	out := m.cmdPlugin("remove foo")
	if _, has := fk.items["foo"]; has {
		t.Error("remove did not delete")
	}
	if fk.rebuilds != 3 {
		t.Errorf("rebuilds after remove = %d", fk.rebuilds)
	}
	if !strings.Contains(out, "removed") {
		t.Errorf("remove output = %q", out)
	}
}

func TestPluginUsage(t *testing.T) {
	fk := newFakePlugins()
	m := newModel(80, 24)
	m.plugins = fk
	for _, in := range []string{"", "bogus", "install"} {
		out := m.cmdPlugin(in)
		if !strings.Contains(out, "usage") {
			t.Errorf("cmdPlugin(%q) should show usage, got %q", in, out)
		}
	}
}

func TestPluginInstallError(t *testing.T) {
	fk := newFakePlugins()
	fk.installErr = errors.New("clone failed")
	m := newModel(80, 24)
	m.plugins = fk
	out := m.cmdPlugin("install https://x/y z")
	if !strings.Contains(out, "install failed") {
		t.Errorf("got %q", out)
	}
	if fk.rebuilds != 0 {
		t.Errorf("no rebuild on failed install, got %d", fk.rebuilds)
	}
}
