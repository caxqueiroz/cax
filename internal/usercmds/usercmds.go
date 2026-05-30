// Package usercmds loads user-level slash commands from disk. The on-disk
// shape mirrors Claude Code's `commands/*.md` format used by the plugin
// loader: a markdown file with optional YAML frontmatter (description,
// argument-hint) followed by the prompt body. Each loaded file becomes one
// plugins.PluginCommand so the CLI dispatcher can merge user + plugin
// commands into a single slice.
package usercmds

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/caxqueiroz/czcli/internal/plugins"
)

// Load scans each directory in dirs for *.md files and returns one
// PluginCommand per file. Missing directories are silently skipped; per-file
// read or parse errors are logged via slog and skipped. The returned slice is
// sorted by Name across all input directories. Source is set to
// "user:<basename-of-dir>" so the slash dispatcher can tell user-level
// commands apart from plugin-level ones at render time.
func Load(dirs []string) []plugins.PluginCommand {
	var out []plugins.PluginCommand
	for _, dir := range dirs {
		out = append(out, loadDir(dir)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func loadDir(dir string) []plugins.PluginCommand {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("usercmds: read dir", "dir", dir, "error", err)
		}
		return nil
	}
	src := "user:" + filepath.Base(dir)
	var out []plugins.PluginCommand
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("usercmds: read file", "file", path, "error", err)
			continue
		}
		fm, body := splitFrontmatter(data)
		var meta cmdFrontmatter
		if len(fm) > 0 {
			if err := yaml.Unmarshal(fm, &meta); err != nil {
				slog.Warn("usercmds: parse frontmatter", "file", path, "error", err)
			}
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		out = append(out, plugins.PluginCommand{
			Name:         name,
			Description:  meta.Description,
			ArgumentHint: meta.ArgumentHint,
			Prompt:       string(body),
			Source:       src,
		})
	}
	return out
}

type cmdFrontmatter struct {
	Description  string `yaml:"description"`
	ArgumentHint string `yaml:"argument-hint"`
}

// splitFrontmatter mirrors internal/plugins.splitFrontmatter (private there).
// Returns the YAML between leading "---" markers and the remaining markdown
// body. If no leading "---\n" is found, returns nil, data.
func splitFrontmatter(data []byte) (yamlSrc, body []byte) {
	const sep = "---\n"
	if !bytes.HasPrefix(data, []byte(sep)) {
		return nil, data
	}
	rest := data[len(sep):]
	end := bytes.Index(rest, []byte("\n---\n"))
	if end < 0 {
		end = bytes.Index(rest, []byte("\n---"))
		if end < 0 {
			return nil, data
		}
	}
	fm := rest[:end]
	tail := rest[end:]
	tail = bytes.TrimPrefix(tail, []byte("\n---\n"))
	tail = bytes.TrimPrefix(tail, []byte("\n---"))
	tail = bytes.TrimPrefix(tail, []byte("\n"))
	return fm, tail
}
