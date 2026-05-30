package creator

import "strings"

// WizardKind identifies which writer the wizard targets.
type WizardKind string

const (
	WizardKindSkill   WizardKind = "skill"
	WizardKindAgent   WizardKind = "agent"
	WizardKindCommand WizardKind = "command"
)

// WizardStep is the current step in the /new wizard state machine.
type WizardStep int

const (
	// WizardStepName asks for the name when /new was invoked without one.
	WizardStepName WizardStep = iota
	// WizardStepDescription collects the one-line description.
	WizardStepDescription
	// WizardStepToolsOrHint asks for tools (agent) or argument hint (command);
	// skipped entirely for skills.
	WizardStepToolsOrHint
	// WizardStepBody collects the markdown body.
	WizardStepBody
	// WizardStepConfirm prompts (y/n) before invoking the creator backend.
	WizardStepConfirm
	// WizardStepDone signals the cli layer to dispatch the create call.
	WizardStepDone
)

// Wizard is the pure-data state shared by /new across kinds. It carries no
// rendering logic; the cli package owns prompts and input routing.
type Wizard struct {
	Kind         WizardKind
	Step         WizardStep
	Name         string
	Description  string
	Tools        []string // agent only
	ArgumentHint string   // command only
	Body         string
}

// Prompt returns the prompt text the cli renders for the wizard's current step.
func (w *Wizard) Prompt() string {
	switch w.Step {
	case WizardStepName:
		return "name? (kebab-case, e.g. explain-go-embedding)"
	case WizardStepDescription:
		return "description?"
	case WizardStepToolsOrHint:
		switch w.Kind {
		case WizardKindAgent:
			return "tools? (comma-separated, blank for default)"
		case WizardKindCommand:
			return "argument-hint? (blank for none)"
		default:
			return ""
		}
	case WizardStepBody:
		return "body? (single line; submit with Enter)"
	case WizardStepConfirm:
		return "confirm? (y/n)"
	}
	return ""
}

// Advance records the typed input into the appropriate field and transitions
// to the next step. Returns the prompt for the new step or "" when the
// machine reaches WizardStepDone.
//
// On WizardStepConfirm any input other than y/yes is treated as a decline
// and the wizard transitions to Done with the Body cleared so the caller can
// short-circuit the create call. The cli layer treats Done as "dispatch if
// confirmed; otherwise just clear" by inspecting the wizard's Body.
func (w *Wizard) Advance(line string) string {
	switch w.Step {
	case WizardStepName:
		w.Name = strings.TrimSpace(line)
		w.Step = WizardStepDescription
	case WizardStepDescription:
		w.Description = line
		if w.Kind == WizardKindSkill {
			w.Step = WizardStepBody
		} else {
			w.Step = WizardStepToolsOrHint
		}
	case WizardStepToolsOrHint:
		switch w.Kind {
		case WizardKindAgent:
			w.Tools = splitCommaList(line)
		case WizardKindCommand:
			w.ArgumentHint = strings.TrimSpace(line)
		}
		w.Step = WizardStepBody
	case WizardStepBody:
		w.Body = line
		w.Step = WizardStepConfirm
	case WizardStepConfirm:
		ans := strings.ToLower(strings.TrimSpace(line))
		if ans != "y" && ans != "yes" {
			// Decline: clear the body so the cli layer can detect the cancel
			// without an extra confirmed flag.
			w.Body = ""
		}
		w.Step = WizardStepDone
	}
	return w.Prompt()
}

// Confirmed reports whether the wizard reached Done via an affirmative answer
// at the confirm step. The cli dispatches the create call only when true.
func (w *Wizard) Confirmed() bool {
	return w.Step == WizardStepDone && w.Body != ""
}

func splitCommaList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
