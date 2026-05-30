// Package creator implements the "just-ask" workflow for creating skills,
// agents, and slash commands. Writer materializes Claude-Code-compatible
// markdown files under cax's namespaced directories; Tools wraps the three
// writers in dive FuncTools so the agent can call them in response to a
// natural-language request. After every successful write the package calls
// Reloader.Rebuild so the running agent picks up the new contribution on the
// next turn without restart.
package creator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// nameRe enforces the contract in 02-tui-redesign-contracts.md §Creator:
// lower-kebab, 1-64 chars, must start with [a-z0-9].
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// validateName rejects anything outside the contract regex plus belt-and-
// suspenders rejection of path-separators, parent-dir traversal and dot
// segments. (The regex already excludes '/', '\', '.', but we keep the
// explicit checks so any future regex change doesn't silently open a hole.)
func validateName(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid name %q: must match %s", name, nameRe.String())
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("invalid name %q: contains path separator", name)
	}
	clean := filepath.Clean(name)
	if strings.Contains(clean, "..") || clean != name {
		return fmt.Errorf("invalid name %q: path traversal not allowed", name)
	}
	return nil
}

// Writer materializes files under cax's namespaced directories. The three
// directory paths are caller-resolved (HOME-expanded) so the package itself
// performs no I/O outside of the configured roots.
type Writer struct {
	SkillsDir   string
	AgentsDir   string
	CommandsDir string
}

// WriteSkill writes <SkillsDir>/<name>/SKILL.md with the standard dive skill
// frontmatter (name + description). Returns the absolute path written.
// Errors if the file already exists and overwrite is false.
func (w Writer) WriteSkill(name, description, body string, overwrite bool) (string, error) {
	if err := validateName(name); err != nil {
		return "", fmt.Errorf("WriteSkill: %w", err)
	}
	if w.SkillsDir == "" {
		return "", errors.New("WriteSkill: SkillsDir not configured")
	}
	dir := filepath.Join(w.SkillsDir, name)
	path := filepath.Join(dir, "SKILL.md")
	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "name: %s\n", name)
	fmt.Fprintf(&sb, "description: %s\n", oneLine(description))
	sb.WriteString("---\n\n")
	sb.WriteString(ensureTrailingNewline(body))
	if err := writeAtomic(dir, path, []byte(sb.String()), overwrite); err != nil {
		return "", fmt.Errorf("WriteSkill: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path, nil
	}
	return abs, nil
}

// WriteAgent writes <AgentsDir>/<name>.md with `description:`, optional
// `tools:` and optional `disallowed-tools:` keys plus the markdown body.
// Returns the absolute path written.
func (w Writer) WriteAgent(name, description string, tools, disallowedTools []string, body string, overwrite bool) (string, error) {
	if err := validateName(name); err != nil {
		return "", fmt.Errorf("WriteAgent: %w", err)
	}
	if w.AgentsDir == "" {
		return "", errors.New("WriteAgent: AgentsDir not configured")
	}
	path := filepath.Join(w.AgentsDir, name+".md")
	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "description: %s\n", oneLine(description))
	if len(tools) > 0 {
		sb.WriteString("tools:\n")
		for _, t := range tools {
			fmt.Fprintf(&sb, "  - %s\n", t)
		}
	}
	if len(disallowedTools) > 0 {
		sb.WriteString("disallowed-tools:\n")
		for _, t := range disallowedTools {
			fmt.Fprintf(&sb, "  - %s\n", t)
		}
	}
	sb.WriteString("---\n\n")
	sb.WriteString(ensureTrailingNewline(body))
	if err := writeAtomic(w.AgentsDir, path, []byte(sb.String()), overwrite); err != nil {
		return "", fmt.Errorf("WriteAgent: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path, nil
	}
	return abs, nil
}

// WriteCommand writes <CommandsDir>/<name>.md with `description:` and optional
// `argument-hint:` keys. The body may include the literal $ARGUMENTS token
// which the CLI command dispatcher expands at invocation. Returns the
// absolute path written.
func (w Writer) WriteCommand(name, description, argumentHint, body string, overwrite bool) (string, error) {
	if err := validateName(name); err != nil {
		return "", fmt.Errorf("WriteCommand: %w", err)
	}
	if w.CommandsDir == "" {
		return "", errors.New("WriteCommand: CommandsDir not configured")
	}
	path := filepath.Join(w.CommandsDir, name+".md")
	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "description: %s\n", oneLine(description))
	if strings.TrimSpace(argumentHint) != "" {
		fmt.Fprintf(&sb, "argument-hint: %s\n", oneLine(argumentHint))
	}
	sb.WriteString("---\n\n")
	sb.WriteString(ensureTrailingNewline(body))
	if err := writeAtomic(w.CommandsDir, path, []byte(sb.String()), overwrite); err != nil {
		return "", fmt.Errorf("WriteCommand: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path, nil
	}
	return abs, nil
}

// writeAtomic mirrors internal/plugins/state.go writeState: create parent dir
// (0755), write to a temp file in the same dir, rename over the target. If
// overwrite is false and the target already exists, returns "already exists".
func writeAtomic(dir, path string, data []byte, overwrite bool) error {
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("file %s already exists (pass overwrite=true to replace)", path)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".creator.*.md")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename to %s: %w", path, err)
	}
	return nil
}

// oneLine collapses interior newlines so a multi-line description doesn't
// blow up the single-line YAML scalar. Leading/trailing whitespace trimmed.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// ensureTrailingNewline keeps file endings POSIX-correct so editors and the
// plugins parser don't choke on a missing terminal newline.
func ensureTrailingNewline(s string) string {
	if s == "" {
		return ""
	}
	if !strings.HasSuffix(s, "\n") {
		return s + "\n"
	}
	return s
}
