// Package skills wraps dive's top-level skill.Loader for cax. It loads
// skills from cfg.Dirs ∪ extraDirs (plugin-contributed) and returns a
// *skill.Loader ready to be registered as a dive.Extension on the agent.
package skills

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/caxqueiroz/cax/internal/config"
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
// (e.g. unreadable dir) land in cax's structured log instead of stderr.
type logBridge struct{}

func (logBridge) Debug(msg string, args ...any) { slog.Debug("skills: "+msg, args...) }
func (logBridge) Warn(msg string, args ...any)  { slog.Warn("skills: "+msg, args...) }

// Load builds a dive skill.Loader over cfg.Dirs ∪ extraDirs. Disabled config
// returns an empty (but non-nil) Loader so the catalog-cleanup hook still
// runs on resumed sessions. Per-skill / per-provider errors are logged and
// skipped; an aggregate error returns only if every provider failed.
//
// cax treats each configured directory as a direct skill root (containing
// <name>/SKILL.md entries), not as a project/home root. That keeps defaults
// like ".dive/skills" / "~/.dive/skills" meaningful and prevents dive's
// DefaultFilesystemProvider from re-prefixing them.
func Load(cfg config.SkillsConfig, extraDirs []string) (*LoadResult, error) {
	if !cfg.Enabled {
		loader := skill.NewLoader(skill.LoaderOptions{Logger: logBridge{}})
		return &LoadResult{Loader: loader, Names: nil}, nil
	}

	paths := make([]string, 0, len(cfg.Dirs)+len(extraDirs))
	for _, d := range cfg.Dirs {
		if d != "" {
			paths = append(paths, d)
		}
	}
	for _, d := range extraDirs {
		if d != "" {
			paths = append(paths, d)
		}
	}

	provider := skill.NewFilesystemProvider(skill.FilesystemOptions{
		Paths:  paths,
		Logger: logBridge{},
	})

	loader, err := skill.Load(context.Background(), skill.LoaderOptions{
		Providers: []skill.Provider{provider},
		Logger:    logBridge{},
	})
	if err != nil {
		return nil, fmt.Errorf("skills: load: %w", err)
	}
	return &LoadResult{Loader: loader, Names: loader.Names()}, nil
}
