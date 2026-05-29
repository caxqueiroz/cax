package bedrock

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
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

// encodeChunkFrame wraps Anthropic event JSON the way Bedrock does: a frame
// with :event-type=chunk and a JSON payload {"bytes": base64(eventJSON)}.
func encodeChunkFrame(t *testing.T, eventJSON string) []byte {
	t.Helper()
	wrapper := map[string]string{"bytes": base64.StdEncoding.EncodeToString([]byte(eventJSON))}
	payload, err := json.Marshal(wrapper)
	if err != nil {
		t.Fatalf("marshal wrapper: %v", err)
	}
	msg := eventstream.Message{
		Headers: eventstream.Headers{
			{Name: ":event-type", Value: eventstream.StringValue("chunk")},
			{Name: ":content-type", Value: eventstream.StringValue("application/json")},
			{Name: ":message-type", Value: eventstream.StringValue("event")},
		},
		Payload: payload,
	}
	var buf bytes.Buffer
	if err := eventstream.NewEncoder().Encode(&buf, msg); err != nil {
		t.Fatalf("encode frame: %v", err)
	}
	return buf.Bytes()
}

func TestStreamDecodesEventStream(t *testing.T) {
	frames := [][]byte{
		encodeChunkFrame(t, `{"type":"message_start","message":{"id":"msg_1","model":"claude","role":"assistant","type":"message","stop_reason":"","content":[],"usage":{"input_tokens":5,"output_tokens":0}}}`),
		encodeChunkFrame(t, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		encodeChunkFrame(t, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`),
		encodeChunkFrame(t, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`),
		encodeChunkFrame(t, `{"type":"content_block_stop","index":0}`),
		encodeChunkFrame(t, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`),
		encodeChunkFrame(t, `{"type":"message_stop"}`),
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/invoke-with-response-stream") {
			t.Errorf("stream path = %q, want suffix /invoke-with-response-stream", r.URL.Path)
		}
		w.Header().Set("content-type", "application/vnd.amazon.eventstream")
		for _, f := range frames {
			_, _ = w.Write(f)
		}
	}))
	defer srv.Close()

	p := New(WithBaseURL(srv.URL), WithModel("m"), WithAPIKey("k"))
	it, err := p.Stream(context.Background(), llm.WithUserTextMessage("hi"))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = it.Close() }()

	acc := llm.NewResponseAccumulator()
	var types []llm.EventType
	for it.Next() {
		ev := it.Event()
		types = append(types, ev.Type)
		if err := acc.AddEvent(ev); err != nil {
			t.Fatalf("AddEvent: %v", err)
		}
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterator err: %v", err)
	}
	if !acc.IsComplete() {
		t.Fatal("accumulator not complete")
	}
	resp := acc.Response()
	if resp.Message().Text() != "Hello world" {
		t.Errorf("text = %q, want 'Hello world'", resp.Message().Text())
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q, want end_turn", resp.StopReason)
	}
	if resp.Usage.InputTokens != 5 || resp.Usage.OutputTokens != 4 {
		t.Errorf("usage = %+v, want in=5 out=4", resp.Usage)
	}
	if len(types) == 0 || types[0] != llm.EventTypeMessageStart {
		t.Errorf("first event = %v, want message_start", types)
	}
}

func TestStreamReturnsHTTPErrorOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "down")
	}))
	defer srv.Close()

	p := New(WithBaseURL(srv.URL), WithModel("m"), WithAPIKey("k"))
	_, err := p.Stream(context.Background(), llm.WithUserTextMessage("hi"))
	if err == nil {
		t.Fatal("expected error on 503, got nil")
	}
	var he *HTTPError
	if !asHTTPError(err, &he) || he.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("want *HTTPError 503, got %v", err)
	}
}
