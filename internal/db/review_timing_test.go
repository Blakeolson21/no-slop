package db

import (
	"github.com/Blakeolson21/no-slop/internal/types"
	"testing"
)

func TestReviewTimingUsesStoredInvocationsAndKeepsUnknownDistinct(t *testing.T) {
	d, _, run := openSessionTestDB(t)
	if got, err := d.GetReviewTiming(run.ID, 200); err != nil || got != nil {
		t.Fatalf("unstarted: %+v %v", got, err)
	}
	s, err := d.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec("UPDATE step_results SET started_at=100, completed_at=150, status='completed' WHERE id=?", s.ID); err != nil {
		t.Fatal(err)
	}
	for i, purpose := range []string{"review", "review-fix", "review", "review"} {
		inv := AgentInvocation{RunID: run.ID, StepName: "review", Purpose: purpose, Round: 1, Agent: "mock", StartedAt: 100 + int64(i)*10, CompletedAt: 105 + int64(i)*10, DurationMS: 5000, ExitStatus: "ok"}
		if i > 0 {
			inv.Round = 2
		}
		if i == 3 {
			inv.ExitStatus = "error"
		}
		if _, err := d.InsertAgentInvocation(inv); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.GetReviewTiming(run.ID, 200)
	if err != nil {
		t.Fatal(err)
	}
	if got.StartedAt != 100 || got.TotalMS != 50000 || !got.Complete || got.RoundCount != 2 || got.ReviewMS != 15000 || got.FixMS != 5000 || len(got.Turns) != 4 {
		t.Fatalf("timing: %+v", got)
	}
	if got.Turns[0].ModelMS != nil || got.Turns[3].ExitStatus != "error" {
		t.Fatalf("unknown metrics or failed attempt hidden: %+v", got.Turns)
	}
	if _, err := d.sql.Exec("UPDATE step_results SET completed_at=NULL, status='fix_review' WHERE id=?", s.ID); err != nil {
		t.Fatal(err)
	}
	got, err = d.GetReviewTiming(run.ID, 200)
	if err != nil || got.Complete || got.TotalMS != 100000 {
		t.Fatalf("parked elapsed: %+v %v", got, err)
	}
}
