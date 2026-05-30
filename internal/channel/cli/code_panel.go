package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// extensionByLang maps fenced code-block language tags to file extensions so
// the temp file the pager/editor opens triggers their native syntax
// highlighting (vim, less with src-hilite, bat, etc.).
var extensionByLang = map[string]string{
	"go": "go", "golang": "go",
	"py": "py", "python": "py",
	"js": "js", "javascript": "js",
	"ts": "ts", "typescript": "ts",
	"tsx": "tsx", "jsx": "jsx",
	"rs": "rs", "rust": "rs",
	"java": "java",
	"c":    "c", "h": "h",
	"cpp": "cpp", "cc": "cc", "hpp": "hpp",
	"cs": "cs", "csharp": "cs",
	"rb": "rb", "ruby": "rb",
	"php": "php",
	"sh":  "sh", "bash": "sh", "zsh": "sh",
	"fish": "fish",
	"yaml": "yaml", "yml": "yaml",
	"json": "json",
	"xml":  "xml",
	"html": "html",
	"css":  "css",
	"scss": "scss",
	"sql":  "sql",
	"md":   "md", "markdown": "md",
	"toml":  "toml",
	"lua":   "lua",
	"swift": "swift",
	"kt":    "kt", "kotlin": "kt",
	"dockerfile": "Dockerfile",
}

// writeScratchFile writes payload to ~/.cax/scratch/<timestamp>.<ext> so the
// chosen viewer can open a real file (and the user can keep / re-open it).
// Returns the absolute path.
func writeScratchFile(payload, lang string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.TempDir()
	}
	dir := filepath.Join(home, ".cax", "scratch")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create scratch dir: %w", err)
	}
	ext := "txt"
	if e, ok := extensionByLang[strings.ToLower(strings.TrimSpace(lang))]; ok {
		ext = e
	}
	name := fmt.Sprintf("%s.%s", time.Now().Format("2006-01-02_15-04-05"), ext)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		return "", fmt.Errorf("write scratch file: %w", err)
	}
	return path, nil
}

// resolveCodeViewer picks the program to open the scratch file in.
// Order: $PAGER, less, bat, $EDITOR, $VISUAL, vim, vi, nano, more, cat.
// less gets "-R" (ANSI passthrough) + "-X" (don't clear the screen on exit,
// so any selection the user made stays on screen after they quit).
func resolveCodeViewer() (string, []string) {
	if p := strings.TrimSpace(os.Getenv("PAGER")); p != "" {
		fields := strings.Fields(p)
		return fields[0], fields[1:]
	}
	if _, err := exec.LookPath("less"); err == nil {
		return "less", []string{"-R", "-X"}
	}
	if _, err := exec.LookPath("bat"); err == nil {
		return "bat", []string{"--style=plain", "--paging=always"}
	}
	for _, e := range []string{"EDITOR", "VISUAL"} {
		if v := strings.TrimSpace(os.Getenv(e)); v != "" {
			fields := strings.Fields(v)
			return fields[0], fields[1:]
		}
	}
	for _, c := range []string{"vim", "vi", "nano", "more", "cat"} {
		if _, err := exec.LookPath(c); err == nil {
			return c, nil
		}
	}
	return "", nil
}

// teaExecProcess builds a tea.Cmd that suspends the TUI, runs the chosen
// viewer over the scratch file, then resumes the TUI and appends a sys
// notice describing what happened.
func teaExecProcess(bin string, args []string, label, path string) tea.Cmd {
	c := exec.Command(bin, args...)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return sysHistoryMsg{text: fmt.Sprintf("✗ viewer failed (%s): %v", bin, err)}
		}
		return sysHistoryMsg{text: fmt.Sprintf("📄 opened %s in %s · saved to %s", label, bin, path)}
	})
}
