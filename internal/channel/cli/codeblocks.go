package cli

import "strings"

// codeBlock is one fenced ``` block inside a markdown reply.
type codeBlock struct {
	Lang string // optional language tag, e.g. "go"
	Code string // body without the fences
}

// extractCodeBlocks pulls every fenced ``` block out of markdown, in order.
// Supports the standard ```lang\n…\n``` form; ignores inline `code` spans.
// Returns an empty slice when there are no fenced blocks.
func extractCodeBlocks(md string) []codeBlock {
	var out []codeBlock
	lines := strings.Split(md, "\n")
	in := false
	var lang string
	var buf strings.Builder
	for _, line := range lines {
		trim := strings.TrimRight(line, "\r")
		if strings.HasPrefix(strings.TrimSpace(trim), "```") {
			if !in {
				in = true
				lang = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(trim), "```"))
				buf.Reset()
				continue
			}
			// closing fence
			out = append(out, codeBlock{Lang: lang, Code: buf.String()})
			in = false
			lang = ""
			buf.Reset()
			continue
		}
		if in {
			buf.WriteString(trim)
			buf.WriteByte('\n')
		}
	}
	// If the model never closed the last fence, salvage the body.
	if in && buf.Len() > 0 {
		out = append(out, codeBlock{Lang: lang, Code: buf.String()})
	}
	return out
}

// lastBotEntry returns the most recent assistant historyEntry, or nil if the
// history is empty / has no bot replies.
func (m model) lastBotEntry() *historyEntry {
	for i := len(m.history) - 1; i >= 0; i-- {
		if m.history[i].who == "bot" {
			h := m.history[i]
			return &h
		}
	}
	return nil
}
