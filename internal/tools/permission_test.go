package tools

import (
	"bytes"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/dive"
)

func TestConfirmDialog_AutoApprove(t *testing.T) {
	d := ConfirmDialog(false, strings.NewReader(""), &bytes.Buffer{})
	out, err := d.Show(t.Context(), &dive.DialogInput{Confirm: true, Title: "Bash"})
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !out.Confirmed {
		t.Fatal("expected auto-approve when requireConfirm=false")
	}
}

func TestConfirmDialog_PromptYes(t *testing.T) {
	var out bytes.Buffer
	d := ConfirmDialog(true, strings.NewReader("y\n"), &out)
	res, err := d.Show(t.Context(), &dive.DialogInput{Confirm: true, Title: "Bash", Message: "rm -rf /tmp/x"})
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !res.Confirmed {
		t.Fatal("expected confirmed on 'y'")
	}
	if !strings.Contains(out.String(), "rm -rf /tmp/x") {
		t.Fatalf("prompt missing message: %q", out.String())
	}
}

func TestConfirmDialog_PromptNo(t *testing.T) {
	d := ConfirmDialog(true, strings.NewReader("n\n"), &bytes.Buffer{})
	res, err := d.Show(t.Context(), &dive.DialogInput{Confirm: true, Title: "Write"})
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if res.Confirmed {
		t.Fatal("expected not confirmed on 'n'")
	}
}

func TestConfirmDialog_EOFDenies(t *testing.T) {
	d := ConfirmDialog(true, strings.NewReader(""), &bytes.Buffer{})
	res, err := d.Show(t.Context(), &dive.DialogInput{Confirm: true, Title: "Bash"})
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if res.Confirmed {
		t.Fatal("expected denial on EOF/no input")
	}
}
