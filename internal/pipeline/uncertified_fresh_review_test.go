package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// divergentRebase builds a branch whose commit is rebased onto an advanced
// default branch, and returns the worktree plus the pre- and post-rebase heads.
func divergentRebase(t *testing.T) (dir, oldHead, newHead string) {
	t.Helper()
	dir = t.TempDir()
	initGitRepo(t, dir)
	base := currentSHA(t, dir)
	execGit(t, dir, "checkout", "-b", "feature")
	writeTestFile(t, dir, "author.txt", "author\n")
	execGit(t, dir, "add", ".")
	execGit(t, dir, "commit", "-m", "author")
	oldHead = currentSHA(t, dir)
	execGit(t, dir, "checkout", "-b", "newmain", base)
	writeTestFile(t, dir, "main.txt", "main\n")
	execGit(t, dir, "add", ".")
	execGit(t, dir, "commit", "-m", "main")
	execGit(t, dir, "checkout", "feature")
	execGit(t, dir, "rebase", "newmain")
	return dir, oldHead, currentSHA(t, dir)
}

// The old code answered a readable tip that reaches neither head with a silent
// no-op, leaving a span that describes nothing about this branch persisted as
// if it still did. It is unprovable provenance like any other: it owes the new
// head a fresh review instead of sitting there looking authoritative.
func TestRemapUncertifiedRange_UnrelatedTipDoesNotStayTrustedByNoOp(t *testing.T) {
	database, _, run, repo := setupTest(t)
	dir, oldHead, newHead := divergentRebase(t)
	// A real commit on an unrelated branch: readable, and an ancestor of
	// neither head.
	execGit(t, dir, "checkout", "-b", "unrelated", oldHead)
	writeTestFile(t, dir, "unrelated.txt", "unrelated\n")
	execGit(t, dir, "add", ".")
	execGit(t, dir, "commit", "-m", "unrelated")
	unrelated := currentSHA(t, dir)
	execGit(t, dir, "checkout", "feature")

	if err := database.UpsertUncertifiedPipelineRange(repo.ID, run.Branch, unrelated, unrelated, run.ID); err != nil {
		t.Fatal(err)
	}
	sctx := &StepContext{Ctx: context.Background(), DB: database, Repo: repo, Run: run, WorkDir: dir}
	if _, err := RemapUncertifiedPipelineRangeAfterRebase(sctx, oldHead, newHead); err != nil {
		t.Fatalf("RemapUncertifiedPipelineRangeAfterRebase() error = %v", err)
	}
	got, err := database.GetUncertifiedPipelineRange(repo.ID, run.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.RecoveryState != db.ReviewRecoveryFreshReviewRequired {
		t.Fatalf("unrelated range stayed trusted: %#v", got)
	}
	if got.FromSHA != newHead || got.ToSHA != newHead || got.SourceRunID != run.ID {
		t.Fatalf("fresh review lost its anchor or source run: %#v", got)
	}
}

// A tip still reachable from the rebased head needs no remap at all, even when
// the pre-rebase head no longer reaches it.
func TestRemapUncertifiedRange_TipLiveInRebasedHeadIsLeftAlone(t *testing.T) {
	database, _, run, repo := setupTest(t)
	dir, oldHead, newHead := divergentRebase(t)
	if err := database.UpsertUncertifiedPipelineRange(repo.ID, run.Branch, newHead, newHead, run.ID); err != nil {
		t.Fatal(err)
	}
	sctx := &StepContext{Ctx: context.Background(), DB: database, Repo: repo, Run: run, WorkDir: dir}
	rollback, err := RemapUncertifiedPipelineRangeAfterRebase(sctx, oldHead, newHead)
	if err != nil {
		t.Fatalf("RemapUncertifiedPipelineRangeAfterRebase() error = %v", err)
	}
	if rollback != nil {
		t.Fatal("live range was rewritten")
	}
	got, err := database.GetUncertifiedPipelineRange(repo.ID, run.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.RecoveryState == db.ReviewRecoveryFreshReviewRequired {
		t.Fatalf("live range was invalidated: %#v", got)
	}
}

// Cancellation is a fault, not unprovable provenance. A run shutting down must
// not rewrite a persisted range on the way out.
func TestRemapUncertifiedRange_CancelledContextDoesNotRewriteRange(t *testing.T) {
	database, _, run, repo := setupTest(t)
	dir, oldHead, newHead := divergentRebase(t)
	if err := database.UpsertUncertifiedPipelineRange(repo.ID, run.Branch, "from-missing", "to-missing", run.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sctx := &StepContext{Ctx: ctx, DB: database, Repo: repo, Run: run, WorkDir: dir}
	if _, err := RemapUncertifiedPipelineRangeAfterRebase(sctx, oldHead, newHead); err == nil {
		t.Fatal("cancelled remap reported success")
	}
	got, err := database.GetUncertifiedPipelineRange(repo.ID, run.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.FromSHA != "from-missing" || got.ToSHA != "to-missing" {
		t.Fatalf("cancelled remap rewrote range: %#v", got)
	}
}

// The review step used to fail outright when the persisted tip named an object
// the gate cannot read. It now binds a fresh full review of this head instead.
func TestBindUncertifiedRange_UnreadableTipRequiresFreshReviewInsteadOfFailing(t *testing.T) {
	database, _, run, repo := setupTest(t)
	dir := t.TempDir()
	initGitRepo(t, dir)
	head := currentSHA(t, dir)
	run.HeadSHA = head
	if err := database.UpdateRunHeadSHA(run.ID, head); err != nil {
		t.Fatal(err)
	}
	sourceStep, err := database.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	findings := `{"findings":[{"id":"bug","severity":"error","description":"claim","action":"auto-fix"}]}`
	if err = database.SetStepFindings(sourceStep.ID, findings); err != nil {
		t.Fatal(err)
	}
	if err = database.UpsertUncertifiedPipelineRangeRecovery(repo.ID, run.Branch, "from-missing", "to-missing", run.ID, db.ReviewRecoverySelectionRecoveredNoDelta); err != nil {
		t.Fatal(err)
	}

	sctx := &StepContext{Ctx: context.Background(), DB: database, Repo: repo, Run: run, WorkDir: dir}
	if err = BindUncertifiedPipelineRange(sctx); err != nil {
		t.Fatalf("BindUncertifiedPipelineRange() error = %v, want fresh-review transition", err)
	}
	got, err := database.GetUncertifiedPipelineRange(repo.ID, run.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.RecoveryState != db.ReviewRecoveryFreshReviewRequired {
		t.Fatalf("unreadable tip kept its recovery state: %#v", got)
	}
	if got.FromSHA != head || got.ToSHA != head || got.SourceRunID != run.ID {
		t.Fatalf("fresh review lost its anchor or source run: %#v", got)
	}
	// A fresh-review marker is a debt, not a certificate: it must not seat a
	// review-only round, must not carry a selection, and must not approve.
	if sctx.Fixing || sctx.SkipFixExecution || sctx.UncertifiedSelectedFindings != "" {
		t.Fatalf("fresh review granted head approval: fixing=%v skipFix=%v selected=%q",
			sctx.Fixing, sctx.SkipFixExecution, sctx.UncertifiedSelectedFindings)
	}
	// The claims themselves survive, so the replacement reviewer is not cold.
	if !strings.Contains(sctx.UncertifiedPriorFindings, `"bug"`) {
		t.Fatalf("fresh review dropped prior claims: %q", sctx.UncertifiedPriorFindings)
	}
	if sctx.UncertifiedSourceRunID != run.ID {
		t.Fatalf("fresh review dropped source run: %q", sctx.UncertifiedSourceRunID)
	}
}

// A tip that is readable and simply not in this head's lineage is a different
// answer from one that cannot be read: it belongs to other work, so it stays
// exactly as persisted and this run applies no provenance.
func TestBindUncertifiedRange_ReadableOutOfLineageTipIsLeftUntouched(t *testing.T) {
	database, _, run, repo := setupTest(t)
	dir := t.TempDir()
	initGitRepo(t, dir)
	base := currentSHA(t, dir)
	execGit(t, dir, "checkout", "-b", "other")
	writeTestFile(t, dir, "other.txt", "other\n")
	execGit(t, dir, "add", ".")
	execGit(t, dir, "commit", "-m", "other")
	other := currentSHA(t, dir)
	execGit(t, dir, "checkout", "-")
	run.HeadSHA = base
	if err := database.UpdateRunHeadSHA(run.ID, base); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertUncertifiedPipelineRange(repo.ID, run.Branch, other, other, run.ID); err != nil {
		t.Fatal(err)
	}
	sctx := &StepContext{Ctx: context.Background(), DB: database, Repo: repo, Run: run, WorkDir: dir}
	if err := BindUncertifiedPipelineRange(sctx); err != nil {
		t.Fatalf("BindUncertifiedPipelineRange() error = %v", err)
	}
	if sctx.UncertifiedFromSHA != "" || sctx.UncertifiedToSHA != "" {
		t.Fatalf("out-of-lineage range was applied: from=%q to=%q", sctx.UncertifiedFromSHA, sctx.UncertifiedToSHA)
	}
	got, err := database.GetUncertifiedPipelineRange(repo.ID, run.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.FromSHA != other || got.ToSHA != other || got.RecoveryState == db.ReviewRecoveryFreshReviewRequired {
		t.Fatalf("out-of-lineage range was rewritten: %#v", got)
	}
}

// The discriminator between "this worktree cannot answer anything" and "this
// worktree does not have that commit" is the head, which the caller just
// observed. A head that does not resolve means the execution context is broken
// and the failure must stay an error.
func TestRangeTipAncestry_SeparatesABrokenWorktreeFromAnAbsentTip(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	head := currentSHA(t, dir)

	sctx := &StepContext{Ctx: context.Background(), WorkDir: dir}
	if inLineage, provable, err := rangeTipAncestry(sctx, head, head); err != nil || !inLineage || !provable {
		t.Fatalf("resolved equal commits: lineage=%v provable=%v err=%v", inLineage, provable, err)
	}
	missing := strings.Repeat("e", 40)
	if inLineage, provable, err := rangeTipAncestry(sctx, missing, missing); err == nil || inLineage || provable {
		t.Errorf("equal absent commits: lineage=%v provable=%v err=%v, want refusal", inLineage, provable, err)
	}
	if _, provable, err := rangeTipAncestry(sctx, strings.Repeat("f", 40), head); err != nil || provable {
		t.Fatalf("absent tip: provable=%v err=%v, want unprovable without error", provable, err)
	}
	if _, _, err := rangeTipAncestry(sctx, strings.Repeat("f", 40), strings.Repeat("e", 40)); err == nil {
		t.Fatal("unresolvable head reported no error")
	}

	broken := &StepContext{Ctx: context.Background(), WorkDir: t.TempDir()}
	if inLineage, provable, err := rangeTipAncestry(broken, head, head); err == nil || inLineage || provable {
		t.Fatalf("equal commits in non-repository: lineage=%v provable=%v err=%v, want refusal", inLineage, provable, err)
	}
	if _, _, err := rangeTipAncestry(broken, strings.Repeat("f", 40), head); err == nil {
		t.Fatal("non-repository worktree reported no error")
	}
}
