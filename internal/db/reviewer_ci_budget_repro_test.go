package db

import (
	"github.com/Blakeolson21/no-slop/internal/types"
	"testing"
)

func TestReviewerCIBudgetCountsDurableInternalAttempts(t *testing.T) {
	d := openTestDB(t)
	repo, err := d.InsertRepo("/ci-budget-repro", "https://example.com/repro", "main")
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.InsertRun(repo.ID, "feature", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	sr, err := d.InsertStepResult(run.ID, types.StepCI)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.SetCIFixAttempts(sr.ID, 3); err != nil {
		t.Fatal(err)
	}
	got, err := d.FixAttempts(sr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != 3 {
		t.Fatalf("durable CI budget spent=3 but shared FixAttempts=%d", got)
	}
}
