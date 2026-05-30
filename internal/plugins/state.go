package plugins

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// stateEntry is one record in ~/.cax/plugins.json.
type stateEntry struct {
	Enabled bool   `json:"enabled"`
	Source  string `json:"source"`
}

// state is the on-disk map: plugin name -> stateEntry.
type state map[string]stateEntry

// readState returns the parsed state. Missing file or corrupt JSON => empty
// state + slog.Warn (corruption never blocks discovery; users keep working).
func readState(path string) (state, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return state{}, nil
		}
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}
	out := state{}
	if err := json.Unmarshal(data, &out); err != nil {
		slog.Warn("plugins: state file corrupt; falling back to empty", "path", path, "error", err)
		return state{}, nil
	}
	return out, nil
}

// writeState serializes the state to a temp file in the same dir and renames
// it over the target for atomicity. Creates parent dirs as needed.
func writeState(path string, s state) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir for state: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".plugins.*.json")
	if err != nil {
		return fmt.Errorf("temp state file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temp state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp state: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename state: %w", err)
	}
	return nil
}
