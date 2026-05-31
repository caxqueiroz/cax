package agent

import (
	"testing"

	"github.com/caxqueiroz/cax/internal/memory"
)

func TestFactExtractor_ParsesJSONArray(t *testing.T) {
	model := newScriptLLM(`[{"op":"add","text":"user prefers Go"},{"op":"update","id":3,"text":"user works at NewCo"}]`)
	e := NewFactExtractor(model)
	ops, err := e.Extract(t.Context(), "I just switched to NewCo and love Go.", "Got it!", []memory.Fact{
		{ID: 3, Text: "user works at OldCo"},
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("want 2 ops, got %d (%+v)", len(ops), ops)
	}
	if ops[0].Op != "add" || ops[0].Text != "user prefers Go" {
		t.Errorf("op 0 = %+v", ops[0])
	}
	if ops[1].Op != "update" || ops[1].ID != 3 || ops[1].Text != "user works at NewCo" {
		t.Errorf("op 1 = %+v", ops[1])
	}
}

func TestFactExtractor_AcceptsCodeFences(t *testing.T) {
	model := newScriptLLM("```json\n[{\"op\":\"add\",\"text\":\"a fact\"}]\n```")
	e := NewFactExtractor(model)
	ops, err := e.Extract(t.Context(), "hi", "hello", nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(ops) != 1 || ops[0].Text != "a fact" {
		t.Fatalf("ops = %+v", ops)
	}
}

func TestFactExtractor_EmptyArray(t *testing.T) {
	model := newScriptLLM("[]")
	e := NewFactExtractor(model)
	ops, err := e.Extract(t.Context(), "hi", "hello", nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(ops) != 0 {
		t.Fatalf("want 0 ops, got %+v", ops)
	}
}

func TestFactExtractor_DropsMalformedOps(t *testing.T) {
	model := newScriptLLM(`[{"op":"add","text":"good"},{"op":"unknown"},{"op":"update"}]`)
	e := NewFactExtractor(model)
	ops, err := e.Extract(t.Context(), "x", "y", nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(ops) != 1 || ops[0].Text != "good" {
		t.Fatalf("malformed ops not dropped: %+v", ops)
	}
}

func TestFactExtractor_RejectsInvalidJSON(t *testing.T) {
	model := newScriptLLM("not json at all")
	e := NewFactExtractor(model)
	_, err := e.Extract(t.Context(), "x", "y", nil)
	if err == nil {
		t.Fatal("expected parse error")
	}
}
