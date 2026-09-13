package pipeline

import (
	"context"
	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/types"
	"testing"
	"time"
)

func TestReviewerZeroCeilingRecoveryMustKeepManualBudget(t *testing.T) {
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
	if err = d.StartStepWithAutoFixLimit(sr.ID, 0); err != nil {
		t.Fatal(err)
	}
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
		got, e := d.GetStepResult(sr.ID)
		if e != nil {
			return nil, e
		}
		v := 0
		if got.AutoFixLimit != nil {
			v = *got.AutoFixLimit
		}
		observed <- v
		return &StepOutcome{Findings: `{"findings":[]}`}, nil
	}}
	e := NewExecutor(d, p, &config.Config{AutoFix: config.AutoFix{Review: 20}}, nil, []Step{step}, nil)
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
		if limit > 0 {
			t.Fatalf("recovery raised original zero ceiling to %d", limit)
		}
	case err := <-done:
		t.Fatalf("no resumed fixer: %v", err)
	case <-ctx.Done():
		t.Fatal("resumed fixer did not run")
	}
}
