package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePlugin(t *testing.T, root, name, manifestJSON string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if manifestJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(manifestJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestReadManifestHappy(t *testing.T) {
	dir := writePlugin(t, t.TempDir(), "my-plugin", `{
		"name": "my-plugin",
		"version": "1.2.3",
		"description": "A nice plugin",
		"author": {"name":"Alice","email":"a@x","url":"https://a"},
		"homepage": "https://example.com",
		"unknownField": 42
	}`)

	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if m.Name != "my-plugin" || m.Version != "1.2.3" || m.Description != "A nice plugin" {
		t.Errorf("manifest fields = %+v", m)
	}
	if m.Author.Name != "Alice" || m.Author.URL != "https://a" {
		t.Errorf("author = %+v", m.Author)
	}
	if m.Homepage != "https://example.com" {
		t.Errorf("homepage = %q", m.Homepage)
	}
}

func TestReadManifestMissingFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "no-manifest")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(dir); err == nil {
		t.Fatal("expected error for missing manifest")
	}
}

func TestReadManifestBadJSON(t *testing.T) {
	dir := writePlugin(t, t.TempDir(), "bad", `{not json`)
	if _, err := ReadManifest(dir); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestReadManifestRequiresName(t *testing.T) {
	dir := writePlugin(t, t.TempDir(), "nameless", `{"version":"1.0.0"}`)
	if _, err := ReadManifest(dir); err == nil {
		t.Fatal("expected error when name is missing")
	}
}

func TestReadMCPServersHappy(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "p", `{"name":"p"}`)
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{
		"mcpServers": {
			"local-db": {"command":"./bin/db","args":["--port=5432"]},
			"remote-api": {"url":"https://api.example.com/mcp","headers":{"X-Auth":"k"}}
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	srvs := ReadMCPServers(dir, "p")
	if len(srvs) != 2 {
		t.Fatalf("len = %d, want 2 (%+v)", len(srvs), srvs)
	}
	byName := map[string]int{srvs[0].Name: 0, srvs[1].Name: 1}
	db := srvs[byName["local-db"]]
	if db.Command != "./bin/db" || len(db.Args) != 1 || db.Args[0] != "--port=5432" {
		t.Errorf("local-db = %+v", db)
	}
	api := srvs[byName["remote-api"]]
	if api.URL != "https://api.example.com/mcp" || api.Headers["X-Auth"] != "k" {
		t.Errorf("remote-api = %+v", api)
	}
}

func TestReadMCPServersMissing(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "p", `{"name":"p"}`)
	if got := ReadMCPServers(dir, "p"); got != nil {
		t.Errorf("missing .mcp.json should return nil, got %+v", got)
	}
}

func TestReadMCPServersBadJSON(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "p", `{"name":"p"}`)
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{bogus`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadMCPServers(dir, "p"); got != nil {
		t.Errorf("bad JSON should log+return nil, got %+v", got)
	}
}

func TestReadLSPServersServersShape(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "p", `{"name":"p"}`)
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "lsp.json"), []byte(`{
		"servers": {
			"gopls": {"command":"gopls","args":["serve"],"languages":["go"],"rootPatterns":["go.mod"]}
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	srvs := ReadLSPServers(dir, "p")
	if len(srvs) != 1 || srvs[0].Name != "gopls" || srvs[0].Command != "gopls" {
		t.Fatalf("got %+v", srvs)
	}
	if len(srvs[0].Languages) != 1 || srvs[0].Languages[0] != "go" {
		t.Errorf("languages = %v", srvs[0].Languages)
	}
}

func TestReadLSPServersClaudeShape(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "p", `{"name":"p"}`)
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "lsp.json"), []byte(`{
		"go": {"command":"gopls","args":["serve"],"extensionToLanguage":{".go":"go"}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	srvs := ReadLSPServers(dir, "p")
	if len(srvs) != 1 || srvs[0].Name != "go" || srvs[0].Command != "gopls" {
		t.Fatalf("got %+v", srvs)
	}
	if len(srvs[0].Languages) != 1 || srvs[0].Languages[0] != "go" {
		t.Errorf("languages = %v", srvs[0].Languages)
	}
}

func TestReadLSPServersMissing(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "p", `{"name":"p"}`)
	if got := ReadLSPServers(dir, "p"); got != nil {
		t.Errorf("missing lsp.json should return nil, got %+v", got)
	}
}

func TestReadHooksHappy(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "p", `{"name":"p"}`)
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"hooks": {
		"PreToolUse": [
			{"matcher":"Bash","hooks":[{"type":"command","command":"/bin/sh -c \"policy.sh\"","timeout":7}]}
		],
		"Stop": [
			{"matcher":"*","hooks":[{"type":"command","command":"/bin/echo done"}]}
		],
		"NotificationEvent": [
			{"hooks":[{"type":"command","command":"/bin/true"}]}
		],
		"PostToolUse": [
			{"matcher":"Bash|Write","hooks":[{"type":"prompt","command":"unused"}]}
		]
	}}`
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "hooks.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	hs := ReadHooks(dir, "p")
	if len(hs) != 2 {
		t.Fatalf("len = %d, want 2 (%+v)", len(hs), hs)
	}
	got := map[string]HookEntry{}
	for _, h := range hs {
		got[h.Event] = h
	}
	if pre := got["PreToolUse"]; pre.Matcher["tool"] != "Bash" || pre.TimeoutSeconds != 7 || pre.Source != "p" {
		t.Errorf("PreToolUse = %+v", pre)
	}
	if stop := got["Stop"]; len(stop.Matcher) != 0 || stop.TimeoutSeconds != 5 {
		t.Errorf("Stop = %+v (matcher should be empty for *, timeout default 5)", stop)
	}
	if len(got["PreToolUse"].Command) < 1 || got["PreToolUse"].Command[0] != "/bin/sh" {
		t.Errorf("PreToolUse.Command not shell-split: %+v", got["PreToolUse"].Command)
	}
}

func TestReadHooksMissing(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "p", `{"name":"p"}`)
	if got := ReadHooks(dir, "p"); got != nil {
		t.Errorf("missing hooks.json should return nil, got %+v", got)
	}
}

func TestReadCommandsHappy(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "p", `{"name":"p"}`)
	cmdDir := filepath.Join(dir, "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	deploy := `---
description: Deploy to prod
argument-hint: [env]
allowed-tools: Bash
---
Deploy $ARGUMENTS following our checklist.`
	if err := os.WriteFile(filepath.Join(cmdDir, "deploy.md"), []byte(deploy), 0o644); err != nil {
		t.Fatal(err)
	}
	bare := `Plain body, no frontmatter.`
	if err := os.WriteFile(filepath.Join(cmdDir, "bare.md"), []byte(bare), 0o644); err != nil {
		t.Fatal(err)
	}
	cmds := ReadCommands(dir, "p")
	if len(cmds) != 2 {
		t.Fatalf("len = %d, want 2 (%+v)", len(cmds), cmds)
	}
	byName := map[string]PluginCommand{cmds[0].Name: cmds[0], cmds[1].Name: cmds[1]}
	dep := byName["deploy"]
	if dep.Description != "Deploy to prod" || dep.Source != "p" {
		t.Errorf("deploy = %+v", dep)
	}
	if !strings.Contains(dep.Prompt, "Deploy $ARGUMENTS") {
		t.Errorf("deploy.Prompt missing body: %q", dep.Prompt)
	}
	if byName["bare"].Prompt != "Plain body, no frontmatter." {
		t.Errorf("bare body = %q", byName["bare"].Prompt)
	}
}

func TestReadCommandsMissingDir(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "p", `{"name":"p"}`)
	if got := ReadCommands(dir, "p"); got != nil {
		t.Errorf("missing commands/ should return nil, got %+v", got)
	}
}

func TestExpandArgumentsPresent(t *testing.T) {
	got := ExpandArguments("Deploy $ARGUMENTS now.", "prod")
	if got != "Deploy prod now." {
		t.Errorf("got %q", got)
	}
}

func TestExpandArgumentsAbsentAppends(t *testing.T) {
	got := ExpandArguments("Just do it.", "fast")
	want := "Just do it.\n\nARGUMENTS: fast"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandArgumentsAbsentNoArgsUnchanged(t *testing.T) {
	got := ExpandArguments("Hello.", "")
	if got != "Hello." {
		t.Errorf("got %q", got)
	}
}
