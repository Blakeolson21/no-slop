package db

import (
	"testing"

	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestReviewTimingUsesMillisecondStepTimestamps(t *testing.T) {
	d, _, run := openSessionTestDB(t)
	step, err := d.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec(`
		UPDATE step_results
		SET started_at = 1000, completed_at = 1001,
			started_at_ms = 1000999, completed_at_ms = 1001199,
			status = 'completed'
		WHERE id = ?`, step.ID); err != nil {
		t.Fatal(err)
	}

	timing, err := d.GetReviewTiming(run.ID, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if timing == nil || timing.TotalMS != 200 {
		t.Fatalf("timing = %+v, want 200ms total", timing)
	}
}
