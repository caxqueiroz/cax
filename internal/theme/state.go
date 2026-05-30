package theme

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// stateFile is the on-disk shape of ~/.cax/state.json. Only the theme name
// is persisted today; future fields can be added without migrating users.
type stateFile struct {
	Theme string `json:"theme,omitempty"`
}

// readStateTheme returns the persisted theme name. Missing or corrupt files
// return "" (silent recovery) so the resolver can fall through to defaults.
func readStateTheme(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Debug("theme: read state", "path", path, "err", err)
		}
		return ""
	}
	var s stateFile
	if err := json.Unmarshal(data, &s); err != nil {
		slog.Warn("theme: state.json corrupt, ignoring", "path", path, "err", err)
		return ""
	}
	return s.Theme
}

// writeStateTheme persists the active theme name atomically (write tmp +
// rename). The parent directory is created if missing.
func writeStateTheme(path, name string) error {
	if path == "" {
		return fmt.Errorf("theme: empty state file path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	data, err := json.Marshal(stateFile{Theme: name})
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "state-*.tmp")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// StateFile is the public helper view.go / commands.go use to compute the
// path. It joins ~/.cax/state.json or "" if the home dir can't be found.
func StateFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cax", "state.json")
}

// WriteActive persists the currently active theme to path. Convenience for
// /theme handlers + Ctrl+T.
func WriteActive(path string) error {
	a := Active()
	if a == nil {
		return nil
	}
	return writeStateTheme(path, a.Name)
}
