package pipeline

import (
	"context"
	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/types"
	"testing"
)

func TestReviewerMissingRangeTipRequiresFreshReview(t *testing.T) {
	d, _, run, repo := setupTest(t)
	dir := t.TempDir()
	initGitRepo(t, dir)
	base := currentSHA(t, dir)
	execGit(t, dir, "checkout", "-b", "feature")
	writeTestFile(t, dir, "author.txt", "author\n")
	execGit(t, dir, "add", ".")
	execGit(t, dir, "commit", "-m", "author")
	old := currentSHA(t, dir)
	execGit(t, dir, "checkout", "-b", "newmain", base)
	writeTestFile(t, dir, "main.txt", "main\n")
	execGit(t, dir, "add", ".")
	execGit(t, dir, "commit", "-m", "main")
	execGit(t, dir, "checkout", "feature")
	execGit(t, dir, "rebase", "newmain")
	head := currentSHA(t, dir)
	if _, err := d.InsertStepResult(run.ID, types.StepReview); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertUncertifiedPipelineRange(repo.ID, run.Branch, old, "ffffffffffffffffffffffffffffffffffffffff", run.ID); err != nil {
		t.Fatal(err)
	}
	s := &StepContext{Ctx: context.Background(), DB: d, Repo: repo, Run: run, WorkDir: dir}
	if _, err := RemapUncertifiedPipelineRangeAfterRebase(s, old, head); err != nil {
		t.Fatalf("missing object blocked fresh review: %v", err)
	}
	got, err := d.GetUncertifiedPipelineRange(repo.ID, run.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if got.RecoveryState != db.ReviewRecoveryFreshReviewRequired || got.FromSHA != head || got.ToSHA != head {
		t.Fatalf("stale state: %+v", got)
	}
}
