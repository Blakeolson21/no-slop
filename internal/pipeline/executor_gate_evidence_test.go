package pipeline

import (
	"testing"

	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// TestExecutor_ReviewRoundsPinReviewedTreesInTheGateMirror exercises the wiring
// the run manager installs in production: with a gate mirror configured, every
// persisted review round that recorded a reviewed head leaves a deterministic
// refs/gate-evidence/<run>/<round> ref in that mirror, and only there. Without
// the executor's pin the refs never exist, so a completed run keeps no
// independent record of which tree each review round actually adjudicated.
func TestExecutor_ReviewRoundsPinReviewedTreesInTheGateMirror(t *testing.T) {
	database, p, _, repo := setupTest(t)
	f := newTerminalAdoptionFixture(t)
	run, err := database.InsertRun(repo.ID, "feature", f.submitted, f.base)
	if err != nil {
		t.Fatal(err)
	}
	fixed := f.selfCommit(t, "feature, fixed by the agent\n")

	calls := 0
	step := &adaptiveCallStep{name: types.StepReview, fn: func(sctx *StepContext) (*StepOutcome, error) {
		calls++
		if calls == 1 {
			return &StepOutcome{
				NeedsApproval:         true,
				Findings:              `{"findings":[{"id":"r1","severity":"error","description":"fix me","action":"auto-fix"}]}`,
				ReviewApprovedHeadSHA: f.submitted,
			}, nil
		}
		return &StepOutcome{ReviewApprovedHeadSHA: fixed}, nil
	}}
	exec := NewExecutor(database, p, &config.Config{}, nil, []Step{step}, nil)
	exec.SetGateDir(f.gate)

	done, _ := startExecutor(t, exec, run, repo, f.workDir)
	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)

	// The first round is pinned as soon as it is persisted, before the gate
	// parks for a decision: a run that is never resumed still keeps evidence.
	firstRef := "refs/gate-evidence/" + run.ID + "/1"
	if got := gitOut(t, f.gate, "rev-parse", "--verify", firstRef+"^{commit}"); got != f.submitted {
		t.Fatalf("round 1 evidence ref = %s, want %s", got, f.submitted)
	}

	if err := exec.Respond(types.StepReview, types.ActionFix, nil); err != nil {
		t.Fatal(err)
	}
	waitExecutorDone(t, done)

	secondRef := "refs/gate-evidence/" + run.ID + "/2"
	if got := gitOut(t, f.gate, "rev-parse", "--verify", secondRef+"^{commit}"); got != fixed {
		t.Fatalf("round 2 evidence ref = %s, want %s", got, fixed)
	}
	// Each round keeps its own tree: the rereview must not overwrite what the
	// first round adjudicated.
	if got := gitOut(t, f.gate, "rev-parse", "--verify", firstRef+"^{commit}"); got != f.submitted {
		t.Fatalf("round 1 evidence ref changed to %s, want %s", got, f.submitted)
	}

	// The developer's own clone is a separate ref store and must stay clean.
	if refs := gitOut(t, f.seed, "for-each-ref", "--format=%(refname)", "refs/gate-evidence/"); refs != "" {
		t.Fatalf("developer clone gained gate custody refs: %s", refs)
	}
	// The artifact branch published by the evidence-publishing path is a
	// separate mechanism and is untouched by round pinning.
	if refs := gitOut(t, f.gate, "for-each-ref", "--format=%(refname)", "refs/heads/no-mistakes/"); refs != "" {
		t.Fatalf("round pinning touched the evidence artifact branch: %s", refs)
	}
}

// TestExecutor_ReviewRoundWithoutReviewedHeadPinsNothing keeps the no-op path
// honest end to end: a review round that records no reviewed head must complete
// normally and leave the gate-evidence namespace empty rather than fail the run
// or pin a placeholder.
func TestExecutor_ReviewRoundWithoutReviewedHeadPinsNothing(t *testing.T) {
	database, p, _, repo := setupTest(t)
	f := newTerminalAdoptionFixture(t)
	run, err := database.InsertRun(repo.ID, "feature", f.submitted, f.base)
	if err != nil {
		t.Fatal(err)
	}
	step := &mockStep{name: types.StepReview, outcome: &StepOutcome{ExitCode: 0}}
	exec := NewExecutor(database, p, &config.Config{}, nil, []Step{step}, nil)
	exec.SetGateDir(f.gate)

	done, _ := startExecutor(t, exec, run, repo, f.workDir)
	waitExecutorDone(t, done)

	if refs := gitOut(t, f.gate, "for-each-ref", "--format=%(refname)", "refs/gate-evidence/"); refs != "" {
		t.Fatalf("review round without a reviewed head pinned refs: %s", refs)
	}
	steps, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		if s.StepName == types.StepReview && s.Status != types.StepStatusCompleted {
			t.Fatalf("review status = %s, want completed", s.Status)
		}
	}
}
