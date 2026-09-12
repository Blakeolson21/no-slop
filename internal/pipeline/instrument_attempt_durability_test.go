package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// retryingAgent reports attempts the way every concrete adapter does: a start
// before the provider call, a completion after it. abandon stops the last
// attempt from ever completing, which is what a daemon killed mid-attempt looks
// like from the store's point of view.
type retryingAgent struct {
	attempts []error
	abandon  bool
	halted   chan struct{}
}

func (a *retryingAgent) Name() string                        { return "codex" }
func (a *retryingAgent) Close() error                        { return nil }
func (a *retryingAgent) ReportsAgentAttempts() bool          { return true }
func (a *retryingAgent) SupportsSessionResume() bool         { return false }
func (a *retryingAgent) SupportsSessionProvider(string) bool { return false }

func (a *retryingAgent) Run(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
	for i, attemptErr := range a.attempts {
		last := i == len(a.attempts)-1
		if opts.OnAttemptStart != nil {
			opts.OnAttemptStart(agent.AttemptStart{Agent: a.Name()})
		}
		if last && a.abandon {
			// The process dies here. Nothing further is reported: no completion
			// callback, no return value, no chance to persist anything.
			close(a.halted)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if opts.OnAttempt != nil {
			opts.OnAttempt(agent.Attempt{Agent: a.Name(), Err: attemptErr, Result: &agent.Result{}})
		}
		if attemptErr == nil {
			return &agent.Result{}, nil
		}
	}
	return nil, errors.New("exhausted")
}

func recordingAgentOver(t *testing.T, database *db.DB, runID string, inner agent.Agent) *perfRecordingAgent {
	t.Helper()
	return &perfRecordingAgent{inner: inner, db: database, runID: runID, stepName: types.StepReview, round: func() int { return 1 }}
}

// Every concrete attempt gets its own durable row before the provider is
// invoked. A single pending row created once per Agent.Run cannot do this: the
// first completed attempt consumed it, so a retry or fallback attempt that the
// process never returns from left no record and no usage at all.
func TestEachAttemptIsDurableBeforeItRuns(t *testing.T) {
	database, _, run, _ := setupTest(t)
	inner := &retryingAgent{attempts: []error{errors.New("transient"), errors.New("transient"), nil}, abandon: true, halted: make(chan struct{})}
	wrapped := recordingAgentOver(t, database, run.ID, inner)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = wrapped.Run(ctx, agent.RunOpts{Purpose: "review"})
	}()

	// The third attempt has started and will never report a completion. Observe
	// the store exactly as a post-mortem would.
	<-inner.halted
	invs, err := database.GetAgentInvocationsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(invs) != 3 {
		t.Fatalf("attempts recorded = %d, want 3 (one row per concrete attempt)", len(invs))
	}
	running := 0
	ids := map[string]bool{}
	for _, inv := range invs {
		ids[inv.ID] = true
		if inv.ExitStatus == "running" {
			running++
		}
		if inv.Purpose != "review" || inv.StepName != string(types.StepReview) {
			t.Fatalf("attempt row lost its duty: %+v", inv)
		}
	}
	if len(ids) != 3 {
		t.Fatalf("attempt rows share invocation IDs: %v", ids)
	}
	if running != 1 {
		t.Fatalf("in-flight attempts recorded as running = %d, want exactly the abandoned one", running)
	}

	cancel()
	<-done
}

// The row opened before an invocation belongs to that invocation's first
// attempt, not to a second one. Two rows for one attempt would double-count the
// turn in the terminal waste payload.
func TestFirstAttemptReusesTheInvocationRow(t *testing.T) {
	database, _, run, _ := setupTest(t)
	inner := &retryingAgent{attempts: []error{nil}}
	wrapped := recordingAgentOver(t, database, run.ID, inner)

	if _, err := wrapped.Run(context.Background(), agent.RunOpts{Purpose: "review"}); err != nil {
		t.Fatal(err)
	}
	invs, err := database.GetAgentInvocationsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(invs) != 1 {
		t.Fatalf("rows = %d, want 1 for a single successful attempt", len(invs))
	}
	if invs[0].ExitStatus == "running" {
		t.Fatal("the completed attempt was left pending")
	}
}

// An adapter that reports no attempts at all still gets its pre-invocation row,
// so a process that dies inside it is not silently free.
func TestUnreportedInvocationStillOpensADurableRow(t *testing.T) {
	database, _, run, _ := setupTest(t)
	inner := &silentAgent{}
	wrapped := recordingAgentOver(t, database, run.ID, inner)

	if _, err := wrapped.Run(context.Background(), agent.RunOpts{Purpose: "review"}); err != nil {
		t.Fatal(err)
	}
	invs, err := database.GetAgentInvocationsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(invs) != 1 || invs[0].ExitStatus == "running" {
		t.Fatalf("unreported invocation rows = %+v", invs)
	}
}

type silentAgent struct{}

func (silentAgent) Name() string { return "claude" }
func (silentAgent) Close() error { return nil }
func (silentAgent) Run(context.Context, agent.RunOpts) (*agent.Result, error) {
	return &agent.Result{}, nil
}
