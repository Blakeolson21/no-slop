package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/lanehealth"
)

func scopedTestOutage(scope QuotaScope, lane string, until, observed time.Time) lanehealth.Outage {
	return lanehealth.Outage{
		ScopeKey:   scope.Key,
		AccountID:  scope.AccountID,
		Model:      scope.Model,
		Lane:       lane,
		Until:      until,
		ObservedAt: observed,
		Reason:     "You've hit your usage limit",
	}
}

func TestLaneHealthIsolatesOutagesByAccountAndModel(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	store := laneTestStore(t, &now)

	accountA := &fallbackTestAgent{
		name:  "codex",
		home:  t.TempDir(),
		model: "gpt-5.6-sol",
		run: func() (*Result, error) {
			return nil, errors.New(codexQuotaStderr)
		},
	}
	if _, err := WithLaneHealth(accountA, store, func() time.Time { return now }).Run(context.Background(), RunOpts{}); err == nil {
		t.Fatal("the first account/model invocation must surface the quota failure")
	}
	accountAScope, _ := accountA.QuotaScope(RunOpts{})
	if _, live := store.Outage(accountAScope.Key); !live {
		t.Fatal("the failed account/model was not marked")
	}

	accountB := &fallbackTestAgent{
		name:  "codex",
		home:  t.TempDir(),
		model: "gpt-5.6-sol",
		run:   func() (*Result, error) { return &Result{Text: "account B"}, nil },
	}
	if _, err := WithLaneHealth(accountB, store, func() time.Time { return now }).Run(context.Background(), RunOpts{}); err != nil {
		t.Fatalf("a different account on the same lane must remain runnable: %v", err)
	}

	otherModel := &fallbackTestAgent{
		name:  "codex",
		home:  accountA.home,
		model: "gpt-5.6-flash",
		run:   func() (*Result, error) { return &Result{Text: "other model"}, nil },
	}
	if _, err := WithLaneHealth(otherModel, store, func() time.Time { return now }).Run(context.Background(), RunOpts{}); err != nil {
		t.Fatalf("a different model on the same account must remain runnable: %v", err)
	}

	blockedSameScope := &fallbackTestAgent{
		name:  "codex",
		home:  accountA.home,
		model: "gpt-5.6-sol",
		run:   func() (*Result, error) { return &Result{Text: "must not run"}, nil },
	}
	if _, err := WithLaneHealth(blockedSameScope, store, func() time.Time { return now }).Run(context.Background(), RunOpts{}); err == nil {
		t.Fatal("the same account/model must stay parked inside its probe interval")
	}
	if blockedSameScope.calls != 0 {
		t.Fatalf("same account/model task calls = %d, want 0", blockedSameScope.calls)
	}
}

func TestLaneHealthUsesABoundedCheapProbeBeforeTheRequestedInvocation(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	store := laneTestStore(t, &now)
	inner := &fallbackTestAgent{
		name:  "codex",
		home:  t.TempDir(),
		model: "gpt-5.6-sol",
		run:   func() (*Result, error) { return &Result{Text: "requested task"}, nil },
		probe: func(ctx context.Context, opts RunOpts) (*Result, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("quota probe must have its own deadline")
			}
			if remaining := time.Until(deadline); remaining <= 0 || remaining > lanehealth.ProbeTimeout {
				t.Fatalf("quota probe deadline has %s remaining, want at most %s", remaining, lanehealth.ProbeTimeout)
			}
			if opts.Prompt != quotaProbePrompt {
				t.Errorf("probe prompt = %q, want the fixed cheap probe", opts.Prompt)
			}
			if len(opts.JSONSchema) != 0 || opts.Session != nil {
				t.Errorf("probe retained task schema/session: schema=%s session=%+v", opts.JSONSchema, opts.Session)
			}
			if opts.Purpose != "quota-probe" || opts.OnChunk != nil {
				t.Errorf("probe task metadata leaked: purpose=%q on_chunk=%t", opts.Purpose, opts.OnChunk != nil)
			}
			return &Result{Text: "OK"}, nil
		},
	}
	scope, _ := inner.QuotaScope(RunOpts{})
	observed := now.Add(-lanehealth.ProbeInterval)
	if err := store.Mark(scopedTestOutage(scope, inner.Name(), now.Add(24*time.Hour), observed)); err != nil {
		t.Fatalf("seed outage: %v", err)
	}
	now = now.Add(lanehealth.ProbeInterval)

	_, err := WithLaneHealth(inner, store, func() time.Time { return now }).Run(context.Background(), RunOpts{
		Prompt:     "perform the real task with its original prompt",
		JSONSchema: []byte(`{"type":"object"}`),
		Session:    &SessionRef{ID: "saved-session", Agent: "codex"},
	})
	if err != nil {
		t.Fatalf("recovered account must continue with the requested invocation: %v", err)
	}
	if len(inner.probes) != 1 || inner.calls != 1 {
		t.Fatalf("probe/task calls = %d/%d, want one each", len(inner.probes), inner.calls)
	}
	if got := inner.runOpts[0].Prompt; got != "perform the real task with its original prompt" {
		t.Fatalf("requested invocation prompt = %q", got)
	}
	if _, live := store.Outage(scope.Key); live {
		t.Fatal("successful probe must clear the recovered account/model mark")
	}
}

