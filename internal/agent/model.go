// Package agent assembles the dive Agent. model.go owns the multi-provider
// model: BuildModel constructs each configured provider and wraps them in an
// ordered fallback chain.
package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/deepnoodle-ai/dive/llm"
	openaiprovider "github.com/deepnoodle-ai/dive/providers/openai"

	"github.com/caxqueiroz/czcli/internal/config"
	"github.com/caxqueiroz/czcli/internal/providers/bedrock"
)

var _ llm.StreamingLLM = (*fallbackLLM)(nil)

// fallbackLLM wraps an ordered list of providers, advancing to the next on a
// retryable error. If all fail, it returns the last error.
type fallbackLLM struct {
	providers []llm.StreamingLLM
}

// Name returns the first provider's name, or "fallback" if empty.
func (f *fallbackLLM) Name() string {
	if len(f.providers) == 0 {
		return "fallback"
	}
	return f.providers[0].Name()
}

// Generate tries each provider in order until one succeeds or a non-retryable
// error occurs.
func (f *fallbackLLM) Generate(ctx context.Context, opts ...llm.Option) (*llm.Response, error) {
	var lastErr error
	for _, p := range f.providers {
		resp, err := p.Generate(ctx, opts...)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return nil, err
		}
	}
	if lastErr == nil {
		return nil, errors.New("agent: no providers configured")
	}
	return nil, lastErr
}

// Stream tries each provider in order until one returns a stream or a
// non-retryable error occurs.
func (f *fallbackLLM) Stream(ctx context.Context, opts ...llm.Option) (llm.StreamIterator, error) {
	var lastErr error
	for _, p := range f.providers {
		it, err := p.Stream(ctx, opts...)
		if err == nil {
			return it, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return nil, err
		}
	}
	if lastErr == nil {
		return nil, errors.New("agent: no providers configured")
	}
	return nil, lastErr
}

// isRetryable reports whether an error should trigger fallback to the next
// provider: HTTP 429/5xx (and Cloudflare 520) or a network timeout. Context
// cancellation/deadline errors are not retryable.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var coder interface{ StatusCode() int }
	if errors.As(err, &coder) {
		switch coder.StatusCode() {
		case 429, 500, 502, 503, 504, 520:
			return true
		default:
			return false
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

// BuildModel constructs each configured provider in order and wraps them in a
// fallback chain. Provider order in config defines fallback priority.
func BuildModel(cfg *config.Config) (llm.StreamingLLM, error) {
	if cfg == nil || len(cfg.Providers) == 0 {
		return nil, fmt.Errorf("agent: at least one provider is required")
	}
	providers := make([]llm.StreamingLLM, 0, len(cfg.Providers))
	for i, pc := range cfg.Providers {
		switch pc.Name {
		case "bedrock":
			providers = append(providers, bedrock.New(
				bedrock.WithBaseURL(pc.BaseURL),
				bedrock.WithModel(pc.Model),
				bedrock.WithAPIKey(os.Getenv(pc.TokenEnv)),
				bedrock.WithMaxTokens(pc.MaxTokens),
			))
		case "openai":
			providers = append(providers, openaiprovider.New(
				openaiprovider.WithModel(pc.Model),
				openaiprovider.WithAPIKey(os.Getenv(pc.APIKeyEnv)),
				openaiprovider.WithMaxTokens(pc.MaxTokens),
			))
		default:
			return nil, fmt.Errorf("agent: providers[%d]: unknown provider %q (want bedrock|openai)", i, pc.Name)
		}
	}
	return &fallbackLLM{providers: providers}, nil
}
