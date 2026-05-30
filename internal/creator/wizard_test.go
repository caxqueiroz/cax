package creator

import "testing"

func TestWizard_SkillHappyPath(t *testing.T) {
	w := &Wizard{Kind: WizardKindSkill, Name: "explain-go", Step: WizardStepDescription}
	if got := w.Prompt(); got != "description?" {
		t.Fatalf("initial prompt = %q, want %q", got, "description?")
	}
	next := w.Advance("Explain Go.")
	if w.Description != "Explain Go." {
		t.Fatalf("description not stored: %+v", w)
	}
	if w.Step != WizardStepBody {
		t.Fatalf("skill should skip tools step; step = %v", w.Step)
	}
	if next != w.Prompt() || next == "" {
		t.Fatalf("Advance returned %q, Prompt = %q", next, w.Prompt())
	}
	next = w.Advance("Use a worked example.")
	if w.Body != "Use a worked example." {
		t.Fatalf("body not stored: %q", w.Body)
	}
	if w.Step != WizardStepConfirm {
		t.Fatalf("expected confirm step; got %v", w.Step)
	}
	if next != "confirm? (y/n)" {
		t.Fatalf("confirm prompt = %q", next)
	}
	_ = w.Advance("y")
	if w.Step != WizardStepDone {
		t.Fatalf("expected done; got %v", w.Step)
	}
	if !w.Confirmed() {
		t.Fatalf("expected Confirmed=true")
	}
}

func TestWizard_AgentRoutesThroughToolsStep(t *testing.T) {
	w := &Wizard{Kind: WizardKindAgent, Name: "reviewer", Step: WizardStepDescription}
	w.Advance("Reviews Go diffs.")
	if w.Step != WizardStepToolsOrHint {
		t.Fatalf("agent should hit tools step; got %v", w.Step)
	}
	if got := w.Prompt(); got == "" {
		t.Fatalf("expected non-empty tools prompt")
	}
	w.Advance("Read, Glob, Edit")
	if len(w.Tools) != 3 || w.Tools[0] != "Read" || w.Tools[2] != "Edit" {
		t.Fatalf("tools = %v, want [Read Glob Edit]", w.Tools)
	}
	if w.Step != WizardStepBody {
		t.Fatalf("expected body step after tools; got %v", w.Step)
	}
}

func TestWizard_CommandHintStored(t *testing.T) {
	w := &Wizard{Kind: WizardKindCommand, Name: "greet", Step: WizardStepDescription}
	w.Advance("Greet the user.")
	if w.Step != WizardStepToolsOrHint {
		t.Fatalf("command should hit hint step; got %v", w.Step)
	}
	w.Advance("<name>")
	if w.ArgumentHint != "<name>" {
		t.Fatalf("ArgumentHint = %q, want <name>", w.ArgumentHint)
	}
}

func TestWizard_ConfirmDeclineClearsBody(t *testing.T) {
	w := &Wizard{Kind: WizardKindSkill, Step: WizardStepConfirm, Body: "stays?"}
	w.Advance("n")
	if w.Step != WizardStepDone {
		t.Fatalf("decline should still hit Done; got %v", w.Step)
	}
	if w.Confirmed() {
		t.Fatalf("expected Confirmed=false on decline")
	}
}

func TestWizard_NameStep(t *testing.T) {
	w := &Wizard{Kind: WizardKindSkill, Step: WizardStepName}
	if got := w.Prompt(); got == "" {
		t.Fatalf("expected non-empty name prompt")
	}
	w.Advance("explain-go")
	if w.Name != "explain-go" {
		t.Fatalf("Name = %q", w.Name)
	}
	if w.Step != WizardStepDescription {
		t.Fatalf("expected description step; got %v", w.Step)
	}
}
