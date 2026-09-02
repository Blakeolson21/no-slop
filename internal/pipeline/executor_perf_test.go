package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// usageAgent is a minimal agent that reports token usage and echoes session
// starts, for perf-recording tests.
type usageAgent struct{ resumable bool }

func (u *usageAgent) Name() string                { return "usage-agent" }
func (u *usageAgent) Close() error                { return nil }
func (u *usageAgent) SupportsSessionResume() bool { return u.resumable }

func (u *usageAgent) Run(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
	result := &agent.Result{
		Output: json.RawMessage(`{}`),
		Model:  "test-model-1",
		Usage:  agent.TokenUsage{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 60},
	}
	if opts.Session != nil {
		if opts.Session.ID != "" {
			result.SessionID = opts.Session.ID
			result.Resumed = true
		} else {
			result.SessionID = "sess-new"
		}
	}
	return result, nil
}

// TestPerfRecordingAgent_RecordsResolvedSymlinkTargetAndUnknownModel executes
// a Claude-compatible wrapper through a symlink and proves the durable row
// describes what actually launched. The helper deliberately reports no model,
// so the row must preserve unknown instead of inferring "claude".
func TestPerfRecordingAgent_RecordsResolvedSymlinkTargetAndUnknownModel(t *testing.T) {
	database, _, run, _ := setupTest(t)
	t.Setenv("NS_REVIEW_IDENTITY_HELPER", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "configured-claude-wrapper")
	if err := os.Symlink(executable, link); err != nil {
		t.Fatal(err)
	}
	inner, err := agent.New(types.AgentClaude, link, []string{
		"-test.run=^TestReviewerIdentityHelperProcess$", "--",
		"--model", "z-ai/glm-5.3-flash",
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapper := &perfRecordingAgent{
		inner: inner, db: database, runID: run.ID, stepName: types.StepReview,
		round: func() int { return 1 },
	}
	if _, err := wrapper.Run(context.Background(), agent.RunOpts{Purpose: "review"}); err != nil {
		t.Fatalf("run wrapper: %v", err)
	}

	invs, err := database.GetAgentInvocationsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(invs) != 1 {
		t.Fatalf("got %d rows, want 1", len(invs))
	}
	got := invs[0]
	realExecutable, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != "claude" || got.ResolvedExecutable == nil || *got.ResolvedExecutable != realExecutable {
		t.Fatalf("recorded agent/executable = %q/%v, want claude/%q", got.Agent, got.ResolvedExecutable, realExecutable)
	}
	if !reflect.DeepEqual(got.ModelArgs, []string{"--model", "z-ai/glm-5.3-flash"}) {
		t.Fatalf("model args = %#v", got.ModelArgs)
	}
	if got.Model != nil {
		t.Fatalf("unreported model must be unknown, got %q", *got.Model)
	}
}

func TestReviewerIdentityHelperProcess(t *testing.T) {
	if os.Getenv("NS_REVIEW_IDENTITY_HELPER") != "1" {
		return
	}
	fmt.Fprintln(os.Stdout, `{"type":"assistant","session_id":"wrapper-session","message":{"usage":{"input_tokens":1,"output_tokens":1},"content":[{"type":"text","text":"ok"}]}}`)
	fmt.Fprintln(os.Stdout, `{"type":"result","subtype":"success","session_id":"wrapper-session","structured_output":{}}`)
}

type fallbackUsageAgent struct {
	name   string
	result *agent.Result
	err    error
}

func (a *fallbackUsageAgent) Name() string { return a.name }

func (a *fallbackUsageAgent) Run(context.Context, agent.RunOpts) (*agent.Result, error) {
	return a.result, a.err
}

func (a *fallbackUsageAgent) Close() error { return nil }

// TestExecutor_RecordsAgentInvocationsLocally proves every agent invocation
// produces one local agent_invocations row carrying run/step identity,
// purpose, round, session mode, model, timing, and token usage - and that
// the raw session id never lands in the telemetry row.
func TestExecutor_RecordsAgentInvocationsLocally(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			if _, err := sctx.RunAgentSession(SessionRoleReviewer, agent.RunOpts{Prompt: "review", Purpose: "review"}); err != nil {
				return nil, err
			}
			if _, err := sctx.Agent.Run(sctx.Ctx, agent.RunOpts{Prompt: "evidence"}); err != nil {
				return nil, err
			}
			return &StepOutcome{}, nil
		},
	}

	cfg := &config.Config{Agent: types.AgentClaude, SessionReuse: true}
	exec := NewExecutor(database, p, cfg, &usageAgent{resumable: true}, []Step{step}, nil)
	if err := exec.Execute(context.Background(), run, repo, workDir); err != nil {
		t.Fatalf("execute: %v", err)
	}

	invocations, err := database.GetAgentInvocationsByRun(run.ID)
	if err != nil {
		t.Fatalf("get invocations: %v", err)
	}
	if len(invocations) != 2 {
		t.Fatalf("got %d invocation rows, want 2", len(invocations))
	}

	review := invocations[0]
	if review.Purpose != "review" || review.StepName != "review" || review.Round != 1 {
		t.Fatalf("review row = %+v", review)
	}
	if review.SessionMode != db.InvocationModeStarted {
		t.Fatalf("review session mode = %q, want started", review.SessionMode)
	}
	if review.SessionKey == "" || review.SessionKey == "sess-new" {
		t.Fatalf("session key must be a fingerprint, not empty or the raw id: %q", review.SessionKey)
	}
	if review.Agent != "usage-agent" || review.Model == nil || *review.Model != "test-model-1" {
		t.Fatalf("agent/model = %q/%v", review.Agent, review.Model)
	}
	// usageAgent reports no launch identity, so the configured kind is still
	// retained in Agent while the fields that can only come from an observed
	// launch stay explicitly unknown rather than being invented from that kind.
	if review.ResolvedExecutable != nil || review.ModelArgs != nil {
		t.Fatalf("unobserved launch identity must stay unknown, got executable=%v args=%#v",
			review.ResolvedExecutable, review.ModelArgs)
	}
	if review.InputTokens != 100 || review.OutputTokens != 20 || review.CacheReadTokens != 60 {
		t.Fatalf("token usage not recorded: %+v", review)
	}
	if review.ExitStatus != "ok" || review.StartedAt == 0 || review.CompletedAt == 0 {
		t.Fatalf("timing/exit not recorded: %+v", review)
	}

	// The second invocation ran outside any session and defaults its purpose
	// to the step name.
	evidence := invocations[1]
	if evidence.SessionMode != db.InvocationModeCold || evidence.Purpose != "review" {
		t.Fatalf("evidence row = %+v", evidence)
	}
}

