package db

import (
	"github.com/Blakeolson21/no-slop/internal/types"
	"testing"
	"time"
)

func TestReviewTimingLegacyResetPreservesFirstStart(t *testing.T) {
	for _, resetName := range []string{"reset", "invalidate"} {
		for _, precise := range []bool{false, true} {
			t.Run(resetName+map[bool]string{false: "-seconds", true: "-millis"}[precise], func(t *testing.T) {
				d, _, run := openSessionTestDB(t)
				step, err := d.InsertStepResult(run.ID, types.StepReview)
				if err != nil {
					t.Fatal(err)
				}
				var ms any
				wantStart := int64(100000)
				if precise {
					ms = int64(100250)
					wantStart = 100250
				}
				_, err = d.sql.Exec("UPDATE step_results SET started_at=100,started_at_ms=?,first_started_at_ms=NULL,status='running' WHERE id=?", ms, step.ID)
				if err != nil {
					t.Fatal(err)
				}
				if resetName == "reset" {
					err = d.ResetStepsFrom(run.ID, types.StepReview.Order())
				} else {
					err = d.ResetStepsFromOrder(run.ID, types.StepReview.Order())
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = d.StartStep(step.ID); err != nil {
					t.Fatal(err)
				}
				_, err = d.sql.Exec("UPDATE step_results SET completed_at=120,completed_at_ms=120250,status='completed' WHERE id=?", step.ID)
				if err != nil {
					t.Fatal(err)
				}
				timing, err := d.GetReviewTimingAt(run.ID, time.UnixMilli(120250))
				if err != nil {
					t.Fatal(err)
				}
				if timing == nil || timing.StartedAtMS == nil || *timing.StartedAtMS != wantStart || timing.TotalMS != 120250-wantStart {
					t.Fatalf("legacy boundary lost: %+v", timing)
				}
			})
		}
	}
}
