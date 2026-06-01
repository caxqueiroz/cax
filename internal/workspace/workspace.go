// Package workspace tracks the set of project roots the agent should
// consider for cross-cutting queries. A workspace is a list of Entry values;
// each entry is one project root (a single service in a monorepo, or a
// standalone repo). The agent uses this list to fan code-search out across
// all relevant trees in parallel so cross-service queries ("how does A talk
// to B?") naturally pull chunks from every matching project.
//
// Discovery (Discover) walks up from a start directory until it finds a
// .git dir, then scans the immediate children of that root for child
// projects. A child is a directory containing any of rootMarkers (.git/
// go.mod/package.json/etc). This catches the common monorepo layout
// (Pythia/clob-engine, Pythia/liquidity-provider, ...) without requiring
// any config.
//
// State is persisted to ~/.cax/workspace.json so the workspace survives
// restart. Manual Add/Remove edit the in-memory state and flush.
package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// rootMarkers signal "this directory is a project root". Mirrored from
// internal/projectroot so the two stay aligned without forcing a dep.
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

// Entry is one project root in the workspace. Name defaults to the basename
// of Path; explicit names let users alias long paths in /workspace output.
type Entry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// state is the JSON shape persisted to disk.
type state struct {
	Entries []Entry `json:"entries"`
}

// Workspace is the in-memory + on-disk list of project roots. Safe under
// concurrent reads/writes (the CLI mutates from /workspace handlers; the
// agent reads on every PreGeneration).
type Workspace struct {
	mu        sync.RWMutex
	statePath string
	entries   []Entry
}

// New loads from statePath if it exists; missing file → empty workspace.
// Pass "" to skip persistence (tests).
func New(statePath string) (*Workspace, error) {
	w := &Workspace{statePath: statePath}
	if statePath == "" {
		return w, nil
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return w, nil
		}
		return nil, fmt.Errorf("workspace: read %s: %w", statePath, err)
	}
	var s state
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("workspace: parse %s: %w", statePath, err)
	}
	w.entries = s.Entries
	return w, nil
}

// Entries returns a snapshot of the current list.
func (w *Workspace) Entries() []Entry {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return slices.Clone(w.entries)
}

// Paths returns just the resolved paths of every entry — convenient for
// fan-out callers that don't care about names.
func (w *Workspace) Paths() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]string, len(w.entries))
	for i, e := range w.entries {
		out[i] = e.Path
	}
	return out
}

