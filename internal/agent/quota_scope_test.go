package agent

import (
	"context"
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

func TestQuotaScopeUsesResolvedExecutableProviderInsteadOfConfiguredLane(t *testing.T) {
	codexHome := t.TempDir()
	claudeHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(`model = "gpt-codex"`), 0o644); err != nil {
		t.Fatalf("write Codex config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(claudeHome, "settings.local.json"), []byte(`{"model":"claude-opus"}`), 0o644); err != nil {
		t.Fatalf("write Claude config: %v", err)
	}
	codexBin := filepath.Join(t.TempDir(), identityExecutableName("codex-gate-seat"))
	if err := os.WriteFile(codexBin, []byte("codex executable fixture"), 0o755); err != nil {
		t.Fatalf("write executable fixture: %v", err)
	}

	// This adapter is configured as Claude, but its executable override runs
	// Codex. The Codex home/model must determine the persisted scope.
	scope, ok := (&claudeAgent{bin: codexBin}).QuotaScope(RunOpts{Env: []string{
		"CODEX_HOME=" + codexHome,
		"CLAUDE_CONFIG_DIR=" + claudeHome,
	}})
	want, _ := accountModelScope("codex", codexHome, "gpt-codex", "")
	if !ok || scope.Key != want.Key || scope.AccountID != want.AccountID || scope.Model != want.Model {
		t.Fatalf("resolved scope = %+v, %t; want Codex executable scope %+v", scope, ok, want)
	}
}

func TestQuotaScopeDoesNotGuessProviderForUnknownExecutableOverride(t *testing.T) {
	claudeHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(claudeHome, "settings.local.json"), []byte(`{"model":"claude-opus"}`), 0o644); err != nil {
		t.Fatalf("write Claude config: %v", err)
	}
	unknownBin := filepath.Join(t.TempDir(), identityExecutableName("custom-agent-wrapper"))
	if err := os.WriteFile(unknownBin, []byte("unknown executable fixture"), 0o755); err != nil {
		t.Fatalf("write executable fixture: %v", err)
	}

	if scope, ok := (&claudeAgent{bin: unknownBin}).QuotaScope(RunOpts{Env: []string{
		"CLAUDE_CONFIG_DIR=" + claudeHome,
	}}); ok {
		t.Fatalf("unknown executable must not inherit the configured Claude identity: %+v", scope)
	}
}

func TestQuotaProbeRequiresResolvedModel(t *testing.T) {
	called := false
	_, err := runQuotaProbeOnce(context.Background(), &fallbackTestAgent{name: "codex"}, RunOpts{}, func(context.Context, RunOpts) (*Result, error) {
		called = true
		return &Result{Text: "OK"}, nil
	})
	if err == nil || called {
		t.Fatalf("quota probe without a resolved model: err=%v, runner called=%t; want fail closed before invocation", err, called)
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
