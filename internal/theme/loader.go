package theme

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/muesli/termenv"
	"gopkg.in/yaml.v3"
)

// LoadUserDir scans a directory for *.yaml theme files and registers each
// valid one. Per-file errors are logged + skipped; a broken theme never
// blocks startup. A missing directory is silently ignored.
func LoadUserDir(path string) []*Theme {
	if path == "" {
		return nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("theme: read user dir", "path", path, "err", err)
		}
		return nil
	}
	out := make([]*Theme, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		full := filepath.Join(path, e.Name())
		data, err := os.ReadFile(full)
		if err != nil {
			slog.Warn("theme: read user theme", "file", full, "err", err)
			continue
		}
		var t Theme
		if err := yaml.Unmarshal(data, &t); err != nil {
			slog.Warn("theme: parse user theme", "file", full, "err", err)
			continue
		}
		if err := validateLoaded(&t, full); err != nil {
			slog.Warn("theme: invalid user theme", "file", full, "err", err)
			continue
		}
		Register(&t)
		out = append(out, &t)
	}
	return out
}

// Resolve picks the startup theme. Order:
//  1. state.json "theme" field (if file exists, parses, names a registered theme)
//  2. configThemeName (from config.cli.theme), if it names a registered theme
//  3. terminal-background-adaptive default: default-light or default-dark
//
// The returned theme is also Set() as active so callers don't need to.
func Resolve(stateFile, configThemeName string) *Theme {
	if name := readStateTheme(stateFile); name != "" {
		if t, err := Get(name); err == nil {
			Set(t)
			return t
		}
		slog.Warn("theme: state.json names unknown theme", "name", name)
	}
	if configThemeName != "" {
		if t, err := Get(configThemeName); err == nil {
			Set(t)
			return t
		}
		slog.Warn("theme: config.cli.theme names unknown theme", "name", configThemeName)
	}
	preferred := "default-dark"
	if !termenv.DefaultOutput().HasDarkBackground() {
		preferred = "default-light"
	}
	if t, err := Get(preferred); err == nil {
		Set(t)
		return t
	}
	// Final fallback: first registered theme, whatever it is.
	for _, n := range List() {
		if t, err := Get(n); err == nil {
			Set(t)
			return t
		}
	}
	return nil
}
