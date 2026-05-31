// Package projectroot resolves "which project is the user currently working
// in?" dynamically — no config required. The agent calls Resolver.For(query)
// before every turn-context lookup (currently: code_search injection). The
// resolved path then flows to ken, bash, sub-agents, anything else that
// needs to know the active root.
//
// Resolution order (first non-empty wins):
//  1. Explicit override set by the /cwd command — sticky until cleared.
//  2. Absolute path detected in the user's query (e.g. the user typed
//     "/Users/cq/Dev/Pythia/clob-engine" — we walk up from there).
//  3. Walk up from the process launch CWD looking for repo markers
//     (.git, go.mod, package.json, pyproject.toml, Cargo.toml, pom.xml,
//     build.gradle, deno.json).
//  4. Empty string — caller decides what to do (typically: skip the lookup).
package projectroot

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// rootMarkers are files/dirs that signal "this is a project root". Listed in
// no particular priority order — the WALK direction (innermost-first) decides.
var rootMarkers = []string{
	".git",
	"go.mod",
	"package.json",
	"pyproject.toml",
	"Cargo.toml",
	"pom.xml",
	"build.gradle",
	"build.gradle.kts",
	"deno.json",
	"Pipfile",
	"composer.json",
	"Gemfile",
}

// pathInQuery matches absolute POSIX paths in user text. Limits to chars
// reasonable for file paths so a regex isn't tricked into capturing prose.
// We don't try to detect relative paths — too many false positives.
var pathInQuery = regexp.MustCompile(`(?:^|[\s\x60'"(\[{])(/[A-Za-z0-9._/~+-]+)`)

// Resolver is process-shared mutable state: the agent reads on every turn,
// the /cwd command writes. Safe under concurrent use.
type Resolver struct {
	mu        sync.RWMutex
	override  string // sticky cwd from /cwd; "" when unset
	launchCWD string // captured at construction so later os.Chdir() can't move it
}

// New captures os.Getwd() as the launch CWD. Failures fall back to "" so
// resolution simply ignores the walk-up signal in that case.
func New() *Resolver {
	cwd, _ := os.Getwd()
	return &Resolver{launchCWD: cwd}
}

// NewWithLaunchCWD lets tests construct a resolver pinned to a specific
// directory without changing the process cwd.
func NewWithLaunchCWD(cwd string) *Resolver {
	return &Resolver{launchCWD: cwd}
}

// SetOverride pins the active project root. The path is validated (must
// exist and be a directory). Tilde expansion + cleaning applied. Returns
// the cleaned path actually stored.
func (r *Resolver) SetOverride(path string) (string, error) {
	clean, err := canonicalize(path)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	r.override = clean
	r.mu.Unlock()
	return clean, nil
}

// ClearOverride removes the sticky cwd. Resolution falls back to query
// detection / walk-up.
func (r *Resolver) ClearOverride() {
	r.mu.Lock()
	r.override = ""
	r.mu.Unlock()
}

// Override returns the current sticky cwd ("" when unset). For UI display.
func (r *Resolver) Override() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.override
}

// For resolves the active project root for the given query. See package
// docs for the resolution order. Returns "" only when every signal fails.
func (r *Resolver) For(query string) string {
	r.mu.RLock()
	override := r.override
	launch := r.launchCWD
	r.mu.RUnlock()

	if override != "" {
		return override
	}
	if p := detectPathInQuery(query); p != "" {
		if root := WalkUp(p); root != "" {
			return root
		}
		// Detected a path but no marker — return the path itself if it's a
		// directory; otherwise its parent. This handles "explore /tmp/scratch".
		if fi, err := os.Stat(p); err == nil {
			if fi.IsDir() {
				return p
			}
			return filepath.Dir(p)
		}
	}
	if launch != "" {
		if root := WalkUp(launch); root != "" {
			return root
		}
		return launch
	}
	return ""
}

// WalkUp searches start and each ancestor for any of rootMarkers. Returns
// the first directory containing a marker, or "" if none found before "/".
// Stops at the filesystem root or at the home directory (whichever is
// shallower) — avoids walking into /etc or /Users when start is unusual.
func WalkUp(start string) string {
	start = strings.TrimSpace(start)
	if start == "" {
		return ""
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	// If start is a file, begin at its directory.
	if fi, err := os.Stat(abs); err == nil && !fi.IsDir() {
		abs = filepath.Dir(abs)
	}
	home, _ := os.UserHomeDir()
	dir := abs
	for {
		for _, m := range rootMarkers {
			if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir { // hit filesystem root
			return ""
		}
		if home != "" && dir == home { // don't escape above $HOME
			return ""
		}
		dir = parent
	}
}

// detectPathInQuery returns the first absolute path mentioned in q that
// resolves to something on disk. "" when no candidate is found. We pick the
// LONGEST match to prefer more specific paths.
func detectPathInQuery(q string) string {
	if q == "" {
		return ""
	}
	matches := pathInQuery.FindAllStringSubmatch(q, -1)
	if len(matches) == 0 {
		return ""
	}
	cands := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		p := expandTilde(strings.TrimRight(m[1], ".,;:)"))
		if _, err := os.Stat(p); err == nil {
			cands = append(cands, p)
		}
	}
	if len(cands) == 0 {
		return ""
	}
	// Longest path wins — most specific.
	sort.Slice(cands, func(i, j int) bool { return len(cands[i]) > len(cands[j]) })
	return cands[0]
}

// canonicalize tilde-expands and Abs+Clean's a user-supplied path; rejects
// non-existent paths and non-directories.
func canonicalize(p string) (string, error) {
	p = expandTilde(strings.TrimSpace(p))
	if p == "" {
		return "", os.ErrInvalid
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	fi, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", os.ErrInvalid
	}
	return abs, nil
}

func expandTilde(p string) string {
	if p == "" || (p != "~" && !strings.HasPrefix(p, "~/")) {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}
