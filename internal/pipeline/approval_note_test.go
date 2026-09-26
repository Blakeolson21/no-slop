package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestApprovalNotePersistsOnAdjudicatedRound(t *testing.T) {
	database, p, run, repo := setupTest(t)
	step := &adaptiveCallStep{name: types.StepReview, fn: func(*StepContext) (*StepOutcome, error) {
		return &StepOutcome{NeedsApproval: true, Findings: `{"findings":[{"id":"r1","description":"decision","severity":"warning","action":"ask-user"}]}`}, nil
	}}
	executor := NewExecutor(database, p, nil, nil, []Step{step}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- executor.Execute(ctx, run, repo, t.TempDir()) }()
	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusParkedForApproval)
	const note = "Accepted: the caller already validates this input."
	if err := executor.RespondWithOverrides(types.StepReview, types.ActionApprove, nil, nil, nil, note); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	steps, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	rounds, err := database.GetRoundsByStep(steps[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rounds) != 1 || rounds[0].ApprovalNote == nil || *rounds[0].ApprovalNote != note {
		t.Fatalf("approval reason was not persisted: %#v", rounds)
	}
}
