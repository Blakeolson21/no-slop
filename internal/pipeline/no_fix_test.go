package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestNoFixRunDisablesConfiguredAndExplicitFixRounds(t *testing.T) {
	database, p, run, repo := setupTest(t)
	run.NoFix = true
	calls := 0
	step := &adaptiveCallStep{name: types.StepReview, fn: func(sctx *StepContext) (*StepOutcome, error) {
		calls++
		if sctx.Fixing {
			t.Error("no-fix run dispatched a fix round")
		}
		return &StepOutcome{AutoFixable: true, NeedsApproval: true, Findings: `{"findings":[{"id":"r1","severity":"warning","action":"auto-fix","description":"repair"}]}`}, nil
	}}
	executor := NewExecutor(database, p, &config.Config{AutoFix: config.AutoFix{Review: 3}}, nil, []Step{step}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	workDir := t.TempDir()
	done := make(chan error, 1)
	go func() { done <- executor.Execute(ctx, run, repo, workDir) }()
	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusParkedForApproval)
	if err := executor.Respond(types.StepReview, types.ActionFix, []string{"r1"}); err == nil {
		t.Error("explicit fix accepted on no-fix run")
	}
	if err := executor.Respond(types.StepReview, types.ActionApprove, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("executed %d rounds, want 1", calls)
	}
}
