package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeQuotaScopeIgnoresInheritedCodexHome(t *testing.T) {
	home := t.TempDir()
	claudeHome := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeHome, 0o755); err != nil {
		t.Fatalf("make Claude home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(claudeHome, "settings.local.json"), []byte(`{"model":"claude-sonnet-4"}`), 0o644); err != nil {
		t.Fatalf("write Claude config: %v", err)
	}
	codexHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(`model = "gpt-5.6-sol"`), 0o644); err != nil {
		t.Fatalf("write Codex config: %v", err)
	}

	scope, ok := (&claudeAgent{bin: "claude"}).QuotaScope(RunOpts{Env: []string{
		"HOME=" + home,
		"CODEX_HOME=" + codexHome,
	}})
	want, _ := accountModelScope("claude", claudeHome, "claude-sonnet-4", "")
	if !ok || scope.AccountID != want.AccountID || scope.Model != want.Model || scope.Key != want.Key {
		t.Fatalf("Claude scope = %+v, resolved %t; want its configured home/model %+v", scope, ok, want)
	}
}

func TestCodexQuotaScopeParsesModelWithInlineComment(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(`model = "gpt-5.6-sol" # provider default`), 0o644); err != nil {
		t.Fatalf("write Codex config: %v", err)
	}

	scope, ok := (&codexAgent{bin: "codex"}).QuotaScope(RunOpts{Env: []string{"CODEX_HOME=" + home}})
	if !ok || scope.Model != "gpt-5.6-sol" {
		t.Fatalf("Codex scope = %+v, resolved %t; want model gpt-5.6-sol", scope, ok)
	}
}

func TestCodexQuotaScopeParsesProfileAfterCommentedTableHeader(t *testing.T) {
	home := t.TempDir()
	config := `model = "gpt-base"
[profiles.other]
model = "gpt-other"
[profiles.work] # selected profile
model = "gpt-work"
`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write Codex config: %v", err)
	}

	scope, ok := (&codexAgent{bin: "codex", extraArgs: []string{"--profile", "work"}}).QuotaScope(RunOpts{Env: []string{"CODEX_HOME=" + home}})
	if !ok || scope.Model != "gpt-work" {
		t.Fatalf("Codex scope = %+v, resolved %t; want selected profile model gpt-work", scope, ok)
	}
}
