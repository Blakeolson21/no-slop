package pipeline

import (
	"errors"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/types"
)

// parkTestRun moves the run to running and stamps the awaiting-agent marker,
// mirroring a run parked at an approval gate when a terminal outcome lands.
func parkTestRun(t *testing.T, d interface {
	UpdateRunStatus(id string, status types.RunStatus) error
	SetRunAwaitingAgent(id string) error
}, runID string) {
	t.Helper()
	if err := d.UpdateRunStatus(runID, types.RunRunning); err != nil {
		t.Fatalf("set running: %v", err)
	}
	if err := d.SetRunAwaitingAgent(runID); err != nil {
		t.Fatalf("park run: %v", err)
	}
}

// TestExecutor_TerminalOutcomesClearAwaitingAgentMarker pins that the
// executor's own terminal write paths (complete success, step failure, and
// user abort) each clear runs.awaiting_agent_since in the same write that
// finalizes the status, so a finished run can never be misread as parked.
func TestExecutor_TerminalOutcomesClearAwaitingAgentMarker(t *testing.T) {
	t.Run("completeRun", func(t *testing.T) {
		database, p, run, repo := setupTest(t)
		parkTestRun(t, database, run.ID)

		exec := NewExecutor(database, p, nil, nil, nil, nil)
		if err := exec.completeRun(run, repo); err != nil {
			t.Fatalf("completeRun: %v", err)
		}
		got, err := database.GetRun(run.ID)
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		if got.Status != types.RunCompleted {
			t.Errorf("status = %q, want completed", got.Status)
		}
		if got.AwaitingAgentSince != nil {
			t.Errorf("AwaitingAgentSince = %d after completion, want nil", *got.AwaitingAgentSince)
		}
	})

	t.Run("failRun failed", func(t *testing.T) {
		database, p, run, repo := setupTest(t)
		parkTestRun(t, database, run.ID)

		exec := NewExecutor(database, p, nil, nil, nil, nil)
		_ = exec.failRun(run, repo, errors.New("step failed"))
		got, err := database.GetRun(run.ID)
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		if got.Status != types.RunFailed {
			t.Errorf("status = %q, want failed", got.Status)
		}
		if got.AwaitingAgentSince != nil {
			t.Errorf("AwaitingAgentSince = %d after failure, want nil", *got.AwaitingAgentSince)
		}
	})

	t.Run("failRun aborted by user", func(t *testing.T) {
		database, p, run, repo := setupTest(t)
		parkTestRun(t, database, run.ID)

		exec := NewExecutor(database, p, nil, nil, nil, nil)
		_ = exec.failRun(run, repo, errors.New(types.RunCancelReasonAbortedByUser))
		got, err := database.GetRun(run.ID)
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		if got.Status != types.RunCancelled {
			t.Errorf("status = %q, want cancelled", got.Status)
		}
		if got.AwaitingAgentSince != nil {
			t.Errorf("AwaitingAgentSince = %d after abort, want nil", *got.AwaitingAgentSince)
		}
	})
}
