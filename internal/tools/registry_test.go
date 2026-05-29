package tools

import (
	"testing"

	"github.com/caxqueiroz/czcli/internal/config"
)

func toolNames(t *testing.T, cfg config.ToolsConfig) map[string]bool {
	t.Helper()
	store := openTestStore(t)
	tools, err := Registry(cfg, store)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Name()] = true
	}
	return names
}

func TestRegistry_AlwaysIncludesRecall(t *testing.T) {
	names := toolNames(t, config.ToolsConfig{})
	if !names["search_memory"] {
		t.Fatal("search_memory must always be present")
	}
}

func TestRegistry_FilesEnabled(t *testing.T) {
	names := toolNames(t, config.ToolsConfig{FilesEnabled: true})
	for _, want := range []string{"Read", "Write", "Edit", "Glob", "Grep"} {
		if !names[want] {
			t.Fatalf("files enabled but %s missing", want)
		}
	}
}

func TestRegistry_FilesDisabled(t *testing.T) {
	names := toolNames(t, config.ToolsConfig{FilesEnabled: false})
	for _, absent := range []string{"Read", "Write", "Edit", "Glob", "Grep"} {
		if names[absent] {
			t.Fatalf("files disabled but %s present", absent)
		}
	}
}

func TestRegistry_WebEnabledAddsFetch(t *testing.T) {
	names := toolNames(t, config.ToolsConfig{WebEnabled: true})
	if !names["WebFetch"] {
		t.Fatal("web enabled but WebFetch missing")
	}
}

func TestRegistry_BashEnabled(t *testing.T) {
	on := toolNames(t, config.ToolsConfig{BashEnabled: true})
	if !on["Bash"] {
		t.Fatal("bash enabled but Bash missing")
	}
	off := toolNames(t, config.ToolsConfig{BashEnabled: false})
	if off["Bash"] {
		t.Fatal("bash disabled but Bash present")
	}
}
