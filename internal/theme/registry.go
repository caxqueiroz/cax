package theme

import (
	"fmt"
	"sort"
	"sync"
)

var (
	mu       sync.RWMutex
	registry = map[string]*Theme{}
	order    []string // insertion order, drives List() and Cycle()
	active   *Theme
)

// Register adds or replaces a theme in the registry. Empty names are ignored.
func Register(t *Theme) {
	if t == nil || t.Name == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if _, exists := registry[t.Name]; !exists {
		order = append(order, t.Name)
	}
	registry[t.Name] = t
}

// List returns theme names in registration order. Built-ins are loaded first
// (alphabetised by LoadBuiltins for stability), user themes after.
func List() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, len(order))
	copy(out, order)
	return out
}

// Get returns a theme by name.
func Get(name string) (*Theme, error) {
	mu.RLock()
	defer mu.RUnlock()
	t, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("theme %q not found", name)
	}
	return t, nil
}

// Active returns the currently active theme, or nil if Set has never been
// called. View code should fall back to a sentinel when nil.
func Active() *Theme {
	mu.RLock()
	defer mu.RUnlock()
	return active
}

// Set makes t the active theme. Subsequent Active() returns t. Safe for
// concurrent reads.
func Set(t *Theme) {
	mu.Lock()
	defer mu.Unlock()
	active = t
}

// Cycle advances the active theme to the next name in registration order,
// wrapping at the end. If nothing is active yet it picks the first. Returns
// the new active theme, or nil if the registry is empty.
func Cycle() *Theme {
	mu.Lock()
	defer mu.Unlock()
	if len(order) == 0 {
		return nil
	}
	if active == nil {
		active = registry[order[0]]
		return active
	}
	for i, n := range order {
		if n == active.Name {
			next := order[(i+1)%len(order)]
			active = registry[next]
			return active
		}
	}
	active = registry[order[0]]
	return active
}

// reset clears the registry. Test-only.
func reset() {
	mu.Lock()
	defer mu.Unlock()
	registry = map[string]*Theme{}
	order = nil
	active = nil
}

// sortNames returns names sorted alphabetically. Used by LoadBuiltins to
// give a stable cross-platform ordering before user themes are appended.
func sortNames(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	sort.Strings(out)
	return out
}
