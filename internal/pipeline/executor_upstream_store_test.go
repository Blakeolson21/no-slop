package pipeline

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/git"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestExecutorPinsUpstreamRunGenerationBeforeParking(t *testing.T) {
	database, p, _, repo := setupTest(t)
	f := newTerminalAdoptionFixture(t)
	store := filepath.Join(p.Root(), "stores", "fixture.git")
	if err := git.InitBare(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	gitOut(t, f.gate, "config", "no-slop.objectStore", store)
	run, err := database.InsertRun(repo.ID, "feature", f.submitted, f.base)
	if err != nil {
		t.Fatal(err)
	}
	fixed := f.selfCommit(t, "checkpoint candidate\n")
	step := &adaptiveCallStep{name: types.StepReview, fn: func(sctx *StepContext) (*StepOutcome, error) {
		if err := database.UpdateRunHeadSHA(run.ID, fixed); err != nil {
			return nil, err
		}
		sctx.Run.HeadSHA = fixed
		return &StepOutcome{NeedsApproval: true, Findings: `{"findings":[{"id":"r1","severity":"error","description":"adjudicate candidate","action":"ask-user"}]}`, ReviewApprovedHeadSHA: fixed}, nil
	}}
	executor := NewExecutor(database, p, &config.Config{}, nil, []Step{step}, nil)
	executor.SetGateDir(f.gate)
	done, _ := startExecutor(t, executor, run, repo, f.workDir)
	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusParkedForApproval)
	for _, oid := range []string{f.base, f.submitted, fixed} {
		if got := gitOut(t, store, "rev-parse", "--verify", "refs/runs/"+run.ID+"/"+oid); got != oid {
			t.Fatal("checkpoint did not retain run generation")
		}
	}
	if err := executor.Respond(types.StepReview, types.ActionApprove, nil); err != nil {
		t.Fatal(err)
	}
	waitExecutorDone(t, done)
}
