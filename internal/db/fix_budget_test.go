package db

import (
	"github.com/Blakeolson21/no-slop/internal/types"
	"path/filepath"
	"testing"
)

func TestFixBudgetSurvivesRestartAndReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := d.InsertRepo("/budget", "https://example.com/r", "main")
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
	if err = d.InitStepFixBudget(step.ID, repo.ID, run.Branch, run.ID, types.StepReview); err != nil {
		t.Fatal(err)
	}
	// Durable legacy rounds are sufficient to reconstruct funding.
	for n := 1; n <= 2; n++ {
		if _, err = d.sql.Exec(`INSERT INTO step_rounds(id,step_result_id,round,trigger_type,duration_ms,created_at) VALUES(?,?,?,'auto_fix',1,1)`, string(rune('a'+n)), step.ID, n); err != nil {
			t.Fatal(err)
		}
	}
	if err = d.UpsertUncertifiedPipelineRange(repo.ID, run.Branch, "head", "head", run.ID); err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for i := 0; i < 3; i++ {
		next, err := d.InsertRun(repo.ID, run.Branch, "rebased", "base")
		if err != nil {
			t.Fatal(err)
		}
		nextStep, err := d.InsertStepResult(next.ID, types.StepReview)
		if err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 2; j++ {
			if err = d.InitStepFixBudget(nextStep.ID, repo.ID, run.Branch, next.ID, types.StepReview); err != nil {
				t.Fatal(err)
			}
		}
		used, err := d.FixAttempts(nextStep.ID)
		if err != nil || used != 2 {
			t.Fatalf("dispatch %d used=%d err=%v", i, used, err)
		}
		if err = d.UpsertUncertifiedPipelineRange(repo.ID, run.Branch, "rebased", "rebased", next.ID); err != nil {
			t.Fatal(err)
		}
	}
}
