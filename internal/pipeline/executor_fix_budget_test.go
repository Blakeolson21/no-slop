package pipeline

import (
	"context"
	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/types"
	"testing"
	"time"
)

func TestExecutorFixBudgetExhaustionIsTerminal(t *testing.T) {
	d, p, run, repo := setupTest(t)
	calls := 0
	step := &adaptiveCallStep{name: types.StepReview, fn: func(*StepContext) (*StepOutcome, error) {
		calls++
		return &StepOutcome{NeedsApproval: true, AutoFixable: true, Findings: `{"findings":[{"id":"bug","severity":"error","description":"still broken","action":"auto-fix"}]}`}, nil
	}}
	e := NewExecutor(d, p, &config.Config{AutoFix: config.AutoFix{Review: 2}}, nil, []Step{step}, nil)
	events := collectEvents(e)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := e.Execute(ctx, run, repo, t.TempDir())
	got, getErr := d.GetRun(run.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if err == nil || got.Status != types.RunFailed || got.Error == nil || *got.Error != "fix budget exhausted" || got.AwaitingAgentSince != nil {
		t.Fatalf("budget did not terminate: err=%v run=%#v", err, got)
	}
	if calls != 3 {
		t.Fatalf("calls=%d, want initial + 2 fixes", calls)
	}
	if events.findLast(ipc.EventRunCompleted, string(types.RunFailed)) == nil {
		t.Fatal("no terminal event")
	}
	active, err := d.GetActiveRuns()
	if err != nil || len(active) != 0 {
		t.Fatalf("admission still active: %v %v", active, err)
	}
}

func TestExecutorRecoveredExhaustedBudgetDoesNotPark(t *testing.T) {
	d, p, run, repo := setupTest(t)
	if err := d.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}
	sr, err := d.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.StartStepWithAutoFixLimit(sr.ID, 2); err != nil {
		t.Fatal(err)
	}
	findings := `{"findings":[{"id":"bug","severity":"error","description":"still broken","action":"auto-fix"}]}`
	for n := 1; n <= 2; n++ {
		if _, err = d.InsertReviewStepRound(sr.ID, n, "auto_fix", &findings, nil, run.HeadSHA, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err = d.ParkStepForApproval(run.ID, sr.ID, types.StepStatusFixReview, 2, &findings); err != nil {
		t.Fatal(err)
	}
	run, err = d.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	step := &adaptiveCallStep{name: types.StepReview, fn: func(*StepContext) (*StepOutcome, error) { calls++; return &StepOutcome{}, nil }}
	e := NewExecutor(d, p, &config.Config{AutoFix: config.AutoFix{Review: 20}}, nil, []Step{step}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = e.Resume(ctx, run, repo, t.TempDir()); err == nil || err.Error() != "fix budget exhausted" {
		t.Fatalf("resume=%v", err)
	}
	got, err := d.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || got.Status != types.RunFailed || got.AwaitingAgentSince != nil {
		t.Fatalf("recovery executed=%d run=%+v", calls, got)
	}
}

func TestExecutorRepeatDispatchDoesNotRefundUncertifiedFixes(t *testing.T) {
	d, p, old, repo := setupTest(t)
	dir := t.TempDir()
	initGitRepo(t, dir)
	head := currentSHA(t, dir)
	old.HeadSHA = head
	if err := d.UpdateRunHeadSHA(old.ID, head); err != nil {
		t.Fatal(err)
	}
	sr, err := d.InsertStepResult(old.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	findings := `{"findings":[{"id":"bug","severity":"error","description":"still broken","action":"auto-fix"}]}`
	for n := 1; n <= 2; n++ {
		if _, err = d.InsertReviewStepRound(sr.ID, n, "auto_fix", &findings, nil, head, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err = d.UpsertUncertifiedPipelineRange(repo.ID, old.Branch, head, head, old.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		run, err := d.InsertRun(repo.ID, old.Branch, head, head)
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		step := &adaptiveCallStep{name: types.StepReview, fn: func(s *StepContext) (*StepOutcome, error) {
			calls++
			if s.Fixing {
				t.Error("exhausted replacement funded another fixer")
			}
			return &StepOutcome{NeedsApproval: true, AutoFixable: true, Findings: findings}, nil
		}}
		e := NewExecutor(d, p, &config.Config{AutoFix: config.AutoFix{Review: 2}}, nil, []Step{step}, nil)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err = e.Execute(ctx, run, repo, dir)
		cancel()
		if err == nil || err.Error() != "fix budget exhausted" || calls != 1 {
			t.Fatalf("dispatch %d: %v calls=%d", i, err, calls)
		}
	}
}
