package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestNoFixAcceptResponseRejectsExplicitFix(t *testing.T) {
	database, p, run, repo := setupTest(t)
	run.NoFix = true
	step := &adaptiveCallStep{name: types.StepReview, fn: func(*StepContext) (*StepOutcome, error) {
		return &StepOutcome{NeedsApproval: true, Findings: `{"findings":[]}`}, nil
	}}
	executor := NewExecutor(database, p, nil, nil, []Step{step}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- executor.Execute(ctx, run, repo, t.TempDir()) }()
	finished := false
	defer func() {
		cancel()
		if !finished {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("executor did not stop")
			}
		}
	}()
	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusParkedForApproval)

	fix := ipc.RespondParams{RunID: run.ID, Step: types.StepReview, Action: types.ActionFix, IdempotencyKey: "no-fix-explicit-fix"}
	if _, err := executor.AcceptResponse(fix); err == nil || !strings.Contains(err.Error(), "fix rounds are disabled") {
		t.Fatalf("explicit fix response error = %v, want no-fix rejection", err)
	}
	if receipt, err := database.GetResponseReceipt(run.ID, fix.IdempotencyKey); err != nil || receipt != nil {
		t.Fatalf("rejected fix receipt = %+v, %v; want no receipt", receipt, err)
	}
	if _, err := executor.AcceptResponse(ipc.RespondParams{RunID: run.ID, Step: types.StepReview, Action: types.ActionApprove, IdempotencyKey: "no-fix-approval"}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	finished = true
}
