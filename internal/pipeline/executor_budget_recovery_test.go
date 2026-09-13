package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// A CI step that spent its repair budget durably must reach the same terminal
// edge as a step that spent it in rounds: the gate is refused on recovery
// rather than parked again on a budget the run no longer has.
func TestExecutorRecoveredDurableCIBudgetExhaustionIsTerminal(t *testing.T) {
	d, p, run, repo := setupTest(t)
	if err := d.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}
	sr, err := d.InsertStepResult(run.ID, types.StepCI)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.StartStepWithAutoFixLimit(sr.ID, 2); err != nil {
		t.Fatal(err)
	}
	// The repairs committed nothing, so they left no rounds behind - only the
	// durable counter the CI step wrote before each attempt.
	if err = d.SetCIFixAttempts(sr.ID, 2); err != nil {
		t.Fatal(err)
	}
	findings := `{"findings":[{"id":"ci","severity":"error","description":"still red","action":"auto-fix"}]}`
	if _, err = d.InsertStepRound(sr.ID, 1, "initial", &findings, nil, 1); err != nil {
		t.Fatal(err)
	}
	if err = d.ParkStepForApproval(run.ID, sr.ID, types.StepStatusFixReview, 1, &findings); err != nil {
		t.Fatal(err)
	}
	run, err = d.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}

	calls := 0
	step := &adaptiveCallStep{name: types.StepCI, fn: func(*StepContext) (*StepOutcome, error) {
		calls++
		return &StepOutcome{}, nil
	}}
	e := NewExecutor(d, p, &config.Config{AutoFix: config.AutoFix{CI: 20}}, nil, []Step{step}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = e.Resume(ctx, run, repo, t.TempDir()); err == nil || err.Error() != ErrFixBudgetExhausted.Error() {
		t.Fatalf("resume = %v, want fix budget exhausted", err)
	}
	got, err := d.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || got.Status != types.RunFailed || got.AwaitingAgentSince != nil {
		t.Fatalf("durable CI spend was refunded: calls=%d run=%+v", calls, got)
	}
}

// A row that predates the zero/unset distinction carries no ceiling at all. It
// must keep deferring to configuration; reading a legacy NULL as "disabled"
// would silently strip funding from every recovered run in an older store.
func TestExecutorLegacyNullCeilingStillDefersToConfiguration(t *testing.T) {
	limit := observeRecoveredCeiling(t, func(d *db.DB, stepID string) {
		if err := d.StartStep(stepID); err != nil {
			t.Fatal(err)
		}
	}, 20)
	if limit != 20 {
		t.Fatalf("recovered ceiling = %d, want the configured 20", limit)
	}
}

// The same fixture with a recorded zero keeps the zero.
func TestExecutorRecordedZeroCeilingIsNotRefundedByConfiguration(t *testing.T) {
	limit := observeRecoveredCeiling(t, func(d *db.DB, stepID string) {
		if err := d.StartStepWithAutoFixLimit(stepID, 0); err != nil {
			t.Fatal(err)
		}
	}, 20)
	if limit != 0 {
		t.Fatalf("recovered ceiling = %d, want the recorded 0", limit)
	}
}

// A recorded positive ceiling below configuration still wins, unchanged.
func TestExecutorRecordedLowerCeilingStillWins(t *testing.T) {
	limit := observeRecoveredCeiling(t, func(d *db.DB, stepID string) {
		if err := d.StartStepWithAutoFixLimit(stepID, 2); err != nil {
			t.Fatal(err)
		}
	}, 20)
	if limit != 2 {
		t.Fatalf("recovered ceiling = %d, want the recorded 2", limit)
	}
}

// observeRecoveredCeiling parks a review at a gate whose ceiling `start`
// records, responds through the real gate, and reports the ceiling the resumed
// fixer actually finds persisted on the step.
func observeRecoveredCeiling(t *testing.T, start func(*db.DB, string), configured int) int {
	t.Helper()
	d, p, run, repo := setupTest(t)
	dir := t.TempDir()
	initGitRepo(t, dir)
	run.HeadSHA = currentSHA(t, dir)
	if err := d.UpdateRunHeadSHA(run.ID, run.HeadSHA); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}
	sr, err := d.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	start(d, sr.ID)
	findings := `{"findings":[{"id":"bug","severity":"error","description":"still broken","action":"auto-fix"}]}`
	if _, err = d.InsertReviewStepRound(sr.ID, 1, "fix", &findings, nil, run.HeadSHA, 1); err != nil {
		t.Fatal(err)
	}
	if err = d.ParkStepForApproval(run.ID, sr.ID, types.StepStatusFixReview, 1, &findings); err != nil {
		t.Fatal(err)
	}
	run, err = d.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}

	observed := make(chan int, 1)
	step := &adaptiveCallStep{name: types.StepReview, fn: func(*StepContext) (*StepOutcome, error) {
		got, readErr := d.GetStepResult(sr.ID)
		if readErr != nil {
			return nil, readErr
		}
		limit := -1
		if got.AutoFixLimit != nil {
			limit = *got.AutoFixLimit
		}
		select {
		case observed <- limit:
		default:
		}
		return &StepOutcome{Findings: `{"findings":[]}`}, nil
	}}
	e := NewExecutor(d, p, &config.Config{AutoFix: config.AutoFix{Review: configured}}, nil, []Step{step}, nil)
	events := collectEvents(e)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Resume(ctx, run, repo, dir) }()
	waitForStepEvent(t, events, ipc.EventStepCompleted, types.StepReview)
	if err = e.Respond(types.StepReview, types.ActionFix, []string{"bug"}); err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	select {
	case limit := <-observed:
		cancel()
		<-done
		return limit
	case err := <-done:
		t.Fatalf("no resumed fixer: %v", err)
	case <-ctx.Done():
		t.Fatal("resumed fixer did not run")
	}
	return -1
}
