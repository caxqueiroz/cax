// Command czcli runs the assistant over a simple stdin/stdout loop. Plan 4
// replaces this entrypoint with a Bubble Tea TUI.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/caxqueiroz/czcli/internal/agent"
	"github.com/caxqueiroz/czcli/internal/channel"
	"github.com/caxqueiroz/czcli/internal/config"
	"github.com/caxqueiroz/czcli/internal/memory"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "czcli: %v\n", err)
		os.Exit(1)
	}
}

// run loads config, wires dependencies, and starts the read loop.
func run(ctx context.Context) error {
	path := os.Getenv("CZCLI_CONFIG")
	if path == "" {
		path = "config.yaml"
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("load config from %q (set CZCLI_CONFIG to override): %w", path, err)
	}

	embedder, err := memory.NewEmbedder(cfg.Embeddings)
	if err != nil {
		return fmt.Errorf("build embedder: %w", err)
	}

	store, err := memory.Open(cfg.Memory, embedder)
	if err != nil {
		return fmt.Errorf("open memory store: %w", err)
	}
	defer func() {
		if cerr := store.Close(); cerr != nil {
			slog.Warn("close store", "err", cerr)
		}
	}()

	model, err := agent.BuildModel(cfg)
	if err != nil {
		return fmt.Errorf("build model: %w", err)
	}

	assistant, err := agent.Build(ctx, cfg, store, model)
	if err != nil {
		return fmt.Errorf("build assistant: %w", err)
	}

	fmt.Fprintln(os.Stdout, "czcli ready. Type a message (Ctrl-D to exit).")
	return readLoop(ctx, "cli", os.Stdin, os.Stdout, assistant.Handle)
}

// readLoop reads one line per turn, runs it through handle, streams text deltas
// inline, and prints the final reply. It returns nil on EOF.
func readLoop(ctx context.Context, sessionID string, in io.Reader, out io.Writer, handle channel.Handler) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for {
		fmt.Fprint(out, "> ")
		if !scanner.Scan() {
			break
		}
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}

		emit := func(ev channel.StreamEvent) {
			switch ev.Type {
			case "text":
				fmt.Fprint(out, ev.Text)
			case "tool_start":
				fmt.Fprintf(out, "\n[tool: %s]\n", ev.Text)
			case "subagent_start":
				fmt.Fprintf(out, "\n[subagent: %s]\n", ev.Text)
			case "error":
				fmt.Fprintf(out, "\n[error: %s]\n", ev.Text)
			}
		}

		if _, err := handle(ctx, channel.Message{SessionID: sessionID, Text: text}, emit); err != nil {
			fmt.Fprintf(out, "\nerror: %v\n", err)
			continue
		}
		// Ensure a clean line after streamed deltas.
		fmt.Fprintln(out)
	}
	return scanner.Err()
}
