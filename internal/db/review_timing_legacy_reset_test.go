package db

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/types"
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

func TestReviewTimingMigrationBackfillsEarlierInvocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review-timing-migration.sqlite")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := d.InsertRepo("/tmp/review-timing-migration", "https://example.com/repo.git", "main")
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.InsertRun(repo.ID, "feature", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	step, err := d.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.InsertAgentInvocation(AgentInvocation{
		RunID: run.ID, StepName: string(types.StepReview), Purpose: "review", Round: 1,
		Agent: "mock", StartedAt: 100, CompletedAt: 101, DurationMS: 1000, ExitStatus: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec(`UPDATE step_results SET status = ?, started_at = 200, started_at_ms = 200500, first_started_at_ms = NULL WHERE id = ?`, types.StepStatusParkedForApproval, step.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.StartStep(step.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec(`UPDATE step_results SET status = ?, completed_at = 220, completed_at_ms = 220250 WHERE id = ?`, types.StepStatusCompleted, step.ID); err != nil {
		t.Fatal(err)
	}

	timing, err := d.GetReviewTimingAt(run.ID, time.UnixMilli(220250))
	if err != nil {
		t.Fatal(err)
	}
	if timing == nil || timing.StartedAtMS == nil || *timing.StartedAtMS != 100000 || timing.TotalMS != 120250 {
		t.Fatalf("migrated timing = %+v, want historical invocation boundary", timing)
	}
}
