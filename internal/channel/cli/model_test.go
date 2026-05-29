package cli

import "testing"

func TestNewModelDefaults(t *testing.T) {
	m := newModel(80, 24)
	if m.width != 80 || m.height != 24 {
		t.Fatalf("size = %dx%d, want 80x24", m.width, m.height)
	}
	if m.input.Value() != "" {
		t.Errorf("input should start empty, got %q", m.input.Value())
	}
	if len(m.history) != 0 {
		t.Errorf("history should start empty, got %d entries", len(m.history))
	}
	if m.streaming {
		t.Errorf("model should not start in streaming state")
	}
}

func TestInitReturnsCmd(t *testing.T) {
	m := newModel(80, 24)
	if cmd := m.Init(); cmd == nil {
		t.Errorf("Init should return a focus command, got nil")
	}
}