func TestCodexQuotaScopeUsesResolvedHomeAndConfiguredModel(t *testing.T) {
	firstHome := t.TempDir()
	secondHome := t.TempDir()
	for _, home := range []string{firstHome, secondHome} {
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(`model = "gpt-5.6-sol"`), 0o644); err != nil {
			t.Fatalf("write codex config: %v", err)
		}
	}
	first := &codexAgent{bin: "codex"}
	one, ok := first.QuotaScope(RunOpts{Env: []string{"CODEX_HOME=" + firstHome}})
	if !ok || one.Model != "gpt-5.6-sol" {
		t.Fatalf("first scope = %+v, resolved %t", one, ok)
	}
	two, ok := first.QuotaScope(RunOpts{Env: []string{"CODEX_HOME=" + secondHome}})
	if !ok || one.AccountID == two.AccountID || one.Key == two.Key {
		t.Fatalf("different homes must produce different account scopes: first=%+v second=%+v", one, two)
	}
	otherModel := &codexAgent{bin: "codex", extraArgs: []string{"--model", "gpt-5.6-flash"}}
	three, ok := otherModel.QuotaScope(RunOpts{Env: []string{"CODEX_HOME=" + firstHome}})
	if !ok || one.AccountID != three.AccountID || one.Model == three.Model || one.Key == three.Key {
		t.Fatalf("different models must produce separate scopes: first=%+v other=%+v", one, three)
	}
	if strings.Contains(one.AccountID, firstHome) {
		t.Fatalf("account identity persisted the resolved home path: %q", one.AccountID)
	}

	relativeHome := filepath.Join(firstHome, "relative-codex-home")
	if err := os.Mkdir(relativeHome, 0o755); err != nil {
		t.Fatalf("make relative home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(relativeHome, "config.toml"), []byte(`model = "gpt-5.6-sol"`), 0o644); err != nil {
		t.Fatalf("write relative codex config: %v", err)
	}
	relative, ok := first.QuotaScope(RunOpts{
		CWD: firstHome,
		Env: []string{"CODEX_HOME=relative-codex-home"},
	})
	absolute, absoluteOK := first.QuotaScope(RunOpts{Env: []string{"CODEX_HOME=" + relativeHome}})
	if !ok || !absoluteOK || relative.AccountID != absolute.AccountID || relative.Model != one.Model {
		t.Fatalf("relative CODEX_HOME must resolve config and identity from invocation cwd: %+v, scoped %t", relative, ok)
	}
}

func TestClaudeQuotaScopeUsesLeasedHomeAndModelOverride(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "settings.local.json"), []byte(`{"model":"claude-opus-4"}`), 0o644); err != nil {
		t.Fatalf("write claude config: %v", err)
	}
	claude := &claudeAgent{bin: "claude"}
	configured, ok := claude.QuotaScope(RunOpts{Env: []string{"CLAUDE_CONFIG_DIR=" + home}})
	if !ok || configured.Model != "claude-opus-4" {
		t.Fatalf("configured Claude scope = %+v, scoped %t", configured, ok)
	}
	overridden, ok := claude.QuotaScope(RunOpts{
		Env: []string{"CLAUDE_CONFIG_DIR=" + home, "ANTHROPIC_MODEL=claude-sonnet-4"},
	})
	if !ok || overridden.AccountID != configured.AccountID || overridden.Model != "claude-sonnet-4" || overridden.Key == configured.Key {
		t.Fatalf("model override must get a separate scope on the leased home: configured=%+v override=%+v", configured, overridden)
	}
}