func TestPerfRecordingAgent_RecordsFallbackAttemptsSeparately(t *testing.T) {
	database, _, run, _ := setupTest(t)
	wrapped := &perfRecordingAgent{
		inner: agent.NewFallback([]agent.Agent{
			&fallbackUsageAgent{name: "codex", err: errors.New("codex start: executable not found")},
			&fallbackUsageAgent{name: "claude", result: &agent.Result{Model: "test-model-2"}},
		}),
		db:       database,
		runID:    run.ID,
		stepName: types.StepReview,
		round:    func() int { return 1 },
	}

	if _, err := wrapped.Run(context.Background(), agent.RunOpts{Purpose: "review"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	invocations, err := database.GetAgentInvocationsByRun(run.ID)
	if err != nil {
		t.Fatalf("get invocations: %v", err)
	}
	if len(invocations) != 2 {
		t.Fatalf("got %d invocation rows, want 2", len(invocations))
	}
	byAgent := map[string]db.AgentInvocation{}
	for _, invocation := range invocations {
		byAgent[invocation.Agent] = invocation
	}
	if got := byAgent["codex"]; got.ExitStatus != "error" || got.FailureCategory != "spawn" {
		t.Fatalf("codex invocation = %+v", got)
	}
	if got := byAgent["claude"]; got.ExitStatus != "ok" || got.Model == nil || *got.Model != "test-model-2" {
		t.Fatalf("claude invocation = %+v", got)
	}
}

func TestPerfRecordingAgent_MixedFallbackRecordsActualProviderCold(t *testing.T) {
	database, _, run, _ := setupTest(t)
	wrapped := &perfRecordingAgent{
		inner: agent.NewFallback([]agent.Agent{
			&fallbackUsageAgent{name: "pi", result: &agent.Result{Model: "pi-model"}},
			&usageAgent{resumable: true},
		}),
		db:       database,
		runID:    run.ID,
		stepName: types.StepReview,
		round:    func() int { return 1 },
	}

	sessions := NewRunSessions(database, run.ID, wrapped, true)
	if _, err := sessions.Run(context.Background(), wrapped, SessionRoleReviewer, agent.RunOpts{Purpose: "review"}, nil); err != nil {
		t.Fatalf("run session: %v", err)
	}

	invocations, err := database.GetAgentInvocationsByRun(run.ID)
	if err != nil {
		t.Fatalf("get invocations: %v", err)
	}
	if len(invocations) != 1 {
		t.Fatalf("got %d invocation rows, want 1", len(invocations))
	}
	if got := invocations[0]; got.Agent != "pi" || got.SessionMode != db.InvocationModeCold {
		t.Fatalf("invocation = %+v, want pi cold", got)
	}
}

// TestExecutor_AccumulatesParkedDuration proves a gate wait lands in the
// run's persisted parked total once the wait ends.
func TestExecutor_AccumulatesParkedDuration(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	step := newApprovalStep(types.StepReview, `{"findings":[{"severity":"warning","description":"x","action":"ask-user"}],"summary":"1"}`)
	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)
	time.Sleep(50 * time.Millisecond)
	if err := exec.Respond(types.StepReview, types.ActionApprove, nil); err != nil {
		t.Fatalf("respond: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor timed out")
	}

	got, err := database.GetRun(run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.ParkedMS < 50 {
		t.Fatalf("ParkedMS = %d, want >= 50 (the gate wait)", got.ParkedMS)
	}
	if got.AwaitingAgentSince != nil {
		t.Fatal("awaiting marker must be clear after resume")
	}
}
