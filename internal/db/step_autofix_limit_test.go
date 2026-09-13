package db

import (
	"path/filepath"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/types"
)

// Zero and unset are different facts. Zero says an operator disabled this
// step's funding; NULL says nobody recorded a ceiling at all. Collapsing the
// first into the second let recovery re-fund a deliberately unfunded step.
func TestStartStepKeepsAnExplicitZeroCeilingDistinctFromUnset(t *testing.T) {
	d := openTestDB(t)
	repo, err := d.InsertRepo("/ceiling", "https://example.com/ceiling", "main")
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.InsertRun(repo.ID, "feature", "head", "base")
	if err != nil {
		t.Fatal(err)
	}

	disabled, err := d.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.StartStepWithAutoFixLimit(disabled.ID, 0); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetStepResult(disabled.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AutoFixLimit == nil || *got.AutoFixLimit != 0 {
		t.Fatalf("explicit zero ceiling = %v, want recorded 0", got.AutoFixLimit)
	}

	unset, err := d.InsertStepResult(run.ID, types.StepTest)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.StartStep(unset.ID); err != nil {
		t.Fatal(err)
	}
	got, err = d.GetStepResult(unset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AutoFixLimit != nil {
		t.Fatalf("unrecorded ceiling = %v, want NULL", *got.AutoFixLimit)
	}
}

// A recorded zero has to outlive the process that recorded it; that is the
// whole failure mode, since the ceiling is only re-read on recovery.
func TestExplicitZeroCeilingSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := d.InsertRepo("/ceiling-restart", "https://example.com/restart", "main")
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
	if err = d.StartStepWithAutoFixLimit(step.ID, 0); err != nil {
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
	got, err := reopened.GetStepResult(step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AutoFixLimit == nil || *got.AutoFixLimit != 0 {
		t.Fatalf("zero ceiling after restart = %v, want recorded 0", got.AutoFixLimit)
	}
}

// SetStepAutoFixLimit shares the encoding, so it must agree about zero.
func TestSetStepAutoFixLimitRecordsZeroAndOnlyNullsAnUnsetCeiling(t *testing.T) {
	d := openTestDB(t)
	repo, err := d.InsertRepo("/ceiling-set", "https://example.com/set", "main")
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
	if err = d.SetStepAutoFixLimit(step.ID, 0); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetStepResult(step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AutoFixLimit == nil || *got.AutoFixLimit != 0 {
		t.Fatalf("set zero ceiling = %v, want recorded 0", got.AutoFixLimit)
	}
	if err = d.SetStepAutoFixLimit(step.ID, AutoFixLimitUnset); err != nil {
		t.Fatal(err)
	}
	got, err = d.GetStepResult(step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AutoFixLimit != nil {
		t.Fatalf("unset ceiling = %v, want NULL", *got.AutoFixLimit)
	}
}
