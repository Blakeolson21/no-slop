package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/lanehealth"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// The pipeline agent must consume persisted lane health, otherwise every run
// rediscovers an exhausted lane by spawning it - the 2026-08-04 incident, where
// a dozen consecutive runs each failed on the same dead Codex quota. Marking
// every lane also proves the terminal message names each lane's reset time
// instead of failing bare.
func TestNewPipelineAgentSkipsQuotaExhaustedLanesAndNamesEveryResetTime(t *testing.T) {
	now := time.Now()
	store := lanehealth.NewStore(
		filepath.Join(t.TempDir(), "lane-health.json"),
		func() time.Time { return now },
	)
	codexUntil := now.Add(72 * time.Hour)
	claudeUntil := now.Add(4 * time.Hour)
	models := map[types.AgentName]string{
		types.AgentCodex:  "test-codex-model",
		types.AgentClaude: "test-claude-model",
	}
	for name, model := range models {
		until := codexUntil
		reason := "You've hit your usage limit"
		if name == types.AgentClaude {
			until = claudeUntil
			reason = "You've hit your session limit"
		}
		outage := testLaneOutage(t, name, model, until, reason)
		if err := store.Mark(outage); err != nil {
			t.Fatalf("Mark %s: %v", name, err)
		}
	}

	// Point both lanes at binaries that cannot exist, so if the cooldown were
	// NOT consumed the failure would be a spawn error naming those paths.
	missing := filepath.Join(t.TempDir(), "definitely-not-installed")
	cfg := &config.Config{
		Agent:  types.AgentCodex,
		Agents: []types.AgentName{types.AgentCodex, types.AgentClaude},
		AgentArgsOverride: map[string][]string{
			string(types.AgentCodex):  {"--model", models[types.AgentCodex]},
			string(types.AgentClaude): {"--model", models[types.AgentClaude]},
		},
		AgentPathOverride: map[string]string{
			string(types.AgentCodex):  missing,
			string(types.AgentClaude): missing,
		},
	}
	ag, err := newPipelineAgent(context.Background(), cfg, fakeLookPath, store)
	if err != nil {
		t.Fatalf("newPipelineAgent: %v", err)
	}
	defer func() { _ = ag.Close() }()

	_, runErr := ag.Run(context.Background(), agent.RunOpts{Prompt: "x", CWD: t.TempDir()})
	if runErr == nil {
		t.Fatalf("expected the run to fail with every lane exhausted")
	}
	msg := runErr.Error()
	if !strings.Contains(msg, "every configured agent lane is quota-exhausted") {
		t.Fatalf("error %q must report that no lane can run", msg)
	}
	for _, want := range []string{
		"codex account ",
		"model " + models[types.AgentCodex] + " until " + codexUntil.Local().Format("2006-01-02 15:04 MST"),
		"claude account ",
		"model " + models[types.AgentClaude] + " until " + claudeUntil.Local().Format("2006-01-02 15:04 MST"),
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q must contain %q", msg, want)
		}
	}
	if strings.Contains(msg, missing) {
		t.Fatalf("no marked lane may be spawned, but the error names the binary: %q", msg)
	}
}

// Account selection has to precede the health lookup. An old account/model
// mark must not suppress a different Quartermaster seat before its identity is
// known.
func TestNewPipelineAgentResolvesLaneHealthAfterQuartermasterSelectsAccount(t *testing.T) {
	now := time.Now()
	store := lanehealth.NewStore(
		filepath.Join(t.TempDir(), "lane-health.json"),
		func() time.Time { return now },
	)
	codexUntil := now.Add(72 * time.Hour)
	model := "test-codex-model"
	if err := store.Mark(testLaneOutage(t, types.AgentCodex, model, codexUntil, "You've hit your usage limit")); err != nil {
		t.Fatalf("Mark: %v", err)
	}

	missing := filepath.Join(t.TempDir(), "definitely-not-installed")
	missingQuartermaster := filepath.Join(t.TempDir(), "definitely-no-quartermaster")
	cfg := &config.Config{
		Agent:  types.AgentCodex,
		Agents: []types.AgentName{types.AgentCodex},
		AgentArgsOverride: map[string][]string{
			string(types.AgentCodex): {"--model", model},
		},
		AgentPathOverride: map[string]string{
			string(types.AgentCodex): missing,
		},
		Quartermaster: config.Quartermaster{
			Enabled: true,
			Bin:     missingQuartermaster,
			TTL:     30 * time.Minute,
			Weight:  1,
		},
	}
	ag, err := newPipelineAgent(context.Background(), cfg, fakeLookPath, store)
	if err != nil {
		t.Fatalf("newPipelineAgent: %v", err)
	}
	defer func() { _ = ag.Close() }()

	_, runErr := ag.Run(context.Background(), agent.RunOpts{Prompt: "x", CWD: t.TempDir()})
	if runErr == nil || !agent.IsQuartermasterRefusal(runErr) {
		t.Fatalf("account selection must run before scoped health lookup, got %v", runErr)
	}
	if agent.IsQuotaOutage(runErr) {
		t.Fatalf("the pre-lease account mark must not suppress another account: %v", runErr)
	}
	if !strings.Contains(runErr.Error(), missingQuartermaster) {
		t.Fatalf("expected account admission before health lookup, got %v", runErr)
	}
}

func testLaneOutage(t *testing.T, name types.AgentName, model string, until time.Time, reason string) lanehealth.Outage {
	t.Helper()
	configured, err := agent.NewWithOptions(name, string(name), []string{"--model", model}, agent.Options{})
	if err != nil {
		t.Fatalf("NewWithOptions(%s): %v", name, err)
	}
	scope, ok := agent.ResolveQuotaScope(configured, agent.RunOpts{})
	_ = configured.Close()
	if !ok {
		t.Fatalf("could not resolve test scope for %s", name)
	}
	return lanehealth.Outage{
		ScopeKey: scope.Key, AccountID: scope.AccountID, Model: scope.Model,
		Lane: string(name), Until: until, Reason: reason,
	}
}

func TestNewPipelineAgentToleratesNoLaneHealthStore(t *testing.T) {
	cfg := &config.Config{Agent: types.AgentCodex}
	ag, err := newPipelineAgent(context.Background(), cfg, fakeLookPath, nil)
	if err != nil {
		t.Fatalf("newPipelineAgent without a store: %v", err)
	}
	_ = ag.Close()
}
