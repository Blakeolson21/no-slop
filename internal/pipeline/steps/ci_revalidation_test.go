package steps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/branchsync"
	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/pipeline"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func assertCIRestartsValidation(t *testing.T, outcome *pipeline.StepOutcome, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("CI repair returned error: %v", err)
	}
	if outcome == nil || outcome.RestartFrom != types.StepReview {
		t.Fatalf("CI repair outcome = %#v, want restart from review", outcome)
	}
}

func TestCIStep_RepairCommitStaysLocalAndInvalidatesReviewAuthority(t *testing.T) {
	t.Parallel()

	remote := t.TempDir()
	gitCmd(t, remote, "init", "--bare")

	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@test.com")
	gitCmd(t, dir, "checkout", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "base")
	baseSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "remote", "add", "origin", remote)
	gitCmd(t, dir, "push", "origin", "main")

	gitCmd(t, dir, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "feature")
	approvedHead := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "push", "origin", "feature")

	sctx := newTestContextWithDBRecords(t, &mockAgent{name: "test"}, dir, baseSHA, approvedHead, config.Commands{})
	sctx.Repo.UpstreamURL = remote
	sctx.Run.Branch = "refs/heads/feature"
	if err := sctx.DB.UpdateRunReviewApprovedHeadSHA(sctx.Run.ID, approvedHead); err != nil {
		t.Fatal(err)
	}
	sctx.Run.ReviewApprovedHeadSHA = &approvedHead
	if err := sctx.DB.UpdateRunPushBinding(sctx.Run.ID, db.PushBinding{
		HeadSHA:           approvedHead,
		TargetKind:        "upstream",
		TargetFingerprint: branchsync.TargetFingerprint(remote),
		Ref:               "refs/heads/feature",
	}); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "ci-fix.txt"), []byte("fixed"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := (&CIStep{}).commitAndPush(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("CI repair did not report a changed local head")
	}

	repairedHead := gitCmd(t, dir, "rev-parse", "HEAD")
	if repairedHead == approvedHead {
		t.Fatal("CI repair did not create a local commit")
	}
	if remoteHead := gitCmd(t, remote, "rev-parse", "refs/heads/feature"); remoteHead != approvedHead {
		t.Fatalf("remote head = %s, want stale approved head %s until revalidation", remoteHead, approvedHead)
	}
	if branchHead := gitCmd(t, dir, "rev-parse", "refs/heads/feature"); branchHead != repairedHead {
		t.Fatalf("local feature ref = %s, want repaired head %s", branchHead, repairedHead)
	}

	run, err := sctx.DB.GetRun(sctx.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.HeadSHA != repairedHead {
		t.Fatalf("durable run head = %s, want repaired head %s", run.HeadSHA, repairedHead)
	}
	if run.ReviewApprovedHeadSHA != nil {
		t.Fatalf("stale review authority survived CI repair: %s", *run.ReviewApprovedHeadSHA)
	}
	if run.LastPushedSHA == nil || *run.LastPushedSHA != approvedHead || run.PushGeneration == nil || *run.PushGeneration != 1 {
		t.Fatalf("prior push binding changed before Push republished: %#v", run)
	}

	_, err = (&PushStep{}).Execute(sctx)
	if err == nil || !strings.Contains(err.Error(), "no durably recorded review-approved head") {
		t.Fatalf("PushStep.Execute error = %v, want refusal for invalidated review authority", err)
	}
	remoteHead := gitCmd(t, remote, "rev-parse", "refs/heads/feature")
	if remoteHead != approvedHead {
		t.Fatalf("push after stale approval changed remote to %s, want %s", remoteHead, approvedHead)
	}
	t.Logf("CI repair safety evidence:\n  local repaired head: %s\n  remote feature head: %s\n  durable run head: %s\n  durable review authority: absent\n  retained prior push binding: %s (generation %d)\n  stale-authority push: refused (%v)", repairedHead, remoteHead, run.HeadSHA, *run.LastPushedSHA, *run.PushGeneration, err)
}

func TestCIStep_SuccessfulAutoRepairRestartsFromReview(t *testing.T) {
	t.Parallel()

	dir, baseSHA, approvedHead := setupGitRepo(t)
	ag := &mockAgent{
		name: "test",
		runFn: func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
			if err := os.WriteFile(filepath.Join(opts.CWD, "ci-fix.txt"), []byte("fixed"), 0o644); err != nil {
				return nil, err
			}
			return &agent.Result{Output: []byte(`{"summary":"stabilize CI repair"}`)}, nil
		},
	}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, approvedHead, config.Commands{})
	prURL := "https://github.com/test/repo/pull/42"
	sctx.Run.PRURL = &prURL
	sctx.Run.Branch = "refs/heads/feature"
	sctx.Env = fakeCIGH(t, "OPEN", `[{"name":"test","status":"COMPLETED","conclusion":"failure","bucket":"fail"}]`)
	sctx.Config.CITimeout = 30 * time.Second
	sctx.Config.AutoFix = config.AutoFix{CI: 1}
	if err := sctx.DB.UpdateRunReviewApprovedHeadSHA(sctx.Run.ID, approvedHead); err != nil {
		t.Fatal(err)
	}
	sctx.Run.ReviewApprovedHeadSHA = &approvedHead
	stepResult, err := sctx.DB.InsertStepResult(sctx.Run.ID, types.StepCI)
	if err != nil {
		t.Fatal(err)
	}
	sctx.StepResultID = stepResult.ID

	outcome, err := (&CIStep{
		waitForNextPoll: func(context.Context, time.Duration) error {
			return errors.New("CI repair incorrectly resumed polling")
		},
	}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if outcome == nil || outcome.RestartFrom != types.StepReview {
		t.Fatalf("CI repair outcome = %#v, want restart from review", outcome)
	}
	if message := gitCmd(t, dir, "log", "-1", "--format=%s"); message != "no-slop(ci): stabilize CI repair" {
		t.Fatalf("CI repair commit message = %q, want agent summary rendered through commit.fix_message", message)
	}
}

