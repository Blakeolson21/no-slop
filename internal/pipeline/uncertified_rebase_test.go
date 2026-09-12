package pipeline

import (
	"context"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// A cancelled selection may have no fixer commit at all. Its equal endpoints
// remain valid review provenance even when the submitted head predates main.
func TestRemapUncertifiedNoDeltaAfterMainAdvance(t *testing.T) {
	database, _, run, repo := setupTest(t)
	dir := t.TempDir()
	initGitRepo(t, dir)
	base := currentSHA(t, dir)
	execGit(t, dir, "checkout", "-b", "feature")
	writeTestFile(t, dir, "author.txt", "author\n")
	execGit(t, dir, "add", ".")
	execGit(t, dir, "commit", "-m", "author")
	old := currentSHA(t, dir)
	if _, err := database.InsertStepResult(run.ID, types.StepReview); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertUncertifiedPipelineRangeRecovery(repo.ID, run.Branch, old, old, run.ID, db.ReviewRecoverySelectionRecoveredNoDelta); err != nil {
		t.Fatal(err)
	}
	execGit(t, dir, "checkout", "-b", "newbase", base)
	writeTestFile(t, dir, "main.txt", "main\n")
	execGit(t, dir, "add", ".")
	execGit(t, dir, "commit", "-m", "main advance")
	main := currentSHA(t, dir)
	execGit(t, dir, "checkout", "feature")
	execGit(t, dir, "rebase", "newbase")
	head := currentSHA(t, dir)
	sctx := &StepContext{Ctx: context.Background(), DB: database, Repo: repo, Run: run, WorkDir: dir}
	t.Logf("base=%s old=%s range=%s..%s main=%s rebased=%s", base, old, old, old, main, head)
	rollback, err := RemapUncertifiedPipelineRangeAfterRebase(sctx, old, head)
	if err != nil {
		t.Fatal(err)
	}
	got, err := database.GetUncertifiedPipelineRange(repo.ID, run.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if got.FromSHA != head || got.ToSHA != head || got.RecoveryState != db.ReviewRecoverySelectionRecoveredNoDelta {
		t.Fatalf("remapped no-delta state = %#v", got)
	}
	if _, err := RemapUncertifiedPipelineRangeAfterRebase(sctx, old, head); err != nil {
		t.Fatalf("repeat remap: %v", err)
	}
	if rollback == nil {
		t.Fatal("missing adoption rollback")
	}
	if err := rollback(); err != nil {
		t.Fatal(err)
	}
	got, _ = database.GetUncertifiedPipelineRange(repo.ID, run.Branch)
	if got.FromSHA != old || got.ToSHA != old {
		t.Fatalf("rollback = %#v", got)
	}
}

func TestRemapUncertifiedDroppedCommitRequiresFreshReview(t *testing.T) {
	database, _, run, repo := setupTest(t)
	dir := t.TempDir()
	initGitRepo(t, dir)
	base := currentSHA(t, dir)
	execGit(t, dir, "checkout", "-b", "feature")
	writeTestFile(t, dir, "author.txt", "author\n")
	execGit(t, dir, "add", ".")
	execGit(t, dir, "commit", "-m", "author")
	from := currentSHA(t, dir)
	writeTestFile(t, dir, "fix.txt", "fix\n")
	execGit(t, dir, "add", ".")
	execGit(t, dir, "commit", "-m", "fix")
	old := currentSHA(t, dir)
	if _, err := database.InsertStepResult(run.ID, types.StepReview); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertUncertifiedPipelineRange(repo.ID, run.Branch, from, old, run.ID); err != nil {
		t.Fatal(err)
	}
	execGit(t, dir, "checkout", "-b", "newbase", base)
	// Main has independently landed the fix; Git drops it during rebase.
	execGit(t, dir, "cherry-pick", old)
	execGit(t, dir, "checkout", "feature")
	execGit(t, dir, "rebase", "newbase")
	head := currentSHA(t, dir)
	sctx := &StepContext{Ctx: context.Background(), DB: database, Repo: repo, Run: run, WorkDir: dir}
	if _, err := RemapUncertifiedPipelineRangeAfterRebase(sctx, old, head); err != nil {
		t.Fatal(err)
	}
	got, err := database.GetUncertifiedPipelineRange(repo.ID, run.Branch)
	if err != nil {
		t.Fatal(err)
	}
	// A distance-based guess points at the author's commit, falsely attributing
	// its delta to the fixer. Unknown correspondence must demand a full review.
	if got.RecoveryState != "fresh_review_required" {
		t.Fatalf("unproved remap kept stale selection state: %#v", got)
	}
	run.HeadSHA = head
	if err := BindUncertifiedPipelineRange(sctx); err != nil {
		t.Fatal(err)
	}
	if sctx.Fixing || sctx.SkipFixExecution || sctx.UncertifiedToSHA != head {
		t.Fatalf("fresh review not bound: fixing=%t skip=%t tip=%s", sctx.Fixing, sctx.SkipFixExecution, sctx.UncertifiedToSHA)
	}
}

func TestBindLegacyNoDeltaWithoutSelectionRequiresFreshReview(t *testing.T) {
	d, _, run, repo := setupTest(t)
	dir := t.TempDir()
	initGitRepo(t, dir)
	head := currentSHA(t, dir)
	run.HeadSHA = head
	sr, err := d.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	findings := `{"findings":[{"id":"bug","severity":"error","description":"unresolved","action":"ask-user"}]}`
	if err = d.SetStepFindings(sr.ID, findings); err != nil {
		t.Fatal(err)
	}
	if err = d.UpsertUncertifiedPipelineRangeState(repo.ID, run.Branch, head, head, run.ID, false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		sctx := &StepContext{Ctx: context.Background(), DB: d, Repo: repo, Run: run, WorkDir: dir}
		if err = BindUncertifiedPipelineRange(sctx); err != nil {
			t.Fatal(err)
		}
		if sctx.Fixing || sctx.SkipFixExecution || sctx.UncertifiedPriorFindings != findings {
			t.Fatalf("bind discarded truth or reused selection: %+v", sctx)
		}
		rng, err := d.GetUncertifiedPipelineRange(repo.ID, run.Branch)
		if err != nil || rng.RecoveryState != db.ReviewRecoveryFreshReviewRequired {
			t.Fatalf("state=%+v %v", rng, err)
		}
	}
}
