package bedrock

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/dive/llm"
)

func TestGenerateBuildsNativeAnthropicRequest(t *testing.T) {
	var gotPath, gotAPIKey, gotVersionHeader, gotContentType string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersionHeader = r.Header.Get("anthropic-version")
		gotContentType = r.Header.Get("content-type")
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("server: bad body: %v", err)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{
			"id": "msg_1",
			"model": "claude",
			"role": "assistant",
			"type": "message",
			"stop_reason": "end_turn",
			"content": [{"type": "text", "text": "hello there"}],
			"usage": {"input_tokens": 11, "output_tokens": 7}
		}`)
	}))
	defer srv.Close()

	p := New(
		WithBaseURL(srv.URL),
		WithModel("us.anthropic.claude-test"),
		WithAPIKey("key-abc"),
		WithMaxTokens(256),
	)
	resp, err := p.Generate(context.Background(),
		llm.WithUserTextMessage("hi"),
		llm.WithSystemPrompt("be brief"),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if gotPath != "/model/us.anthropic.claude-test/invoke" {
		t.Errorf("path = %q, want /model/us.anthropic.claude-test/invoke", gotPath)
	}
	if gotAPIKey != "key-abc" {
		t.Errorf("x-api-key = %q, want key-abc", gotAPIKey)
	}
	if gotVersionHeader != "" {
		// version belongs in the body, not as anthropic-version header here.
		t.Logf("anthropic-version header present: %q", gotVersionHeader)
	}
	if !strings.HasPrefix(gotContentType, "application/json") {
		t.Errorf("content-type = %q, want application/json", gotContentType)
	}
	if _, hasModel := gotBody["model"]; hasModel {
		t.Errorf("body must NOT contain model field, got: %v", gotBody)
	}
	if gotBody["anthropic_version"] != "bedrock-2023-05-31" {
		t.Errorf("anthropic_version = %v, want bedrock-2023-05-31", gotBody["anthropic_version"])
	}
	if gotBody["system"] != "be brief" {
		t.Errorf("system = %v, want 'be brief'", gotBody["system"])
	}
	if gotBody["max_tokens"] != float64(256) {
		t.Errorf("max_tokens = %v, want 256", gotBody["max_tokens"])
	}
	msgs, ok := gotBody["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages = %v, want 1 message", gotBody["messages"])
	}

	if resp.Message().Text() != "hello there" {
		t.Errorf("response text = %q, want 'hello there'", resp.Message().Text())
	}
	if resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 7 {
		t.Errorf("usage = %+v, want in=11 out=7", resp.Usage)
	}
}

func TestGenerateReturnsHTTPErrorOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"message":"slow down"}`)
	}))
	defer srv.Close()

	p := New(WithBaseURL(srv.URL), WithModel("m"), WithAPIKey("k"))
	_, err := p.Generate(context.Background(), llm.WithUserTextMessage("hi"))
	if err == nil {
		t.Fatal("expected error on 429, got nil")
	}
	var he *HTTPError
	if !asHTTPError(err, &he) {
		t.Fatalf("error is not *HTTPError: %v", err)
	}
	if he.StatusCode() != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", he.StatusCode())
	}
}

// asHTTPError is a tiny test helper around errors.As.
func asHTTPError(err error, target **HTTPError) bool {
	for err != nil {
		if he, ok := err.(*HTTPError); ok {
			*target = he
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestName(t *testing.T) {
	if got := New().Name(); got != "bedrock" {
		t.Errorf("Name() = %q, want bedrock", got)
	}
}
