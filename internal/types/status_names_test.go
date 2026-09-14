package types

import "testing"

// TestNormalizeStatusLegacyEquivalence pins the one-release rename alias:
// every legacy stored literal must classify identically to the renamed token
// that supersedes it. Old rows (pre-backfill) and new rows (post-rename) must
// be indistinguishable to every reader. Writers emit only the new names.
func TestNormalizeStatusLegacyEquivalence(t *testing.T) {
	stepCases := []struct {
		legacy string
		modern StepStatus
	}{
		{"fix_review", StepStatusParkedAfterFix},
		{"awaiting_approval", StepStatusParkedForApproval},
		{"fixing", StepStatusFixerRunning},
	}
	for _, tc := range stepCases {
		if got := NormalizeStepStatus(tc.legacy); got != tc.modern {
			t.Errorf("NormalizeStepStatus(%q) = %q, want %q", tc.legacy, got, tc.modern)
		}
		// Idempotent: normalizing the modern name is a no-op.
		if got := NormalizeStepStatus(string(tc.modern)); got != tc.modern {
			t.Errorf("NormalizeStepStatus(%q) = %q, want itself", tc.modern, got)
		}
	}

	runCases := []struct {
		legacy string
		modern RunStatus
	}{
		{"pending", RunStarting},
	}
	for _, tc := range runCases {
		if got := NormalizeRunStatus(tc.legacy); got != tc.modern {
			t.Errorf("NormalizeRunStatus(%q) = %q, want %q", tc.legacy, got, tc.modern)
		}
		if got := NormalizeRunStatus(string(tc.modern)); got != tc.modern {
			t.Errorf("NormalizeRunStatus(%q) = %q, want itself", tc.modern, got)
		}
	}

	// Unknown values pass through unchanged so the store lint can name them.
	for _, raw := range []string{"", "weird", "superseded", "running", "completed"} {
		if got := string(NormalizeStepStatus(raw)); got != raw {
			t.Errorf("NormalizeStepStatus(%q) = %q, want passthrough", raw, got)
		}
		if got := string(NormalizeRunStatus(raw)); got != raw {
			t.Errorf("NormalizeRunStatus(%q) = %q, want passthrough", raw, got)
		}
	}

	// Out of scope by ruling: step_results "pending" is an unrelated token and
	// must never be rewritten by the run-level alias.
	if got := NormalizeStepStatus("pending"); got != StepStatusPending {
		t.Errorf("step_results pending must stay pending, got %q", got)
	}
	if got := NormalizeRunStatus("fixing"); got != "fixing" {
		t.Errorf("runs.status must not adopt step-level aliases, got %q", got)
	}

	// The legal sets must contain every modern name, every legacy name, and
	// nothing invented.
	legalRun := map[string]bool{}
	for _, n := range LegalRunStatusNames() {
		legalRun[n] = true
	}
	for _, n := range []string{
		string(RunStarting), string(RunRunning), string(RunCompleted),
		string(RunFailed), string(RunCancelled), string(LegacyRunPending),
	} {
		if !legalRun[n] {
			t.Errorf("LegalRunStatusNames missing %q", n)
		}
	}
	legalStep := map[string]bool{}
	for _, n := range LegalStepStatusNames() {
		legalStep[n] = true
	}
	for _, n := range []string{
		string(StepStatusPending), string(StepStatusRunning),
		string(StepStatusParkedForApproval), string(StepStatusFixerRunning),
		string(StepStatusParkedAfterFix), string(StepStatusCompleted),
		string(StepStatusSkipped), string(StepStatusFailed), "cancelled",
		string(LegacyStepStatusFixReview), string(LegacyStepStatusAwaitingApproval),
		string(LegacyStepStatusFixing),
	} {
		if !legalStep[n] {
			t.Errorf("LegalStepStatusNames missing %q", n)
		}
	}
	if legalStep[string(LegacyStepStatusFixReview)] && LegacyStepStatusFixReview == StepStatusParkedAfterFix {
		t.Error("legacy step constants must hold the old literals, not the new ones")
	}
}
