package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deepnoodle-ai/dive/llm"

	"github.com/caxqueiroz/cax/internal/memory"
)

// modelFactExtractor implements memory.FactExtractor using an LLM call. The
// model returns a strict JSON array of operations; on parse failure the
// extractor returns an empty op list (best-effort, never fatal).
type modelFactExtractor struct {
	model llm.LLM
}

// NewFactExtractor wraps m as a memory.FactExtractor. Callers typically pass
// router.For(config.ModelRoleFactExtractor) so extraction uses the cheap role.
func NewFactExtractor(m llm.LLM) memory.FactExtractor {
	return &modelFactExtractor{model: m}
}

const factExtractorPrompt = `You are a memory extractor. Read the user/assistant turn below and decide what DURABLE FACTS about the user (preferences, identity, decisions, relationships, ongoing projects) should be remembered.

DURABLE means: facts that would still be relevant in a future conversation. NOT to extract: pleasantries, ephemeral state ("running build right now"), trivia.

You will be shown the EXISTING facts already remembered. Choose for each new fact:
- "add" — the fact is NEW and not represented in existing facts
- "update" — the new info SUPERSEDES an existing fact (e.g. user changed their mind, role changed)
- "delete" — an existing fact is now KNOWN FALSE or obsolete

Output a JSON ARRAY of operations. Each op is one of:
  {"op":"add","text":"<atomic declarative fact>"}
  {"op":"update","id":<existing_id>,"text":"<revised fact>"}
  {"op":"delete","id":<existing_id>}

Rules:
- Output JSON ONLY. No markdown, no commentary, no code fences.
- If nothing is worth recording, output [].
- Each fact must be one atomic declarative sentence, written about the user.
- Do not duplicate facts that are already represented.`

// Extract calls the LLM with the turn + existing facts and parses the JSON
// response into operations.
func (e *modelFactExtractor) Extract(ctx context.Context, userText, assistantText string, existing []memory.Fact) ([]memory.FactOp, error) {
	if e.model == nil {
		return nil, fmt.Errorf("factextractor: nil model")
	}
	if strings.TrimSpace(userText) == "" && strings.TrimSpace(assistantText) == "" {
		return nil, nil
	}

	var sb strings.Builder
	sb.WriteString("EXISTING FACTS")
	if len(existing) == 0 {
		sb.WriteString(" (none yet)\n")
	} else {
		sb.WriteString(":\n")
		for _, f := range existing {
			fmt.Fprintf(&sb, "  %d: %s\n", f.ID, f.Text)
		}
	}
	sb.WriteString("\nTURN:\nUser: ")
	sb.WriteString(strings.TrimSpace(userText))
	sb.WriteString("\nAssistant: ")
	sb.WriteString(strings.TrimSpace(assistantText))
	sb.WriteString("\n\nOutput JSON:")

	resp, err := e.model.Generate(ctx,
		llm.WithSystemPrompt(factExtractorPrompt),
		llm.WithMessages(llm.NewUserTextMessage(sb.String())),
	)
	if err != nil {
		return nil, fmt.Errorf("factextractor: generate: %w", err)
	}
	raw := strings.TrimSpace(resp.Message().Text())
	if raw == "" {
		return nil, nil
	}
	// Tolerate a fenced code block — strip ``` and an optional language tag.
	raw = stripCodeFences(raw)

	var ops []memory.FactOp
	if err := json.Unmarshal([]byte(raw), &ops); err != nil {
		return nil, fmt.Errorf("factextractor: parse json: %w (raw=%q)", err, raw)
	}
	// Drop any ops the model emitted with bad shape rather than failing the
	// whole batch.
	valid := ops[:0]
	for _, op := range ops {
		if err := op.Validate(); err == nil {
			valid = append(valid, op)
		}
	}
	return valid, nil
}

// stripCodeFences removes a leading ```lang and trailing ``` if present.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop first line (``` or ```json).
	if nl := strings.IndexByte(s, '\n'); nl != -1 {
		s = s[nl+1:]
	}
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
