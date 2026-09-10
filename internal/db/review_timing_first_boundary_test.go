package db

import (
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestReviewTimingFirstBoundarySurvivesResetAndRestart(t *testing.T) {
	d, _, run := openSessionTestDB(t)
	step, err := d.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.StartStep(step.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec(`UPDATE step_results SET started_at = 100, started_at_ms = 100250, first_started_at_ms = 100250 WHERE id = ?`, step.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.InsertAgentInvocation(AgentInvocation{
		RunID: run.ID, StepName: string(types.StepReview), Purpose: "review", Round: 1,
		Agent: "mock", StartedAt: 110, CompletedAt: 111, DurationMS: 1000, ExitStatus: "ok",
	}); err != nil {
		t.Fatal(err)
	}

	if err := d.ResetStepsFrom(run.ID, types.StepReview.Order()); err != nil {
		t.Fatal(err)
	}
	if err := d.StartStep(step.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.ResetStepsFromOrder(run.ID, types.StepReview.Order()); err != nil {
		t.Fatal(err)
	}
	if err := d.StartStep(step.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.CompleteStep(step.ID, 0, 1000, "review.log"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec(`UPDATE step_results SET completed_at = 120, completed_at_ms = 120250 WHERE id = ?`, step.ID); err != nil {
		t.Fatal(err)
	}

	gotStep, err := d.GetStepResult(step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotStep.FirstStartedAtMS == nil || *gotStep.FirstStartedAtMS != 100250 {
		t.Fatalf("first boundary = %v, want 100250", gotStep.FirstStartedAtMS)
	}
	if gotStep.StartedAtMS == nil || *gotStep.StartedAtMS <= 100250 {
		t.Fatalf("restart boundary = %v, want newer current start", gotStep.StartedAtMS)
	}

	timing, err := d.GetReviewTimingAt(run.ID, time.UnixMilli(120250))
	if err != nil {
		t.Fatal(err)
	}
	if timing == nil || timing.StartedAt != 100 || timing.TotalMS != 20000 || timing.Status != types.StepStatusCompleted || !timing.Complete {
		t.Fatalf("timing = %+v, want first-attempt interval", timing)
	}
}
