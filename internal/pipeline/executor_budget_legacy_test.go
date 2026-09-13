package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestExecutorRecoveredLegacyNullCeilingUsesConfiguredBudget(t *testing.T) {
	d, p, run, repo := setupTest(t)
	dir := t.TempDir()
	initGitRepo(t, dir)
	head := currentSHA(t, dir)
	run.HeadSHA = head
	if err := d.UpdateRunHeadSHA(run.ID, head); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}
	sr, err := d.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.StartStep(sr.ID); err != nil {
		t.Fatal(err)
	}
	findings := `{"findings":[{"id":"bug","severity":"error","description":"still broken","action":"auto-fix"}]}`
	for round := 1; round <= 3; round++ {
		if _, err := d.InsertReviewStepRound(sr.ID, round, "auto_fix", &findings, nil, head, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.ParkStepForApproval(run.ID, sr.ID, types.StepStatusFixReview, 3, &findings); err != nil {
		t.Fatal(err)
	}
	run, err = d.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}

	step := &adaptiveCallStep{name: types.StepReview, fn: func(*StepContext) (*StepOutcome, error) {
		return &StepOutcome{}, nil
	}}
	e := NewExecutor(d, p, &config.Config{AutoFix: config.AutoFix{Review: 20}}, nil, []Step{step}, nil)
	events := collectEvents(e)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Resume(ctx, run, repo, dir) }()

	event := waitForStepEvent(t, events, ipc.EventStepCompleted, types.StepReview)
	if event.Status == nil || *event.Status != string(types.StepStatusFixReview) {
		t.Fatalf("recovery status = %v, want fix_review", event.Status)
	}
	if err := e.Respond(types.StepReview, types.ActionApprove, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, err := d.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != types.RunCompleted {
		t.Fatalf("run status = %s, want completed", got.Status)
	}
}
