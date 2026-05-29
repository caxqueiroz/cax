package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/deepnoodle-ai/dive/llm"
)

// scriptStream replays a fixed slice of events as an llm.StreamIterator.
type scriptStream struct {
	events []*llm.Event
	i      int
}

func (s *scriptStream) Next() bool {
	if s.i >= len(s.events) {
		return false
	}
	s.i++
	return true
}
func (s *scriptStream) Event() *llm.Event { return s.events[s.i-1] }
func (s *scriptStream) Err() error        { return nil }
func (s *scriptStream) Close() error      { return nil }

// scriptLLM is a deterministic llm.StreamingLLM. It returns replyText as a
// single assistant message and reports usage. It records the system prompt and
// messages it last saw so tests can assert what the hooks injected. It is
// distinct from model_test.go's fakeLLM, which is tailored to fallback tests.
type scriptLLM struct {
	mu         sync.Mutex
	replyText  string
	usage      *llm.Usage
	lastSystem string
	lastMsgs   []*llm.Message
}

func newScriptLLM(reply string) *scriptLLM {
	return &scriptLLM{
		replyText: reply,
		usage:     &llm.Usage{InputTokens: 7, OutputTokens: 11},
	}
}

func (f *scriptLLM) Name() string { return "fake" }

func (f *scriptLLM) capture(opts []llm.Option) {
	var cfg llm.Config
	cfg.Apply(opts...)
	f.mu.Lock()
	f.lastSystem = cfg.SystemPrompt
	f.lastMsgs = cfg.Messages
	f.mu.Unlock()
}

func (f *scriptLLM) response() *llm.Response {
	return &llm.Response{
		ID:      "resp_fake",
		Model:   "fake",
		Role:    llm.Assistant,
		Content: []llm.Content{&llm.TextContent{Text: f.reply()}},
		Usage:   *f.usage,
	}
}

func (f *scriptLLM) reply() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.replyText
}

func (f *scriptLLM) Generate(_ context.Context, opts ...llm.Option) (*llm.Response, error) {
	f.capture(opts)
	return f.response(), nil
}

func (f *scriptLLM) Stream(_ context.Context, opts ...llm.Option) (llm.StreamIterator, error) {
	f.capture(opts)
	idx := 0
	reply := f.reply()
	events := []*llm.Event{
		{Type: llm.EventTypeMessageStart, Message: &llm.Response{ID: "resp_fake", Model: "fake", Role: llm.Assistant}},
		{Type: llm.EventTypeContentBlockStart, Index: &idx, ContentBlock: &llm.EventContentBlock{Type: llm.ContentTypeText}},
		{Type: llm.EventTypeContentBlockDelta, Index: &idx, Delta: &llm.EventDelta{Type: llm.EventDeltaTypeText, Text: reply}},
		{Type: llm.EventTypeContentBlockStop, Index: &idx},
		{Type: llm.EventTypeMessageDelta, Delta: &llm.EventDelta{StopReason: "end_turn"}, Usage: f.usage},
		{Type: llm.EventTypeMessageStop},
	}
	return &scriptStream{events: events}, nil
}

func (f *scriptLLM) seenSystem() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastSystem
}

func (f *scriptLLM) seenMessages() []*llm.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastMsgs
}

func TestScriptLLMStreamsReply(t *testing.T) {
	f := newScriptLLM("hello world")
	it, err := f.Stream(context.Background())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer it.Close()
	acc := llm.NewResponseAccumulator()
	for it.Next() {
		if err := acc.AddEvent(it.Event()); err != nil {
			t.Fatalf("add event: %v", err)
		}
	}
	if got := acc.Response().Message().Text(); got != "hello world" {
		t.Fatalf("got %q want %q", got, "hello world")
	}
}
