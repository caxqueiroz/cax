package agent

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/deepnoodle-ai/dive/llm"
)

// fakeLLM is a scriptable llm.StreamingLLM for fallback tests.
type fakeLLM struct {
	name        string
	genErr      error
	genResp     *llm.Response
	streamErr   error
	stream      llm.StreamIterator
	genCalls    int
	streamCalls int
}

func (f *fakeLLM) Name() string { return f.name }

func (f *fakeLLM) Generate(ctx context.Context, opts ...llm.Option) (*llm.Response, error) {
	f.genCalls++
	if f.genErr != nil {
		return nil, f.genErr
	}
	return f.genResp, nil
}

func (f *fakeLLM) Stream(ctx context.Context, opts ...llm.Option) (llm.StreamIterator, error) {
	f.streamCalls++
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	return f.stream, nil
}

// statusErr is an error carrying an HTTP status code.
type statusErr struct {
	code int
}

func (e *statusErr) Error() string   { return "status error" }
func (e *statusErr) StatusCode() int { return e.code }

func okResp(text string) *llm.Response {
	return &llm.Response{
		Role:    llm.Assistant,
		Content: []llm.Content{&llm.TextContent{Text: text}},
	}
}

func TestFallbackAdvancesOnRetryableError(t *testing.T) {
	p1 := &fakeLLM{name: "p1", genErr: &statusErr{code: 429}}
	p2 := &fakeLLM{name: "p2", genResp: okResp("from p2")}
	f := &fallbackLLM{providers: []llm.StreamingLLM{p1, p2}}

	resp, err := f.Generate(context.Background(), llm.WithUserTextMessage("hi"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Message().Text() != "from p2" {
		t.Errorf("text = %q, want 'from p2'", resp.Message().Text())
	}
	if p1.genCalls != 1 || p2.genCalls != 1 {
		t.Errorf("calls: p1=%d p2=%d, want 1 and 1", p1.genCalls, p2.genCalls)
	}
}

func TestFallbackStopsOnNonRetryableError(t *testing.T) {
	nonRetryable := errors.New("bad request: invalid input")
	p1 := &fakeLLM{name: "p1", genErr: nonRetryable}
	p2 := &fakeLLM{name: "p2", genResp: okResp("from p2")}
	f := &fallbackLLM{providers: []llm.StreamingLLM{p1, p2}}

	_, err := f.Generate(context.Background(), llm.WithUserTextMessage("hi"))
	if !errors.Is(err, nonRetryable) {
		t.Fatalf("err = %v, want the non-retryable error", err)
	}
	if p1.genCalls != 1 {
		t.Errorf("p1 calls = %d, want 1", p1.genCalls)
	}
	if p2.genCalls != 0 {
		t.Errorf("p2 calls = %d, want 0 (chain must stop)", p2.genCalls)
	}
}

func TestFallbackAllFailReturnsLastError(t *testing.T) {
	last := &statusErr{code: 503}
	p1 := &fakeLLM{name: "p1", genErr: &statusErr{code: 429}}
	p2 := &fakeLLM{name: "p2", genErr: last}
	f := &fallbackLLM{providers: []llm.StreamingLLM{p1, p2}}

	_, err := f.Generate(context.Background(), llm.WithUserTextMessage("hi"))
	if !errors.Is(err, last) {
		t.Fatalf("err = %v, want last error %v", err, last)
	}
	if p1.genCalls != 1 || p2.genCalls != 1 {
		t.Errorf("calls: p1=%d p2=%d, want 1 and 1", p1.genCalls, p2.genCalls)
	}
}

func TestFallbackStreamAdvancesOnRetryableError(t *testing.T) {
	p1 := &fakeLLM{name: "p1", streamErr: &statusErr{code: 500}}
	p2 := &fakeLLM{name: "p2", stream: emptyIterator{}}
	f := &fallbackLLM{providers: []llm.StreamingLLM{p1, p2}}

	it, err := f.Stream(context.Background(), llm.WithUserTextMessage("hi"))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	_ = it.Close()
	if p1.streamCalls != 1 || p2.streamCalls != 1 {
		t.Errorf("stream calls: p1=%d p2=%d, want 1 and 1", p1.streamCalls, p2.streamCalls)
	}
}

func TestFallbackName(t *testing.T) {
	f := &fallbackLLM{providers: []llm.StreamingLLM{&fakeLLM{name: "p1"}, &fakeLLM{name: "p2"}}}
	if got := f.Name(); got != "p1" {
		t.Errorf("Name() = %q, want p1 (first provider)", got)
	}
	if got := (&fallbackLLM{}).Name(); got != "fallback" {
		t.Errorf("empty Name() = %q, want fallback", got)
	}
}

func TestIsRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"429", &statusErr{code: 429}, true},
		{"500", &statusErr{code: 500}, true},
		{"503", &statusErr{code: 503}, true},
		{"504", &statusErr{code: 504}, true},
		{"520", &statusErr{code: 520}, true},
		{"400", &statusErr{code: 400}, false},
		{"404", &statusErr{code: 404}, false},
		{"plain error", errors.New("boom"), false},
		{"context canceled", context.Canceled, false},
		{"deadline exceeded", context.DeadlineExceeded, false},
		{"net timeout", timeoutErr{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetryable(tc.err); got != tc.want {
				t.Errorf("isRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// emptyIterator is a no-op llm.StreamIterator.
type emptyIterator struct{}

func (emptyIterator) Next() bool        { return false }
func (emptyIterator) Event() *llm.Event { return nil }
func (emptyIterator) Err() error        { return nil }
func (emptyIterator) Close() error      { return nil }

// timeoutErr is a net.Error reporting Timeout() == true.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var _ net.Error = timeoutErr{}
var _ = time.Second // keep time import used if trimmed later
