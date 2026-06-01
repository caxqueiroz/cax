package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caxqueiroz/cax/internal/config"
	"github.com/caxqueiroz/cax/internal/workspace"
)

// codeSearchMaxBytes caps how much of the command's stdout we forward into
// the prompt per repo. Large rankers can emit dozens of chunks; we'd rather
// truncate than let one bad query blow the context window.
const codeSearchMaxBytes = 16 * 1024

// codeSearchMergedCap is the across-repo cap on the merged result when fan
// out targets multiple repos. Sized for monorepo/multi-repo workspaces —
// 64 chunks × ~150B/chunk ≈ 10 KB, well under the prompt's tolerance and
// enough that a 13-service Pythia-style fan-out keeps ~5 hits per service
// after merging. Without this, a runaway query could blow the context.
// Without a high enough value, the model can't see enough cross-service
// context to skip its own Grep/Read exploration — defeating ken entirely.
const codeSearchMergedCap = 64

// rankedHit is one row parsed from ken's output. score is the numeric prefix;
// body is the rest of the line ("file:line-line  snippet"). repo is the
// workspace entry the hit came from — used to prefix the merged output so
// the LLM knows which service each chunk lives in.
type rankedHit struct {
	score float64
	body  string
	repo  string
}

// runCodeSearch executes cfg.Command for each target with cfg.Args
// substituted ({QUERY} → user query, {REPO} → target.Path). When len(targets)
// > 1, every target runs concurrently and the union is merged + re-ranked
// by score, top-codeSearchMergedCap kept.
//
// When targets is empty the function returns ("", nil) — code_search is
// effectively disabled until the workspace has at least one entry.
//
// Substitution is per-argv-slot, NOT shell-expanded — the query never goes
// through a shell, so quotes / spaces / metacharacters in user input are safe.
func runCodeSearch(ctx context.Context, cfg config.CodeSearchConfig, query string, targets []workspace.Entry) (string, error) {
	if !cfg.Enabled() {
		return "", nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return "", nil
	}
	if len(targets) == 0 {
		return "", nil
	}

	timeout := time.Duration(cfg.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	// Per-target slot in the results slice — index aligns with targets, so
	// goroutines write without contention.
	results := make([][]rankedHit, len(targets))
	errs := make([]error, len(targets))

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Go(func() {
			hits, err := runOne(cctx, cfg, query, t)
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = hits
		})
	}
	wg.Wait()

	// Merge: any non-nil error gets logged via the first non-nil errs in the
	// caller; we still emit whatever hits succeeded so a single broken index
	// can't blank-out the whole fan-out.
	var merged []rankedHit
	for _, hs := range results {
		merged = append(merged, hs...)
	}
	if len(merged) == 0 {
		// Collapse to first error if every target failed.
		for _, e := range errs {
			if e != nil {
				return "", e
			}
		}
		return "", nil
	}

	// Sort by descending score, cap.
	slices.SortFunc(merged, func(a, b rankedHit) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		default:
			return 0
		}
	})
	if len(merged) > codeSearchMergedCap {
		merged = merged[:codeSearchMergedCap]
	}

	var out strings.Builder
	maxRepoLen := 0
	for _, h := range merged {
		if len(h.repo) > maxRepoLen {
			maxRepoLen = len(h.repo)
		}
	}
	for _, h := range merged {
		fmt.Fprintf(&out, "[%-*s] %s\n", maxRepoLen, h.repo, h.body)
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// runOne invokes cfg.Command for a single repo and parses its output into
// ranked hits.
func runOne(ctx context.Context, cfg config.CodeSearchConfig, query string, t workspace.Entry) ([]rankedHit, error) {
	repo := t.Path
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

	cmd := exec.CommandContext(ctx, cfg.Command, args...)
	cmd.Dir = repo
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("code_search: %s @ %s: %s", cfg.Command, t.Name, msg)
	}

	raw := stdout.Bytes()
	if len(raw) > codeSearchMaxBytes {
		raw = raw[:codeSearchMaxBytes]
	}
	return parseRankedHits(string(raw), t.Name), nil
}

// parseRankedHits walks the command output line by line, splits on the first
// whitespace run, and tries to parse the leading token as a float (the
// score). Lines that don't parse are kept with score 0 so unknown rankers
// still surface — they'll just sort to the bottom.
func parseRankedHits(text, repoName string) []rankedHit {
	var out []rankedHit
	for line := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
		if line == "" {
			continue
		}
		// Find first whitespace.
		idx := strings.IndexFunc(line, func(r rune) bool { return r == ' ' || r == '\t' })
		var score float64
		var body string
		if idx > 0 {
			if v, err := strconv.ParseFloat(strings.TrimSpace(line[:idx]), 64); err == nil {
				score = v
				body = strings.TrimSpace(line[idx:])
			} else {
				body = line
			}
		} else {
			body = line
		}
		out = append(out, rankedHit{score: score, body: body, repo: repoName})
	}
	return out
}
