package db

import (
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestReviewTimingVerifiedHeadPromotionFreezesTerminalBoundary(t *testing.T) {
	for _, withError := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "failed"}[withError], func(t *testing.T) {
			d, _, run := openSessionTestDB(t)
			step, err := d.InsertStepResult(run.ID, types.StepReview)
			if err != nil {
				t.Fatal(err)
			}
			if err := d.StartStep(step.ID); err != nil {
				t.Fatal(err)
			}
			if withError {
				err = d.UpdateRunErrorStatusWithVerifiedHead(run.ID, "failed", types.RunFailed, "promoted")
			} else {
				err = d.UpdateRunStatusWithVerifiedHead(run.ID, types.RunCompleted, "promoted")
			}
			if err != nil {
				t.Fatal(err)
			}
			stored, err := d.GetRun(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.HeadSHA != "promoted" || stored.TerminalAtMS == nil {
				t.Fatalf("missing atomic terminal boundary: %+v", stored)
			}
			if err := d.UpdateRunError(run.ID, "later diagnostic"); err != nil {
				t.Fatal(err)
			}
			timing, err := d.GetReviewTimingAt(run.ID, time.Now().Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if timing == nil || timing.StartedAtMS == nil || !timing.Complete || timing.TotalMS != *stored.TerminalAtMS-*timing.StartedAtMS {
				t.Fatalf("terminal promotion did not freeze review wall: %+v", timing)
			}
		})
	}
}

func TestReviewTimingResetUsesFirstAttemptBoundary(t *testing.T) {
	d, _, run := openSessionTestDB(t)
	step, err := d.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec(`
		UPDATE step_results
		SET started_at = 200, started_at_ms = 200500,
			completed_at = 205, completed_at_ms = 205500, status = 'completed'
		WHERE id = ?`, step.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.InsertAgentInvocation(AgentInvocation{
		RunID: run.ID, StepName: string(types.StepReview), Purpose: "review", Round: 1,
		Agent: "mock", StartedAt: 100, CompletedAt: 101, DurationMS: 1000, ExitStatus: "ok",
	}); err != nil {
		t.Fatal(err)
	}

	timing, err := d.GetReviewTiming(run.ID, 999)
	if err != nil {
		t.Fatal(err)
	}
	if timing.StartedAt != 100 || timing.TotalMS != 105500 {
		t.Fatalf("timing = %+v, want first-attempt interval", timing)
	}
}

func TestReviewTimingTerminalBoundaryUsesMilliseconds(t *testing.T) {
	d, _, run := openSessionTestDB(t)
	step, err := d.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec(`
		UPDATE step_results
		SET started_at = 100, started_at_ms = 100500, status = 'running'
		WHERE id = ?`, step.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec(`
		UPDATE runs SET status = 'failed', updated_at = 200, terminal_at_ms = 200700
		WHERE id = ?`, run.ID); err != nil {
		t.Fatal(err)
	}

	timing, err := d.GetReviewTimingAt(run.ID, time.UnixMilli(999999))
	if err != nil {
		t.Fatal(err)
	}
	if timing == nil || !timing.Complete || timing.TotalMS != 100200 {
		t.Fatalf("timing = %+v, want persisted terminal interval", timing)
	}
}
