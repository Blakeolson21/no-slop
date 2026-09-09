package db

import (
	"context"
	"errors"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestActiveGateStepsSelectsOnlyActiveAncestryAcrossEstate(t *testing.T) {
	d := openTestDB(t)
	repo, err := d.InsertRepo(t.TempDir(), "https://example.test/repo", "main")
	if err != nil {
		t.Fatal(err)
	}
	want := make(map[string]int)
	for _, runStatus := range []types.RunStatus{types.RunPending, types.RunRunning, types.RunCompleted, types.RunCancelled, types.RunFailed} {
		for range 8 {
			run, err := d.InsertRun(repo.ID, "feature", "head", "base")
			if err != nil {
				t.Fatal(err)
			}
			if err := d.UpdateRunStatus(run.ID, runStatus); err != nil {
				t.Fatal(err)
			}
			for n, status := range []types.StepStatus{types.StepStatusPending, types.StepStatusRunning, types.StepStatusFixing, types.StepStatusAwaitingApproval, types.StepStatusFixReview, types.StepStatusCompleted, types.StepStatusFailed, types.StepStatusSkipped} {
				step, err := d.InsertStepResult(run.ID, types.StepReview)
				if err != nil {
					t.Fatal(err)
				}
				var pid *int
				if n%2 == 0 {
					value := 4000 + n
					pid = &value
				}
				if _, err := d.sql.Exec(`UPDATE step_results SET status = ?, agent_pid = ? WHERE id = ?`, status, pid, step.ID); err != nil {
					t.Fatal(err)
				}
				if (runStatus == types.RunPending || runStatus == types.RunRunning) && n >= 1 && n <= 4 {
					value := 0
					if pid != nil {
						value = *pid
					}
					want[run.ID] += value
				}
			}
		}
	}
	got, err := d.ActiveGateSteps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 64 {
		t.Fatalf("active steps = %d, want 64", len(got))
	}
	sums := make(map[string]int)
	for _, step := range got {
		if _, ok := want[step.RunID]; !ok || step.RepoID != repo.ID || step.Phase != types.StepReview {
			t.Fatalf("unexpected active step: %+v", step)
		}
		sums[step.RunID] += step.AgentPID
	}
	for runID, sum := range want {
		if sums[runID] != sum {
			t.Errorf("run %s PID sum = %d, want %d", runID, sums[runID], sum)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.ActiveGateSteps(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query = %v", err)
	}
}
