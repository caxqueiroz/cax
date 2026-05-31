package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/caxqueiroz/cax/internal/config"
)

// codeSearchMaxBytes caps how much of the command's stdout we forward into
// the prompt. Large rankers can emit dozens of file chunks; we'd rather
// truncate than let one bad query blow the context window.
const codeSearchMaxBytes = 16 * 1024

// runCodeSearch executes cfg.Command with cfg.Args, substituting "{QUERY}"
// and "{REPO}" placeholders. Returns the stdout (truncated to
// codeSearchMaxBytes) or an error. Stderr is collected for the error message
// only; never injected into the prompt.
//
// Substitution is per-argv-slot, NOT shell-expanded — the query never goes
// through a shell, so quotes / spaces / metacharacters in user input are safe.
//
// repo is the dynamically-resolved project root (from projectroot.Resolver).
// When empty, falls back to cfg.RepoRoot, then to os.Getwd().
func runCodeSearch(ctx context.Context, cfg config.CodeSearchConfig, query, repo string) (string, error) {
	if !cfg.Enabled() {
		return "", nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return "", nil
	}

	repo = strings.TrimSpace(repo)
	if repo == "" {
		repo = strings.TrimSpace(cfg.RepoRoot)
	}
	if repo == "" {
		if cwd, err := os.Getwd(); err == nil {
			repo = cwd
		} else {
			repo = "."
		}
	}
	if strings.HasPrefix(repo, "~/") || repo == "~" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if repo == "~" {
				repo = home
			} else {
				repo = filepath.Join(home, repo[2:])
			}
		}
	}

	args := make([]string, 0, len(cfg.Args))
	for _, a := range cfg.Args {
		a = strings.ReplaceAll(a, "{QUERY}", query)
		a = strings.ReplaceAll(a, "{REPO}", repo)
		args = append(args, a)
	}

	timeout := time.Duration(cfg.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, cfg.Command, args...)
	cmd.Dir = repo
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("code_search: %s: %s", cfg.Command, msg)
	}

	out := stdout.Bytes()
	if len(out) > codeSearchMaxBytes {
		out = append(out[:codeSearchMaxBytes], []byte("\n...[truncated]")...)
	}
	return strings.TrimRight(string(out), "\n"), nil
}
