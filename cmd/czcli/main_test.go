package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/caxqueiroz/czcli/internal/channel"
)

// fakeHandler stands in for the assistant in the loop test.
func fakeHandler(_ context.Context, msg channel.Message, emit channel.EventSink) (channel.Reply, error) {
	emit(channel.StreamEvent{Type: "text", Text: "echo: " + msg.Text})
	return channel.Reply{Text: "echo: " + msg.Text}, nil
}

func TestReadLoop_EchoesReplies(t *testing.T) {
	in := strings.NewReader("hello\nbye\n")
	var out bytes.Buffer
	if err := readLoop(context.Background(), "sess", in, &out, fakeHandler); err != nil {
		t.Fatalf("readLoop: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "echo: hello") {
		t.Fatalf("missing first reply: %q", got)
	}
	if !strings.Contains(got, "echo: bye") {
		t.Fatalf("missing second reply: %q", got)
	}
}

func TestReadLoop_SkipsBlankLines(t *testing.T) {
	in := strings.NewReader("\n   \nhi\n")
	var out bytes.Buffer
	if err := readLoop(context.Background(), "sess", in, &out, fakeHandler); err != nil {
		t.Fatalf("readLoop: %v", err)
	}
	if !strings.Contains(out.String(), "echo: hi") {
		t.Fatalf("expected echo: hi, got %q", out.String())
	}
}