func TestCIStep_RevalidationCanRepairSameFailureAgainWithoutCompletionTime(t *testing.T) {
	t.Parallel()

	dir, baseSHA, approvedHead := setupGitRepo(t)
	fixCalls := 0
	ag := &mockAgent{
		name: "test",
		runFn: func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
			fixCalls++
			path := filepath.Join(opts.CWD, fmt.Sprintf("ci-fix-%d.txt", fixCalls))
			if err := os.WriteFile(path, []byte("fixed"), 0o644); err != nil {
				return nil, err
			}
			return &agent.Result{Output: []byte(`{"summary":"repair recurring failure"}`)}, nil
		},
	}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, approvedHead, config.Commands{})
	prURL := "https://github.com/test/repo/pull/42"
	sctx.Run.PRURL = &prURL
	sctx.Run.Branch = "refs/heads/feature"
	sctx.Env = fakeCIGH(t, "OPEN", `[{"name":"test","status":"COMPLETED","conclusion":"failure","bucket":"fail"}]`)
	sctx.Config.CITimeout = 30 * time.Second
	sctx.Config.AutoFix = config.AutoFix{CI: 3}
	stepResult, err := sctx.DB.InsertStepResult(sctx.Run.ID, types.StepCI)
	if err != nil {
		t.Fatal(err)
	}
	sctx.StepResultID = stepResult.ID
	step := &CIStep{waitForNextPoll: func(context.Context, time.Duration) error {
		return errors.New("repeated failure was suppressed")
	}}

	outcome, err := step.Execute(sctx)
	assertCIRestartsValidation(t, outcome, err)
	outcome, err = step.Execute(sctx)
	assertCIRestartsValidation(t, outcome, err)
	if fixCalls != 2 {
		t.Fatalf("repair calls = %d, want 2", fixCalls)
	}
	persisted, err := sctx.DB.GetStepResult(stepResult.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted == nil || persisted.CIFixAttempts != 2 {
		t.Fatalf("durable CI repair attempts = %#v, want 2", persisted)
	}
}

