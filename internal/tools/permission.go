// Package tools assembles the dive built-in tools czcli exposes, plus the
// search_memory recall tool and the CLI permission dialog.
package tools

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/deepnoodle-ai/dive"
)

// confirmDialog implements dive.Dialog. When requireConfirm is false it
// auto-approves every confirm prompt; otherwise it prompts on out and reads a
// yes/no answer from in. Non-confirm dialogs (select/input) are not used by the
// permission gate, so they are answered with safe defaults.
type confirmDialog struct {
	requireConfirm bool
	in             io.Reader
	out            io.Writer
}

var _ dive.Dialog = (*confirmDialog)(nil)

// ConfirmDialog returns a dive.Dialog that prompts on stdin/stdout (or
// auto-approves when requireConfirm is false).
func ConfirmDialog(requireConfirm bool, in io.Reader, out io.Writer) dive.Dialog {
	return &confirmDialog{requireConfirm: requireConfirm, in: in, out: out}
}

func (d *confirmDialog) Show(_ context.Context, in *dive.DialogInput) (*dive.DialogOutput, error) {
	if !d.requireConfirm {
		if in.Confirm {
			return &dive.DialogOutput{Confirmed: true}, nil
		}
		return &dive.DialogOutput{Text: in.Default}, nil
	}
	if !in.Confirm {
		// The permission gate only issues confirm prompts; default-allow others.
		return &dive.DialogOutput{Text: in.Default}, nil
	}

	title := in.Title
	if title == "" {
		title = "tool"
	}
	if in.Message != "" {
		_, _ = fmt.Fprintf(d.out, "\n[permission] %s wants to run:\n  %s\n", title, in.Message)
	} else {
		_, _ = fmt.Fprintf(d.out, "\n[permission] allow %s?\n", title)
	}
	_, _ = fmt.Fprint(d.out, "Allow? [y/N]: ")

	reader := bufio.NewReader(d.in)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		// EOF / no input -> deny by default.
		return &dive.DialogOutput{Confirmed: false}, nil
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	confirmed := answer == "y" || answer == "yes"
	return &dive.DialogOutput{Confirmed: confirmed}, nil
}
