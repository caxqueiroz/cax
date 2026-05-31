package agent

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/deepnoodle-ai/dive/llm"

	"github.com/caxqueiroz/cax/internal/config"
)

// Router resolves a model role (agent, cheap, summarizer, fact_extractor,
// subagent_default, ...) to a constructed llm.StreamingLLM. It lets utility
// LLM calls (summarization, fact extraction, sub-agent defaults) target a
// cheaper model than the main turn loop without per-call configuration.
//
// Resolution order for For(role):
//  1. cfg.ModelRoles[role] → if mapped to a named provider that is enabled,
//     return its built llm.
//  2. If role != ModelRoleCheap, try For(ModelRoleCheap).
//  3. Fall back to the main agent chain (ModelRoleAgent) — the same fallback
//     chain BuildModel returns.
//
// Construction is lazy and cached per name: utility roles that the user never
// triggers don't allocate a client. Cache is keyed by provider NAME (not role)
// so two roles pointing at the same provider share one instance.
type Router struct {
	cfg          *config.Config
	agent        llm.StreamingLLM
	byName       map[string]config.ProviderConfig // name → enabled provider
	cache        map[string]llm.StreamingLLM      // name → built llm
	missingWarns map[string]struct{}              // dedup the "role X has no model" warning
}

// NewRouterFromLLM builds a stub router that returns m for every role.
// Useful in tests that already have a scripted llm and don't want to assemble
// a full config + provider chain.
func NewRouterFromLLM(m llm.StreamingLLM) *Router {
	return &Router{
		cfg:          &config.Config{},
		agent:        m,
		byName:       map[string]config.ProviderConfig{},
		cache:        map[string]llm.StreamingLLM{},
		missingWarns: map[string]struct{}{},
	}
}

// NewRouter builds a router from cfg. agentChain is the multi-provider
// fallback chain returned by BuildModel — Router treats it as the ultimate
// fallback for any unresolved role.
func NewRouter(cfg *config.Config, agentChain llm.StreamingLLM) (*Router, error) {
	if cfg == nil {
		return nil, fmt.Errorf("router: nil config")
	}
	if agentChain == nil {
		return nil, fmt.Errorf("router: nil agent chain")
	}
	r := &Router{
		cfg:          cfg,
		agent:        agentChain,
		byName:       make(map[string]config.ProviderConfig, len(cfg.Providers)),
		cache:        make(map[string]llm.StreamingLLM),
		missingWarns: make(map[string]struct{}),
	}
	for _, p := range cfg.Providers {
		if p.IsEnabled() {
			r.byName[p.Name] = p
		}
	}
	return r, nil
}

// For resolves the named role to an llm.StreamingLLM. Always returns a
// non-nil result (worst case: the main agent chain). Role lookups are
// best-effort — unknown roles fall through silently.
func (r *Router) For(role string) llm.StreamingLLM {
	role = strings.TrimSpace(role)
	if role == "" || role == config.ModelRoleAgent {
		return r.agent
	}
	if name, ok := r.cfg.ModelRoles[role]; ok {
		if m := r.byNameOrNil(name); m != nil {
			return m
		}
		r.warnOnce(role, fmt.Sprintf("role %q -> provider %q is not enabled or unknown; falling back", role, name))
	}
	if role != config.ModelRoleCheap {
		return r.For(config.ModelRoleCheap)
	}
	return r.agent
}

// byNameOrNil returns the cached llm for name, building it on first use.
// Returns nil if the provider is not registered (not enabled).
func (r *Router) byNameOrNil(name string) llm.StreamingLLM {
	if m, ok := r.cache[name]; ok {
		return m
	}
	pc, ok := r.byName[name]
	if !ok {
		return nil
	}
	m, err := buildOne(pc)
	if err != nil {
		slog.Warn("router: build provider failed", "name", name, "err", err)
		return nil
	}
	r.cache[name] = m
	return m
}

// warnOnce emits a slog warning the first time role appears here. Avoids
// spamming the log when a role is hit on every turn.
func (r *Router) warnOnce(role, msg string) {
	if _, ok := r.missingWarns[role]; ok {
		return
	}
	r.missingWarns[role] = struct{}{}
	slog.Warn("router: " + msg)
}
