package theme

import (
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed builtins/*.yaml
var builtinFS embed.FS

// LoadBuiltins parses every embedded YAML in builtins/ and registers it. The
// returned slice is ordered alphabetically by file name so List()/Cycle()
// produce a stable order across builds.
func LoadBuiltins() []*Theme {
	entries, err := fs.ReadDir(builtinFS, "builtins")
	if err != nil {
		slog.Error("theme: read embedded builtins", "err", err)
		return nil
	}
	var fileNames []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		fileNames = append(fileNames, e.Name())
	}
	fileNames = sortNames(fileNames)
	out := make([]*Theme, 0, len(fileNames))
	for _, fn := range fileNames {
		data, err := fs.ReadFile(builtinFS, "builtins/"+fn)
		if err != nil {
			slog.Warn("theme: read builtin", "file", fn, "err", err)
			continue
		}
		var t Theme
		if err := yaml.Unmarshal(data, &t); err != nil {
			slog.Warn("theme: parse builtin", "file", fn, "err", err)
			continue
		}
		if err := validateLoaded(&t, fn); err != nil {
			slog.Warn("theme: invalid builtin", "file", fn, "err", err)
			continue
		}
		Register(&t)
		out = append(out, &t)
	}
	return out
}

// validateLoaded enforces that every required field is set; ships a clear
// error message that includes the file name so a broken built-in is easy
// to find.
func validateLoaded(t *Theme, source string) error {
	missing := []string{}
	check := func(name, v string) {
		if v == "" {
			missing = append(missing, name)
		}
	}
	check("name", t.Name)
	check("foreground", t.Foreground)
	check("dim", t.Dim)
	check("separator", t.Separator)
	check("accent", t.Accent)
	check("ok", t.OK)
	check("amber", t.Amber)
	check("red", t.Red)
	check("user_prefix", t.UserPrefix)
	check("assistant_text", t.AssistantText)
	check("sys_text", t.SysText)
	check("code_bg", t.CodeBG)
	check("gauge_filled", t.GaugeFilled)
	check("gauge_empty", t.GaugeEmpty)
	check("markdown", t.Markdown)
	if len(missing) > 0 {
		return fmt.Errorf("%s: missing fields %v", source, missing)
	}
	return nil
}