// Find returns the entry with the given name or path, or nil if none.
// Matches against both Name and Path so the same lookup works whether the
// caller has a user-supplied label or a fully-resolved path.
func (w *Workspace) Find(nameOrPath string) *Entry {
	q := strings.TrimSpace(nameOrPath)
	if q == "" {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	for i := range w.entries {
		e := &w.entries[i]
		if e.Name == q || e.Path == q {
			return e
		}
	}
	return nil
}

// MatchingNames returns the subset of entries whose Name is a substring of
// query (case-insensitive). Used by the agent to NARROW fan-out to specific
// services when the user names them in their prompt.
//
// Example: workspace = [clob-engine, liquidity-provider, risk-engine] and
// query = "how does clob-engine route to risk-engine?" returns the two
// matching entries. Query with no service-name match returns nil — caller
// then fans out across the full workspace.
func (w *Workspace) MatchingNames(query string) []Entry {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	var out []Entry
	for _, e := range w.entries {
		name := strings.ToLower(e.Name)
		if name == "" {
			continue
		}
		// Require a word-boundary-ish match so "engine" in "clob-engine"
		// doesn't match every "engine" in unrelated prose. Cheap heuristic:
		// the name must appear surrounded by non-letter chars or at
		// start/end.
		if containsWord(q, name) {
			out = append(out, e)
		}
	}
	return out
}

// Add appends a new entry, canonicalising the path. Duplicate paths are
// rejected. Auto-derives Name from basename when not provided.
func (w *Workspace) Add(name, path string) (Entry, error) {
	clean, err := canonicalize(path)
	if err != nil {
		return Entry{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, e := range w.entries {
		if e.Path == clean {
			return Entry{}, fmt.Errorf("workspace: %s already added (name %q)", clean, e.Name)
		}
	}
	if name == "" {
		name = filepath.Base(clean)
	}
	entry := Entry{Name: name, Path: clean}
	w.entries = append(w.entries, entry)
	return entry, w.flushLocked()
}

// Remove drops the entry matching name or path. Returns the removed entry,
// or zero value + error when nothing matches.
func (w *Workspace) Remove(nameOrPath string) (Entry, error) {
	q := strings.TrimSpace(nameOrPath)
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, e := range w.entries {
		if e.Name == q || e.Path == q {
			w.entries = slices.Delete(w.entries, i, i+1)
			return e, w.flushLocked()
		}
	}
	return Entry{}, fmt.Errorf("workspace: no entry matches %q", q)
}

// minProjectChildren is the threshold for declaring a directory "the
// workspace root". Two child projects is enough — most monorepo / multi-
// repo layouts have at least that, and accidental hits (a dir that happens
// to contain a couple of cloned repos) are rare and harmless.
const minProjectChildren = 2

// Discover walks UP from start looking for the innermost ancestor that
// has at least minProjectChildren child directories carrying project
// markers (.git/go.mod/package.json/etc). That parent becomes the
// workspace root and its project-marker children are the entries.
//
// This handles three layouts identically:
//
//  1. Monorepo with one parent .git (e.g. a Go module repo whose subdirs
//     host services): cd in anywhere, walks up until 2+ children carry
//     markers.
//  2. Multi-repo parent dir (e.g. ~/Dev/Pythia where each service has
//     its own .git but the parent does not): scans the parent and finds
//     2+ children with .git directly under them.
//  3. Single-project dir (e.g. plain Go repo): no ancestor has 2+ child
//     projects → returns ("", nil, nil). The CLI surfaces a hint pointing
//     at /workspace add for explicit setup.
//
// Walk-up stops at $HOME (no escape into /etc/Users/etc). Found children
// are NOT added — caller decides whether to call Add per entry.
func Discover(start string) (root string, children []Entry, err error) {
	start = strings.TrimSpace(start)
	if start == "" {
		start, _ = os.Getwd()
	}
	if start == "" {
		return "", nil, nil
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", nil, fmt.Errorf("workspace: discover %s: %w", start, err)
	}

	home, _ := os.UserHomeDir()
	dir := abs
	for {
		kids := scanProjectChildren(dir)
		if len(kids) >= minProjectChildren {
			return dir, kids, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil, nil
		}
		if home != "" && dir == home { // stop at HOME boundary
			return "", nil, nil
		}
		dir = parent
	}
}

// scanProjectChildren returns the immediate children of dir that carry a
// project marker (.git/go.mod/package.json/etc). Hidden dirs (".foo") are
// skipped. Returns nil on read errors — discovery just keeps climbing.
func scanProjectChildren(dir string) []Entry {
	rd, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Entry
	for _, entry := range rd {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		child := filepath.Join(dir, name)
		if hasAnyMarker(child) {
			out = append(out, Entry{Name: name, Path: child})
		}
	}
	return out
}

// hasAnyMarker reports whether dir contains at least one of rootMarkers.
func hasAnyMarker(dir string) bool {
	for _, m := range rootMarkers {
		if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
			return true
		}
	}
	return false
}

// containsWord reports whether name appears in text bounded by non-letter
// chars (or string edges). Avoids "engine" matching mid-word.
func containsWord(text, name string) bool {
	idx := 0
	for {
		i := strings.Index(text[idx:], name)
		if i < 0 {
			return false
		}
		i += idx
		ok := true
		if i > 0 {
			c := text[i-1]
			if isWordRune(c) {
				ok = false
			}
		}
		if ok {
			end := i + len(name)
			if end < len(text) {
				c := text[end]
				if isWordRune(c) {
					ok = false
				}
			}
		}
		if ok {
			return true
		}
		idx = i + 1
		if idx >= len(text) {
			return false
		}
	}
}

func isWordRune(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
}

// canonicalize validates the path exists, expands ~, and resolves to abs+clean.
func canonicalize(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("workspace: empty path")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if p == "~" {
				p = home
			} else {
				p = filepath.Join(home, p[2:])
			}
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("workspace: abs %s: %w", p, err)
	}
	abs = filepath.Clean(abs)
	fi, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("workspace: stat %s: %w", abs, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("workspace: %s is not a directory", abs)
	}
	return abs, nil
}

// flushLocked writes the current state to disk. Caller holds mu.
func (w *Workspace) flushLocked() error {
	if w.statePath == "" {
		return nil
	}
	data, err := json.MarshalIndent(state{Entries: w.entries}, "", "  ")
	if err != nil {
		return fmt.Errorf("workspace: marshal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(w.statePath), 0o700); err != nil {
		return fmt.Errorf("workspace: mkdir %s: %w", filepath.Dir(w.statePath), err)
	}
	if err := os.WriteFile(w.statePath, data, 0o600); err != nil {
		return fmt.Errorf("workspace: write %s: %w", w.statePath, err)
	}
	return nil
}
