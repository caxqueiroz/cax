// Package bedrock implements a dive llm.LLM / llm.StreamingLLM that talks to
// Bedrock Claude through a KrakenD no-op gateway using the native Anthropic
// Messages payload (anthropic_version: bedrock-2023-05-31, no model in the
// body), authenticating via the x-api-key header.
package bedrock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/deepnoodle-ai/dive/llm"
	"github.com/deepnoodle-ai/dive/providers/anthropic"
)

// ProviderName is the name reported by Name().
const ProviderName = "bedrock"

// anthropicVersion is the value Bedrock requires in the request body for the
// native Anthropic Messages format.
const anthropicVersion = "bedrock-2023-05-31"

const defaultMaxTokens = 4096

// minReasoningBudget is the minimum extended-thinking budget Anthropic accepts.
const minReasoningBudget = 1024

var defaultClient = &http.Client{Timeout: 300 * time.Second}

var _ llm.LLM = (*Provider)(nil)

// Provider talks to Bedrock Claude through KrakenD.
type Provider struct {
	baseURL    string
	model      string
	apiKey     string
	httpClient *http.Client
	maxTokens  int
}

// Option configures a Provider.
type Option func(*Provider)

// WithBaseURL sets the KrakenD base URL (e.g. https://krakend.internal/bedrock).
func WithBaseURL(u string) Option { return func(p *Provider) { p.baseURL = u } }

// WithModel sets the Bedrock/inference-profile model ID used in the URL path.
func WithModel(m string) Option { return func(p *Provider) { p.model = m } }

// WithAPIKey sets the value sent in the x-api-key header.
func WithAPIKey(k string) Option { return func(p *Provider) { p.apiKey = k } }

// WithHTTPClient sets the HTTP client.
func WithHTTPClient(c *http.Client) Option { return func(p *Provider) { p.httpClient = c } }

// WithMaxTokens sets the default max tokens when the call does not specify one.
func WithMaxTokens(n int) Option { return func(p *Provider) { p.maxTokens = n } }

// New creates a Provider with the given options.
func New(opts ...Option) *Provider {
	p := &Provider{
		httpClient: defaultClient,
		maxTokens:  defaultMaxTokens,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name returns the provider name.
func (p *Provider) Name() string { return ProviderName }

// HTTPError is returned for non-2xx responses from the gateway.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("bedrock api error (status %d): %s", e.Status, e.Body)
}

// StatusCode exposes the HTTP status for retry classification.
func (e *HTTPError) StatusCode() int { return e.Status }

// bedrockRequest is the native Anthropic Messages body for Bedrock. It omits
// the "model" field (model goes in the URL) and injects anthropic_version.
// Field types reuse dive's anthropic/llm types so mapping stays consistent.
type bedrockRequest struct {
	AnthropicVersion string                `json:"anthropic_version"`
	Messages         []*llm.Message        `json:"messages"`
	MaxTokens        int                   `json:"max_tokens"`
	System           string                `json:"system,omitempty"`
	Temperature      *float64              `json:"temperature,omitempty"`
	Stream           bool                  `json:"stream,omitempty"`
	Tools            []map[string]any      `json:"tools,omitempty"`
	ToolChoice       *anthropic.ToolChoice `json:"tool_choice,omitempty"`
	Thinking         *anthropic.Thinking   `json:"thinking,omitempty"`
}

// buildRequest maps an llm.Config into the Bedrock body.
func (p *Provider) buildRequest(config *llm.Config, stream bool) (*bedrockRequest, error) {
	if len(config.Messages) == 0 {
		return nil, fmt.Errorf("bedrock: no messages provided")
	}
	maxTokens := p.maxTokens
	if config.MaxTokens != nil {
		maxTokens = *config.MaxTokens
	}
	req := &bedrockRequest{
		AnthropicVersion: anthropicVersion,
		Messages:         config.Messages,
		MaxTokens:        maxTokens,
		System:           config.SystemPrompt,
		Temperature:      config.Temperature,
		Stream:           stream,
	}
	if len(config.Tools) > 0 {
		tools := make([]map[string]any, 0, len(config.Tools))
		for _, tool := range config.Tools {
			tc := map[string]any{
				"name":        tool.Name(),
				"description": tool.Description(),
			}
			if schema := tool.Schema(); schema != nil && schema.Type != "" {
				tc["input_schema"] = schema
			}
			tools = append(tools, tc)
		}
		req.Tools = tools
	}
	if config.ToolChoice != nil && len(config.Tools) > 0 {
		req.ToolChoice = &anthropic.ToolChoice{
			Type: anthropic.ToolChoiceType(config.ToolChoice.Type),
			Name: config.ToolChoice.Name,
		}
		if config.ParallelToolCalls != nil && !*config.ParallelToolCalls {
			req.ToolChoice.DisableParallelToolUse = true
		}
	}
	if err := applyThinking(req, config); err != nil {
		return nil, err
	}
	return req, nil
}

// applyThinking maps dive's reasoning options onto the Anthropic thinking
// block, mirroring dive's anthropic provider: an explicit ReasoningBudget
// enables thinking with that budget; otherwise a ReasoningEffort maps to a
// fixed budget. The two are mutually exclusive.
func applyThinking(req *bedrockRequest, config *llm.Config) error {
	if config.ReasoningBudget != nil {
		budget := *config.ReasoningBudget
		if budget < minReasoningBudget {
			return fmt.Errorf("bedrock: reasoning budget must be at least %d", minReasoningBudget)
		}
		req.Thinking = &anthropic.Thinking{Type: "enabled", BudgetTokens: budget}
	}
	if config.ReasoningEffort != "" {
		if req.Thinking != nil {
			return fmt.Errorf("bedrock: cannot set both reasoning budget and effort")
		}
		req.Thinking = &anthropic.Thinking{Type: "enabled"}
		switch config.ReasoningEffort {
		case llm.ReasoningEffortLow:
			req.Thinking.BudgetTokens = 1024
		case llm.ReasoningEffortMedium:
			req.Thinking.BudgetTokens = 4096
		case llm.ReasoningEffortHigh:
			req.Thinking.BudgetTokens = 16384
		default:
			return fmt.Errorf("bedrock: invalid reasoning effort: %s", config.ReasoningEffort)
		}
	}
	return nil
}

// endpoint builds the gateway URL for the given suffix ("invoke" or
// "invoke-with-response-stream").
func (p *Provider) endpoint(suffix string) string {
	base := strings.TrimRight(p.baseURL, "/")
	return base + "/model/" + url.PathEscape(p.model) + "/" + suffix
}

// newHTTPRequest creates a POST request with the Bedrock/Anthropic headers.
func (p *Provider) newHTTPRequest(ctx context.Context, suffix string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(suffix), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("bedrock: create request: %w", err)
	}
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("content-type", "application/json")
	return req, nil
}

// Generate performs a non-streaming InvokeModel-style call.
func (p *Provider) Generate(ctx context.Context, opts ...llm.Option) (*llm.Response, error) {
	config := &llm.Config{}
	config.Apply(opts...)

	br, err := p.buildRequest(config, false)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(br)
	if err != nil {
		return nil, fmt.Errorf("bedrock: marshal request: %w", err)
	}
	req, err := p.newHTTPRequest(ctx, "invoke", body)
	if err != nil {
		return nil, err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bedrock: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(raw)}
	}

	var result llm.Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("bedrock: decode response: %w", err)
	}
	if len(result.Content) == 0 {
		return nil, fmt.Errorf("bedrock: empty response")
	}
	return &result, nil
}
