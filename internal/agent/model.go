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
	"sync"

	"github.com/deepnoodle-ai/dive/llm"
	openaiprovider "github.com/deepnoodle-ai/dive/providers/openai"

	"github.com/caxqueiroz/cax/internal/config"
	"github.com/caxqueiroz/cax/internal/providers/bedrock"
)

var (
	_ llm.StreamingLLM = (*fallbackLLM)(nil)
	_ ActiveReporter   = (*fallbackLLM)(nil)
)

// ActiveReporter reports which provider in a fallback chain served the most
// recent call. agent.Status type-asserts BuildModel's result to this interface
// to populate channel.Status.Model / OnFallback / FallbackIndex; index 0 is the
// primary provider.
type ActiveReporter interface {
	Active() (index int, name string)
}

// fallbackLLM wraps an ordered list of providers, advancing to the next on a
// retryable error. If all fail, it returns the last error. It tracks the index
// of the provider that served the most recent successful call so the dashboard
// can show a fallback indicator. modelIDs holds each provider's configured
// model string (e.g. "gpt-5.5") so the status row can surface the actual model
// rather than the provider name.
type fallbackLLM struct {
	providers []llm.StreamingLLM
	modelIDs  []string

	mu          sync.Mutex
	activeIndex int
}

// ActiveModel returns the model ID configured for the currently-active
// provider (e.g. "gpt-5.5"), falling back to the provider name when no model
// was recorded. With no providers it returns "".
func (f *fallbackLLM) ActiveModel() string {
	f.mu.Lock()
	i := f.activeIndex
	f.mu.Unlock()
	if i < 0 || i >= len(f.providers) {
		return ""
	}
	if i < len(f.modelIDs) && f.modelIDs[i] != "" {
		return f.modelIDs[i]
	}
	return f.providers[i].Name()
}

// Name returns the first provider's name, or "fallback" if empty.
func (f *fallbackLLM) Name() string {
	if len(f.providers) == 0 {
		return "fallback"
	}
	return f.providers[0].Name()
}

// setActive records the index of the provider that served the most recent call.
func (f *fallbackLLM) setActive(i int) {
	f.mu.Lock()
	f.activeIndex = i
	f.mu.Unlock()
}

// Active returns the 0-based index and name of the provider that served the
// most recent call; index 0 is the primary. With no providers it reports
// (0, "fallback").
func (f *fallbackLLM) Active() (int, string) {
	f.mu.Lock()
	i := f.activeIndex
	f.mu.Unlock()
	if i < 0 || i >= len(f.providers) {
		return 0, "fallback"
	}
	return i, f.providers[i].Name()
}

// Generate tries each provider in order until one succeeds or a non-retryable
// error occurs.
func (f *fallbackLLM) Generate(ctx context.Context, opts ...llm.Option) (*llm.Response, error) {
	var lastErr error
	for i, p := range f.providers {
		resp, err := p.Generate(ctx, opts...)
		if err == nil {
			f.setActive(i)
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
	for i, p := range f.providers {
		it, err := p.Stream(ctx, opts...)
		if err == nil {
			f.setActive(i)
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

// BuildModel constructs each enabled provider in order and wraps them in a
// fallback chain. Provider order in config defines fallback priority; entries
// with `enabled: false` are skipped (unknown providers still error).
func BuildModel(cfg *config.Config) (llm.StreamingLLM, error) {
	if cfg == nil || len(cfg.Providers) == 0 {
		return nil, fmt.Errorf("agent: at least one provider is required")
	}
	providers := make([]llm.StreamingLLM, 0, len(cfg.Providers))
	modelIDs := make([]string, 0, len(cfg.Providers))
	for i, pc := range cfg.Providers {
		if !pc.IsEnabled() {
			continue
		}
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
		modelIDs = append(modelIDs, pc.Model)
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("agent: all providers are disabled; set enabled: true on at least one")
	}
	return &fallbackLLM{providers: providers, modelIDs: modelIDs}, nil
}
