package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// writeThemeState atomically updates the "theme" field of the JSON state file
// at path, preserving any other top-level fields already present. The write
// uses temp-file + rename so a crash mid-write leaves the previous content
// intact (mirrors internal/plugins/state.go's atomic write pattern).
func writeThemeState(path, themeName string) error {
	if path == "" {
		return fmt.Errorf("write theme state: empty path")
	}
	state := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		// Tolerate corruption: if the existing file is invalid JSON we
		// overwrite it cleanly rather than failing the Ctrl+T cycle.
		_ = json.Unmarshal(data, &state)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read state %s: %w", path, err)
	}
	state["theme"] = themeName

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir state: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".state.*.json")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(state); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("encode state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("rename temp -> %s: %w", path, err)
	}
	return nil
}
