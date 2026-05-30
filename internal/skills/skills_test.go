package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/caxqueiroz/czcli/internal/config"
)

func writeSkill(t *testing.T, dir, name, description string) {
	t.Helper()
	sd := filepath.Join(dir, name)
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", sd, err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(sd, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
}

func TestLoadDiscoversSkillsFromCfgAndExtraDirs(t *testing.T) {
	projDir := t.TempDir()
	extra := t.TempDir()
	writeSkill(t, projDir, "alpha", "Alpha skill")
	writeSkill(t, extra, "beta", "Beta skill")

	res, err := Load(config.SkillsConfig{Enabled: true, Dirs: []string{projDir}}, []string{extra})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res == nil || res.Loader == nil {
		t.Fatal("Load returned nil result/loader")
	}
	gotNames := map[string]bool{}
	for _, n := range res.Names {
		gotNames[n] = true
	}
	if !gotNames["alpha"] || !gotNames["beta"] {
		t.Errorf("Names = %v, want alpha+beta", res.Names)
	}
}

func TestLoadDisabledReturnsEmpty(t *testing.T) {
	res, err := Load(config.SkillsConfig{Enabled: false}, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res == nil {
		t.Fatal("Load returned nil result when disabled")
	}
	if len(res.Names) != 0 {
		t.Errorf("Names = %v, want empty", res.Names)
	}
	if res.Loader == nil {
		t.Errorf("Loader should be non-nil even when disabled (extension still cleans stale catalog)")
	}
}