func TestCIStep_PersistenceFailureAfterRepairDoesNotResumePolling(t *testing.T) {
	t.Parallel()

	dir, baseSHA, approvedHead := setupGitRepo(t)
	gitCmd(t, dir, "checkout", "--detach", approvedHead)
	var sctx *pipeline.StepContext
	ag := &mockAgent{
		name: "test",
		runFn: func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
			if err := os.WriteFile(filepath.Join(opts.CWD, "ci-fix.txt"), []byte("fixed"), 0o644); err != nil {
				return nil, err
			}
			if err := sctx.DB.Close(); err != nil {
				return nil, err
			}
			return &agent.Result{Output: []byte(`{"summary":"repair before persistence failure"}`)}, nil
		},
	}
	sctx = newTestContextWithDBRecords(t, ag, dir, baseSHA, approvedHead, config.Commands{})
	prURL := "https://github.com/test/repo/pull/42"
	sctx.Run.PRURL = &prURL
	sctx.Run.Branch = "refs/heads/feature"
	sctx.Env = fakeCIGH(t, "OPEN", `[{"name":"test","status":"COMPLETED","conclusion":"failure","bucket":"fail"}]`)
	sctx.Config.CITimeout = 30 * time.Second
	sctx.Config.AutoFix = config.AutoFix{CI: 1}
	if err := sctx.DB.UpdateRunReviewApprovedHeadSHA(sctx.Run.ID, approvedHead); err != nil {
		t.Fatal(err)
	}
	sctx.Run.ReviewApprovedHeadSHA = &approvedHead
	waitCalls := 0
	step := &CIStep{waitForNextPoll: func(context.Context, time.Duration) error {
		waitCalls++
		return errors.New("CI resumed polling after a partial repair")
	}}

	outcome, err := step.Execute(sctx)
	if err == nil || !strings.Contains(err.Error(), "persist uncertified review range before CI head adoption") {
		t.Fatalf("CI outcome = %#v, error = %v, want actionable persistence failure", outcome, err)
	}
	if waitCalls != 0 {
		t.Fatalf("CI resumed polling %d times after the repaired head advanced", waitCalls)
	}
	repairedHead := gitCmd(t, dir, "rev-parse", "HEAD")
	if repairedHead == approvedHead || sctx.Run.HeadSHA != approvedHead {
		t.Fatalf("unadopted repair head = %s, in-memory head = %s, approved head = %s", repairedHead, sctx.Run.HeadSHA, approvedHead)
	}
	if branchHead := gitCmd(t, dir, "rev-parse", "refs/heads/feature"); branchHead != approvedHead {
		t.Fatalf("branch adopted repair despite failed recovery persistence: got %s, want %s", branchHead, approvedHead)
	}
	if sctx.Run.ReviewApprovedHeadSHA == nil || *sctx.Run.ReviewApprovedHeadSHA != approvedHead {
		t.Fatalf("review authority = %#v, want stale authority retained only on the aborted path", sctx.Run.ReviewApprovedHeadSHA)
	}
}

func TestCIStep_AutoRepairAttemptBudgetSurvivesStepRecovery(t *testing.T) {
	t.Parallel()

	dir, baseSHA, headSHA := setupGitRepo(t)
	fixCalls := 0
	ag := &mockAgent{
		name: "test",
		runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
			fixCalls++
			return &agent.Result{}, nil
		},
	}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	prURL := "https://github.com/test/repo/pull/42"
	sctx.Run.PRURL = &prURL
	sctx.Run.Branch = "refs/heads/feature"
	sctx.Env = fakeCIGH(t, "OPEN", `[{"name":"test","status":"COMPLETED","conclusion":"failure","bucket":"fail"}]`)
	sctx.Config.CITimeout = 30 * time.Second
	sctx.Config.AutoFix = config.AutoFix{CI: 1}
	stepResult, err := sctx.DB.InsertStepResult(sctx.Run.ID, types.StepCI)
	if err != nil {
		t.Fatal(err)
	}
	sctx.StepResultID = stepResult.ID

	outcome, err := (&CIStep{waitForNextPoll: func(context.Context, time.Duration) error { return nil }}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if outcome == nil || !outcome.NeedsApproval {
		t.Fatalf("first CI outcome = %#v, want approval after the only repair attempt produced no changes", outcome)
	}
	if fixCalls != 1 {
		t.Fatalf("repair calls = %d, want 1", fixCalls)
	}
	persisted, err := sctx.DB.GetStepResult(stepResult.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted == nil || persisted.CIFixAttempts != 1 {
		t.Fatalf("durable CI repair attempts = %#v, want 1", persisted)
	}

	outcome, err = (&CIStep{waitForNextPoll: func(context.Context, time.Duration) error { return nil }}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if outcome == nil || !outcome.NeedsApproval {
		t.Fatalf("recovered CI outcome = %#v, want exhausted-budget approval", outcome)
	}
	if fixCalls != 1 {
		t.Fatalf("recovered CI spent a second repair attempt; calls = %d", fixCalls)
	}
}
