package db

import (
	"testing"
)

func TestFixAttemptsCountsOuterAndInternalSpendTogether(t *testing.T) {
	d := openTestDB(t)
	step := newCIStepForBudget(t, d, "/ci-mixed")
	round, err := d.InsertStepRound(step.ID, 1, "initial", nil, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.SetStepRoundSelection(round.ID, stringPtr("[\"finding\"]"), RoundSelectionSourceAutoFix); err != nil {
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
		t.Fatalf("FixAttempts = %d, want 3", got)
	}
}

func stringPtr(value string) *string { return &value }
