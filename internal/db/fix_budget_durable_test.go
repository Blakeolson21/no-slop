package db

import (
	"path/filepath"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/types"
)

// Every internal CI repair that commits is written back by the executor as one
// "auto_fix" round on the same step result, so the durable counter and the
// round record describe the same expenditure. Summing them charges it twice.
func TestFixAttemptsDoesNotDoubleChargeARestartedCIRepair(t *testing.T) {
	d := openTestDB(t)
	step := newCIStepForBudget(t, d, "/ci-overlap")
	if err := d.SetCIFixAttempts(step.ID, 2); err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 2; round++ {
		if _, err := d.InsertStepRound(step.ID, round, "auto_fix", nil, nil, 1); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.FixAttempts(step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("FixAttempts = %d, want 2 (one charge per repair, not a sum)", got)
	}
}

// An internal repair that produced no commit raises the durable counter without
// producing a round. The total must not fall back to the round count.
func TestFixAttemptsNeverRefundsInternalCIExpenditure(t *testing.T) {
	d := openTestDB(t)
	step := newCIStepForBudget(t, d, "/ci-no-commit")
	if err := d.SetCIFixAttempts(step.ID, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := d.InsertStepRound(step.ID, 1, "auto_fix", nil, nil, 1); err != nil {
		t.Fatal(err)
	}
	got, err := d.FixAttempts(step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != 3 {
		t.Fatalf("FixAttempts = %d, want 3 (durable spend is never refunded)", got)
	}
}

// A step that never spent internal budget is unaffected by the durable column.
func TestFixAttemptsIgnoresDurableColumnForStepsThatNeverSpendIt(t *testing.T) {
	d := openTestDB(t)
	step := newCIStepForBudget(t, d, "/review-only")
	for round := 1; round <= 2; round++ {
		if _, err := d.InsertStepRound(step.ID, round, "auto_fix", nil, nil, 1); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.FixAttempts(step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("FixAttempts = %d, want 2", got)
	}
}

// The whole point of the durable counter is that it outlives the process that
// spent it, so a restart must not hand the run a fresh budget.
func TestFixAttemptsDurableSpendSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	step := newCIStepForBudget(t, d, "/ci-restart")
	if err = d.SetCIFixAttempts(step.ID, 3); err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	got, err := reopened.FixAttempts(step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != 3 {
		t.Fatalf("FixAttempts after restart = %d, want 3", got)
	}
}

// An inherited budget is expenditure from a previous run and is additive: it is
// a different step's spend, not another view of this one.
func TestFixAttemptsAddsInheritedBudgetToDurableSpend(t *testing.T) {
	d := openTestDB(t)
	repo, err := d.InsertRepo("/ci-inherited", "https://example.com/inherited", "main")
	if err != nil {
		t.Fatal(err)
	}
	source, err := d.InsertRun(repo.ID, "feature", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	sourceStep, err := d.InsertStepResult(source.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.InsertStepRound(sourceStep.ID, 1, "auto_fix", nil, nil, 1); err != nil {
		t.Fatal(err)
	}
	if err = d.UpsertUncertifiedPipelineRange(repo.ID, source.Branch, "from", "to", source.ID); err != nil {
		t.Fatal(err)
	}

	replacement, err := d.InsertRun(repo.ID, "feature", "head2", "base")
	if err != nil {
		t.Fatal(err)
	}
	step, err := d.InsertStepResult(replacement.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.InitStepFixBudget(step.ID, repo.ID, replacement.Branch, replacement.ID, types.StepReview); err != nil {
		t.Fatal(err)
	}
	if err = d.SetCIFixAttempts(step.ID, 2); err != nil {
		t.Fatal(err)
	}
	got, err := d.FixAttempts(step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != 3 {
		t.Fatalf("FixAttempts = %d, want 3 (1 inherited + 2 durable)", got)
	}
}

func newCIStepForBudget(t *testing.T, d *DB, path string) *StepResult {
	t.Helper()
	repo, err := d.InsertRepo(path, "https://example.com"+path, "main")
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.InsertRun(repo.ID, "feature", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	step, err := d.InsertStepResult(run.ID, types.StepCI)
	if err != nil {
		t.Fatal(err)
	}
	return step
}
