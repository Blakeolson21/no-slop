package steps

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/branchsync"
	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/scm"
	"github.com/Blakeolson21/no-slop/internal/types"
)

type recordingPRContentHost struct {
	content  scm.PRContent
	updates  []scm.PRContent
	getCalls int
	getErr   error
}

func (h *recordingPRContentHost) GetPRContent(context.Context, *scm.PR) (scm.PRContent, error) {
	h.getCalls++
	return h.content, h.getErr
}

func (h *recordingPRContentHost) UpdatePR(_ context.Context, _ *scm.PR, content scm.PRContent) (*scm.PR, error) {
	h.updates = append(h.updates, content)
	h.content = content
	return &scm.PR{Number: "42"}, nil
}

func (h *recordingPRContentHost) Provider() scm.Provider { return scm.ProviderGitHub }
func (h *recordingPRContentHost) Capabilities() scm.Capabilities {
	return scm.Capabilities{}
}
func (h *recordingPRContentHost) Available(context.Context) error { return nil }
func (h *recordingPRContentHost) FindPR(context.Context, string, string) (*scm.PR, error) {
	return nil, nil
}
func (h *recordingPRContentHost) CreatePR(context.Context, string, string, scm.PRContent) (*scm.PR, error) {
	return nil, nil
}
func (h *recordingPRContentHost) GetPRState(context.Context, *scm.PR) (scm.PRState, error) {
	return scm.PRStateOpen, nil
}
func (h *recordingPRContentHost) GetChecks(context.Context, *scm.PR) ([]scm.Check, error) {
	return nil, nil
}
func (h *recordingPRContentHost) GetMergeableState(context.Context, *scm.PR) (scm.MergeableState, error) {
	return scm.MergeableUnknown, scm.ErrUnsupported
}
func (h *recordingPRContentHost) FetchFailedCheckLogs(context.Context, *scm.PR, string, string, []string) (string, error) {
	return "", scm.ErrUnsupported
}

func TestCIStep_RefreshPRAttestationBindsCurrentHead(t *testing.T) {
	dir, baseSHA, headSHA := setupGitRepo(t)
	sctx := newTestContextWithDBRecords(t, &mockAgent{name: "test"}, dir, baseSHA, headSHA, config.Commands{})
	var steps []*db.StepResult
	for _, name := range []types.StepName{types.StepReview, types.StepTest, types.StepDocument} {
		step, err := sctx.DB.InsertStepResult(sctx.Run.ID, name)
		if err != nil {
			t.Fatal(err)
		}
		if err := sctx.DB.CompleteStepWithStatusAtHead(step.ID, types.StepStatusCompleted, baseSHA, 0, 1, ""); err != nil {
			t.Fatal(err)
		}
		step.Status = types.StepStatusCompleted
		step.CertifiedHeadSHA = &baseSHA
		steps = append(steps, step)
	}
	oldAttestation := buildPipelineAttestation(steps, baseSHA)
	host := &recordingPRContentHost{content: scm.PRContent{
		Title: "fix: preserve CI fixes",
		Body:  "## Pipeline\n\n" + noMistakesPRSignature + "\n\n" + oldAttestation,
	}}

	if err := (&CIStep{}).refreshPRAttestation(sctx, host, &scm.PR{Number: "42"}); err != nil {
		t.Fatal(err)
	}
	if len(host.updates) != 1 {
		t.Fatalf("PR updates = %d, want 1", len(host.updates))
	}
	attestation := parsePipelineAttestationForTest(t, host.updates[0].Body)
	if attestation.HeadSHA != headSHA {
		t.Fatalf("attestation head = %q, want %q", attestation.HeadSHA, headSHA)
	}
	for _, step := range attestation.Steps {
		if step.Step == types.StepReview || step.Step == types.StepTest || step.Step == types.StepDocument {
			if step.HeadSHA != baseSHA {
				t.Fatalf("step %s certified head = %q, want prior head %q", step.Step, step.HeadSHA, baseSHA)
			}
		}
	}
}

func TestCIStep_AutoFixWithoutPushDoesNotRefreshPRAttestation(t *testing.T) {
	dir, baseSHA, headSHA := setupGitRepo(t)
	sctx := newTestContextWithDBRecords(t, &mockAgent{name: "test"}, dir, baseSHA, headSHA, config.Commands{})
	host := &recordingPRContentHost{getErr: errors.New("PR content unavailable")}

	result, err := (&CIStep{}).autoFixCI(sctx, host, &scm.PR{Number: "42"}, []string{"build"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.HeadChanged() {
		t.Fatal("no-change CI fix reported a push")
	}
	if host.getCalls != 0 || len(host.updates) != 0 {
		t.Fatalf("no-change CI fix touched PR content: reads=%d updates=%d", host.getCalls, len(host.updates))
	}
}

func TestCIStep_AutoFixRefreshesAttestationAfterAdoptingRemoteHead(t *testing.T) {
	upstream := t.TempDir()
	gitCmd(t, upstream, "init", "--bare")
	dir, baseSHA, headSHA := setupGitRepo(t)
	gitCmd(t, dir, "remote", "add", "origin", upstream)
	gitCmd(t, dir, "push", "origin", "feature")
	if err := os.WriteFile(filepath.Join(dir, "already-published.txt"), []byte("published"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "already published")
	newHeadSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "push", "origin", "feature")

	sctx := newTestContextWithDBRecords(t, &mockAgent{name: "test"}, dir, baseSHA, headSHA, config.Commands{})
	sctx.Repo.UpstreamURL = upstream
	sctx.Run.Branch = "refs/heads/feature"
	var completed []*db.StepResult
	for _, name := range []types.StepName{types.StepReview, types.StepTest, types.StepDocument} {
		step, err := sctx.DB.InsertStepResult(sctx.Run.ID, name)
		if err != nil {
			t.Fatal(err)
		}
		if err := sctx.DB.CompleteStepWithStatusAtHead(step.ID, types.StepStatusCompleted, headSHA, 0, 1, ""); err != nil {
			t.Fatal(err)
		}
		step.Status = types.StepStatusCompleted
		step.CertifiedHeadSHA = &headSHA
		completed = append(completed, step)
	}
	host := &recordingPRContentHost{content: scm.PRContent{
		Title: "fix: adopt published head",
		Body:  "## Pipeline\n\n" + noMistakesPRSignature + "\n\n" + buildPipelineAttestation(completed, headSHA),
	}}

	result, err := (&CIStep{}).autoFixCI(sctx, host, &scm.PR{Number: "42"}, []string{"build"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.HeadChanged() {
		t.Fatalf("adopted-head result = %#v", result)
	}
	if result.HeadSHA != newHeadSHA || sctx.Run.HeadSHA != newHeadSHA {
		t.Fatalf("adopted head = %q / %q, want %q", result.HeadSHA, sctx.Run.HeadSHA, newHeadSHA)
	}
	if host.getCalls != 1 || len(host.updates) != 1 {
		t.Fatalf("attestation refresh calls: reads=%d updates=%d", host.getCalls, len(host.updates))
	}
	attestation := parsePipelineAttestationForTest(t, host.updates[0].Body)
	if attestation.HeadSHA != newHeadSHA {
		t.Fatalf("attestation head = %q, want %q", attestation.HeadSHA, newHeadSHA)
	}
	for _, step := range attestation.Steps {
		if step.HeadSHA != headSHA {
			t.Fatalf("step %s certified head = %q, want %q", step.Step, step.HeadSHA, headSHA)
		}
	}
}

func TestCIStep_AutoFixPushFailsClosedWhenAttestationRefreshFails(t *testing.T) {
	upstream := t.TempDir()
	gitCmd(t, upstream, "init", "--bare")
	dir, baseSHA, headSHA := setupGitRepo(t)
	gitCmd(t, dir, "remote", "add", "origin", upstream)
	gitCmd(t, dir, "push", "origin", "feature")
	agent := &mockAgent{name: "test", runFn: func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
		if err := os.WriteFile(filepath.Join(opts.CWD, "ci-fix.txt"), []byte("fixed"), 0o644); err != nil {
			t.Fatal(err)
		}
		return &agent.Result{}, nil
	}}
	sctx := newTestContextWithDBRecords(t, agent, dir, baseSHA, headSHA, config.Commands{})
	sctx.Repo.UpstreamURL = upstream
	sctx.Run.Branch = "refs/heads/feature"
	host := &recordingPRContentHost{getErr: errors.New("PR content unavailable")}

	result, err := (&CIStep{}).autoFixCI(sctx, host, &scm.PR{Number: "42"}, []string{"build"}, false)
	if err == nil || !strings.Contains(err.Error(), "refresh PR pipeline attestation") {
		t.Fatalf("autoFixCI error = %v", err)
	}
	if !result.HeadChanged() {
		t.Fatalf("failed refresh lost the published head change: %#v", result)
	}
	if host.getCalls != 1 || len(host.updates) != 0 {
		t.Fatalf("attestation refresh calls: reads=%d updates=%d", host.getCalls, len(host.updates))
	}
	if got := gitCmd(t, upstream, "rev-parse", "refs/heads/feature"); got == headSHA {
		t.Fatal("CI fix did not reach remote before refresh failure")
	}
}

func TestCIStep_AutoFixPreservesPublishedHeadWhenRefAdoptionFails(t *testing.T) {
	upstream := t.TempDir()
	gitCmd(t, upstream, "init", "--bare")
	dir, baseSHA, headSHA := setupGitRepo(t)
	gitCmd(t, dir, "remote", "add", "origin", upstream)
	gitCmd(t, dir, "push", "origin", "feature")
	gitCmd(t, dir, "checkout", "--detach", headSHA)
	tree := gitCmd(t, dir, "rev-parse", headSHA+"^{tree}")
	unrelated := gitCmd(t, dir, "commit-tree", tree, "-m", "unrelated branch head")
	gitCmd(t, dir, "update-ref", "refs/heads/feature", unrelated)
	agent := &mockAgent{name: "test", runFn: func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
		if err := os.WriteFile(filepath.Join(opts.CWD, "ci-fix.txt"), []byte("fixed"), 0o644); err != nil {
			t.Fatal(err)
		}
		return &agent.Result{}, nil
	}}
	sctx := newTestContextWithDBRecords(t, agent, dir, baseSHA, headSHA, config.Commands{})
	sctx.Repo.UpstreamURL = upstream
	sctx.Run.Branch = "refs/heads/feature"
	host := &recordingPRContentHost{}

	result, err := (&CIStep{}).autoFixCI(sctx, host, &scm.PR{Number: "42"}, []string{"build"}, false)
	if err == nil || !strings.Contains(err.Error(), "refusing to move branch ref") {
		t.Fatalf("autoFixCI error = %v", err)
	}
	if !result.HeadChanged() || !result.HeadPersisted {
		t.Fatalf("published head result = %#v", result)
	}
	remoteHead := gitCmd(t, upstream, "rev-parse", "refs/heads/feature")
	if result.HeadSHA != remoteHead || remoteHead == headSHA {
		t.Fatalf("published head = result %q remote %q old %q", result.HeadSHA, remoteHead, headSHA)
	}
	persisted, getErr := sctx.DB.GetRun(sctx.Run.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if persisted.HeadSHA != remoteHead {
		t.Fatalf("persisted head = %q, want %q", persisted.HeadSHA, remoteHead)
	}
}

func TestCIStep_AutoFixPreservesPublishedHeadWhenVerificationFails(t *testing.T) {
	upstream := t.TempDir()
	gitCmd(t, upstream, "init", "--bare")
	dir, baseSHA, headSHA := setupGitRepo(t)
	gitCmd(t, dir, "remote", "add", "origin", upstream)
	gitCmd(t, dir, "push", "origin", "feature")

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := fakeCLIBinDir(t)
	linkTestBinary(t, binDir, "git")
	marker := filepath.Join(t.TempDir(), "pushed")
	env := fakeCLIEnv(binDir, map[string]string{
		"FAKE_CLI_MODE":        "git-fail-verify-after-push",
		"FAKE_CLI_REAL_GIT":    realGit,
		"FAKE_CLI_PUSH_MARKER": marker,
	})
	agent := &mockAgent{name: "test", runFn: func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
		if err := os.WriteFile(filepath.Join(opts.CWD, "ci-fix.txt"), []byte("fixed"), 0o644); err != nil {
			t.Fatal(err)
		}
		return &agent.Result{}, nil
	}}
	sctx := newTestContextWithDBRecords(t, agent, dir, baseSHA, headSHA, config.Commands{})
	sctx.Env = env
	sctx.Repo.UpstreamURL = upstream
	sctx.Run.Branch = "refs/heads/feature"
	host := &recordingPRContentHost{}

	result, err := (&CIStep{}).autoFixCI(sctx, host, &scm.PR{Number: "42"}, []string{"build"}, false)
	if err == nil || !strings.Contains(err.Error(), "verify successful push") {
		t.Fatalf("autoFixCI error = %v", err)
	}
	if !result.HeadChanged() || !result.HeadPersisted || !result.ExpectedAttestationTracked {
		t.Fatalf("published head result = %#v", result)
	}
	remoteHead := gitCmd(t, upstream, "rev-parse", "refs/heads/feature")
	if result.HeadSHA != remoteHead || remoteHead == headSHA {
		t.Fatalf("published head = result %q remote %q old %q", result.HeadSHA, remoteHead, headSHA)
	}
	persisted, getErr := sctx.DB.GetRun(sctx.Run.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if persisted.HeadSHA != remoteHead {
		t.Fatalf("persisted head = %q, want %q", persisted.HeadSHA, remoteHead)
	}
	encoded, getErr := sctx.DB.GetRunCIRerunState(sctx.Run.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	var tracking checkRerunBudget
	if err := tracking.unmarshal(encoded); err != nil {
		t.Fatal(err)
	}
	if tracking.expectedAttestationHeadSHA != remoteHead {
		t.Fatalf("tracked attestation head = %q, want %q", tracking.expectedAttestationHeadSHA, remoteHead)
	}
}

func TestCIStep_AutoFixPreservesCandidateWhenPushErrorsAfterRemoteUpdate(t *testing.T) {
	upstream := t.TempDir()
	gitCmd(t, upstream, "init", "--bare")
	dir, baseSHA, headSHA := setupGitRepo(t)
	gitCmd(t, dir, "remote", "add", "origin", upstream)
	gitCmd(t, dir, "push", "origin", "feature")

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := fakeCLIBinDir(t)
	linkTestBinary(t, binDir, "git")
	marker := filepath.Join(t.TempDir(), "pushed")
	env := fakeCLIEnv(binDir, map[string]string{
		"FAKE_CLI_MODE":        "git-fail-after-push",
		"FAKE_CLI_REAL_GIT":    realGit,
		"FAKE_CLI_PUSH_MARKER": marker,
	})
	agent := &mockAgent{name: "test", runFn: func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
		if err := os.WriteFile(filepath.Join(opts.CWD, "ci-fix.txt"), []byte("fixed"), 0o644); err != nil {
			t.Fatal(err)
		}
		return &agent.Result{}, nil
	}}
	sctx := newTestContextWithDBRecords(t, agent, dir, baseSHA, headSHA, config.Commands{})
	sctx.Env = env
	sctx.Repo.UpstreamURL = upstream
	sctx.Run.Branch = "refs/heads/feature"
	host := &recordingPRContentHost{}

	result, err := (&CIStep{}).autoFixCI(sctx, host, &scm.PR{Number: "42"}, []string{"build"}, false)
	if err == nil || !strings.Contains(err.Error(), "push reconciliation unavailable") {
		t.Fatalf("autoFixCI error = %v", err)
	}
	if !result.HeadChanged() || !result.HeadPersisted || !result.ExpectedAttestationTracked {
		t.Fatalf("uncertain published head result = %#v", result)
	}
	remoteHead := gitCmd(t, upstream, "rev-parse", "refs/heads/feature")
	if result.HeadSHA != remoteHead || remoteHead == headSHA {
		t.Fatalf("preserved candidate = result %q remote %q old %q", result.HeadSHA, remoteHead, headSHA)
	}
	persisted, getErr := sctx.DB.GetRun(sctx.Run.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if persisted.HeadSHA != remoteHead {
		t.Fatalf("persisted head = %q, want %q", persisted.HeadSHA, remoteHead)
	}
	encoded, getErr := sctx.DB.GetRunCIRerunState(sctx.Run.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	var tracking checkRerunBudget
	if err := tracking.unmarshal(encoded); err != nil {
		t.Fatal(err)
	}
	if tracking.expectedAttestationHeadSHA != remoteHead {
		t.Fatalf("tracked attestation head = %q, want %q", tracking.expectedAttestationHeadSHA, remoteHead)
	}
}

func TestCIStep_CommitAndPush(t *testing.T) {
	t.Parallel()
	// Set up upstream bare repo
	upstream := t.TempDir()
	gitCmd(t, upstream, "init", "--bare")

	// Create working repo
	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@test.com")
	gitCmd(t, dir, "checkout", "-b", "main")
	os.WriteFile(filepath.Join(dir, "init.txt"), []byte("init"), 0o644)
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "initial")
	baseSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "remote", "add", "origin", upstream)
	gitCmd(t, dir, "push", "origin", "main")

	gitCmd(t, dir, "checkout", "-b", "feature")
	os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature"), 0o644)
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "feature")
	headSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "push", "origin", "feature")

	// Add uncommitted changes
	os.WriteFile(filepath.Join(dir, "fix.txt"), []byte("ci fix"), 0o644)

	ag := &mockAgent{name: "test"}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Repo.UpstreamURL = upstream
	sctx.Run.Branch = "refs/heads/feature"
	if err := sctx.DB.UpdateRunPushBinding(sctx.Run.ID, db.PushBinding{HeadSHA: headSHA, TargetKind: "upstream", TargetFingerprint: branchsync.TargetFingerprint(upstream), Ref: "refs/heads/feature"}); err != nil {
		t.Fatal(err)
	}

	step := &CIStep{}
	changed, err := step.commitAndPush(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("expected commitAndPush to report a local repair commit")
	}

	localSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	upstreamSHA := gitCmd(t, upstream, "rev-parse", "refs/heads/feature")
	if upstreamSHA != headSHA {
		t.Fatalf("upstream head = %s, want unchanged head %s", upstreamSHA, headSHA)
	}
	if localSHA == headSHA {
		t.Fatal("local head should contain the CI repair")
	}
	dbRun, err := sctx.DB.GetRun(sctx.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dbRun.HeadSHA != localSHA || dbRun.LastPushedSHA == nil || *dbRun.LastPushedSHA != upstreamSHA || dbRun.PushGeneration == nil || *dbRun.PushGeneration != 1 {
		t.Fatalf("local CI repair state = %#v", dbRun)
	}
}

func TestCIStep_CommitAndPushTargetsForkWhenConfigured(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	fork := t.TempDir()
	gitCmd(t, parent, "init", "--bare")
	gitCmd(t, fork, "init", "--bare")

	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@test.com")
	gitCmd(t, dir, "checkout", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "init.txt"), []byte("init"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "initial")
	baseSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "remote", "add", "origin", parent)
	gitCmd(t, dir, "push", "origin", "main")
	gitCmd(t, dir, "push", fork, "main")

	gitCmd(t, dir, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "feature")
	headSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "ci-fix.txt"), []byte("fixed"), 0o644); err != nil {
		t.Fatal(err)
	}

	ag := &mockAgent{name: "test"}
	sctx := newTestContext(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Repo.UpstreamURL = parent
	sctx.Repo.ForkURL = fork
	sctx.Run.Branch = "refs/heads/feature"

	step := &CIStep{}
	changed, err := step.commitAndPush(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected commitAndPush to report a local repair commit")
	}

	if out, err := exec.Command("git", "-C", fork, "rev-parse", "--verify", "refs/heads/feature").CombinedOutput(); err == nil {
		t.Fatalf("fork unexpectedly received feature branch at %s", strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("git", "-C", parent, "rev-parse", "--verify", "refs/heads/feature").CombinedOutput(); err == nil {
		t.Fatalf("parent unexpectedly received feature branch at %s", strings.TrimSpace(string(out)))
	}
}

func TestCIStep_CommitAndPushDoesNotConsultForkRemote(t *testing.T) {
	dir, baseSHA, headSHA := setupGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "ci-fix.txt"), []byte("fixed"), 0o644); err != nil {
		t.Fatal(err)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := fakeCLIBinDir(t)
	linkTestBinary(t, binDir, "git")
	env := fakeCLIEnv(binDir, map[string]string{
		"FAKE_CLI_MODE":     "git-remote-error",
		"FAKE_CLI_REAL_GIT": realGit,
	})

	ag := &mockAgent{name: "test"}
	sctx := newTestContext(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Env = env
	sctx.Repo.UpstreamURL = "https://github.com/parent/project.git"
	sctx.Repo.ForkURL = "https://user:secret@example.com/fork/project.git"
	sctx.Run.Branch = "refs/heads/feature"

	step := &CIStep{}
	changed, err := step.commitAndPush(sctx)
	if err != nil {
		t.Fatalf("local CI repair unexpectedly consulted remote: %v", err)
	}
	if !changed {
		t.Fatal("expected a local CI repair commit")
	}
}

func TestCIStep_CommitAndPush_NoChanges(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)

	ag := &mockAgent{name: "test"}
	sctx := newTestContext(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Repo.UpstreamURL = "dummy"
	sctx.Run.Branch = "refs/heads/feature"

	step := &CIStep{}
	changed, err := step.commitAndPush(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("expected commitAndPush to report no changes pushed")
	}
}

func TestCIStep_InvalidCommitTemplateDoesNotStageRepair(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	sctx := newTestContext(t, &mockAgent{name: "test"}, dir, baseSHA, headSHA, config.Commands{})
	sctx.Config.Commit = config.Commit{FixMessage: `{{printf "%s" .Summary}}`}
	if err := os.WriteFile(filepath.Join(dir, "ci-fix.txt"), []byte("fixed"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := (&CIStep{}).commitRepair(sctx, "repair checks"); err == nil {
		t.Fatal("commitRepair accepted an invalid commit.fix_message")
	}
	if got := gitCmd(t, dir, "diff", "--cached", "--name-only"); got != "" {
		t.Fatalf("staged files after template error = %q, want none", got)
	}
}

func TestCIStep_CommitAndPush_StatusError(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}

	binDir := fakeCLIBinDir(t)
	linkTestBinary(t, binDir, "git")
	env := fakeCLIEnv(binDir, map[string]string{
		"FAKE_CLI_MODE":     "git-status-error",
		"FAKE_CLI_REAL_GIT": realGit,
	})

	ag := &mockAgent{name: "test"}
	sctx := newTestContext(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Env = env
	sctx.Repo.UpstreamURL = "dummy"
	sctx.Run.Branch = "refs/heads/feature"

	step := &CIStep{}
	changed, err := step.commitAndPush(sctx)
	if err == nil {
		t.Fatal("expected status error")
	}
	if changed {
		t.Error("expected commitAndPush to report no push on status error")
	}
	if !strings.Contains(err.Error(), "git status --porcelain") {
		t.Fatalf("expected status command in error, got %v", err)
	}
	if !strings.Contains(err.Error(), "status failed") {
		t.Fatalf("expected status stderr in error, got %v", err)
	}
}

func TestCIStep_CommitAndPush_UsesStepEnvForAllGitCommands(t *testing.T) {
	upstream := t.TempDir()
	gitCmd(t, upstream, "init", "--bare")

	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@test.com")
	gitCmd(t, dir, "checkout", "-b", "main")
	os.WriteFile(filepath.Join(dir, "init.txt"), []byte("init"), 0o644)
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "initial")
	baseSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "remote", "add", "origin", upstream)
	gitCmd(t, dir, "push", "origin", "main")

	gitCmd(t, dir, "checkout", "-b", "feature")
	os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature"), 0o644)
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "feature")
	headSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "push", "origin", "feature")
	os.WriteFile(filepath.Join(dir, "fix.txt"), []byte("ci fix"), 0o644)

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := fakeCLIBinDir(t)
	linkTestBinary(t, binDir, "git")
	env := fakeCLIEnv(binDir, map[string]string{
		"FAKE_CLI_MODE":     "git-passthrough",
		"FAKE_CLI_REAL_GIT": realGit,
	})
	t.Setenv("PATH", t.TempDir())
	realGitCmd := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command(realGit, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	ag := &mockAgent{name: "test"}
	sctx := newTestContext(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Env = env
	sctx.Repo.UpstreamURL = upstream
	sctx.Run.Branch = "refs/heads/feature"

	step := &CIStep{}
	changed, err := step.commitAndPush(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected commitAndPush to report a local repair commit")
	}

	localSHA := realGitCmd(dir, "rev-parse", "HEAD")
	upstreamSHA := realGitCmd(upstream, "rev-parse", "refs/heads/feature")
	if upstreamSHA != headSHA {
		t.Fatal("expected upstream to remain on the pre-repair head")
	}
	if sctx.Run.HeadSHA != localSHA {
		t.Fatalf("Run.HeadSHA = %s, want %s", sctx.Run.HeadSHA, localSHA)
	}
}

func TestCIStep_CommitAndPush_GitCommandsUseStandardCredentialEnv(t *testing.T) {
	upstream := t.TempDir()
	gitCmd(t, upstream, "init", "--bare")

	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@test.com")
	gitCmd(t, dir, "checkout", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "init.txt"), []byte("init"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "initial")
	baseSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "remote", "add", "origin", upstream)
	gitCmd(t, dir, "push", "origin", "main")

	gitCmd(t, dir, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "feature")
	headSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "push", "origin", "feature")
	if err := os.WriteFile(filepath.Join(dir, "fix.txt"), []byte("ci fix"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GIT_EDITOR", "vim")
	t.Setenv("GIT_SEQUENCE_EDITOR", "vim")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[credential \"https://github.com\"]\n\thelper = !gh auth git-credential\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := fakeCLIBinDir(t)
	linkTestBinary(t, binDir, "git")
	env := fakeCLIEnv(binDir, map[string]string{
		"FAKE_CLI_MODE":     "git-require-noninteractive-env",
		"FAKE_CLI_REAL_GIT": realGit,
	})
	t.Setenv("PATH", t.TempDir())

	ag := &mockAgent{name: "test"}
	sctx := newTestContext(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Env = env
	sctx.Repo.UpstreamURL = upstream
	sctx.Run.Branch = "refs/heads/feature"

	step := &CIStep{}
	changed, err := step.commitAndPush(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected commitAndPush to report changes were pushed")
	}
}

func TestCIStep_CommitAndPush_NoChanges_ReconcilesStaleDatabaseHeadSHA(t *testing.T) {
	t.Parallel()
	upstream := t.TempDir()
	gitCmd(t, upstream, "init", "--bare")

	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@test.com")
	gitCmd(t, dir, "checkout", "-b", "main")
	os.WriteFile(filepath.Join(dir, "init.txt"), []byte("init"), 0o644)
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "initial")
	baseSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "remote", "add", "origin", upstream)
	gitCmd(t, dir, "push", "origin", "main")

	gitCmd(t, dir, "checkout", "-b", "feature")
	os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature"), 0o644)
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "feature")
	actualHeadSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "push", "origin", "feature")

	// Create context with stale HeadSHA (simulates prior DB write failure)
	staleHeadSHA := baseSHA
	ag := &mockAgent{name: "test"}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, staleHeadSHA, config.Commands{})
	sctx.Repo.UpstreamURL = upstream
	sctx.Run.Branch = "refs/heads/feature"

	step := &CIStep{}
	changed, err := step.commitAndPush(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("expected commitAndPush to report the reconciled local head")
	}

	if sctx.Run.HeadSHA != actualHeadSHA {
		t.Errorf("Run.HeadSHA = %s, want %s", sctx.Run.HeadSHA, actualHeadSHA)
	}
	dbRun, err := sctx.DB.GetRun(sctx.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dbRun.HeadSHA != actualHeadSHA {
		t.Errorf("DB HeadSHA = %s, want %s", dbRun.HeadSHA, actualHeadSHA)
	}
}

func TestCIStep_CommitAndPush_NoChanges_ReconcilesStaleDatabaseHeadSHA_UsesStepEnv(t *testing.T) {
	upstream := t.TempDir()
	gitCmd(t, upstream, "init", "--bare")

	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@test.com")
	gitCmd(t, dir, "checkout", "-b", "main")
	os.WriteFile(filepath.Join(dir, "init.txt"), []byte("init"), 0o644)
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "initial")
	baseSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "remote", "add", "origin", upstream)
	gitCmd(t, dir, "push", "origin", "main")

	gitCmd(t, dir, "checkout", "-b", "feature")
	os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature"), 0o644)
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "feature")
	actualHeadSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "push", "origin", "feature")

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := fakeCLIBinDir(t)
	linkTestBinary(t, binDir, "git")
	env := fakeCLIEnv(binDir, map[string]string{
		"FAKE_CLI_MODE":     "git-passthrough",
		"FAKE_CLI_REAL_GIT": realGit,
	})
	t.Setenv("PATH", t.TempDir())

	staleHeadSHA := baseSHA
	ag := &mockAgent{name: "test"}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, staleHeadSHA, config.Commands{})
	sctx.Env = env
	sctx.Repo.UpstreamURL = upstream
	sctx.Run.Branch = "refs/heads/feature"

	step := &CIStep{}
	changed, err := step.commitAndPush(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("expected commitAndPush to report the reconciled local head")
	}

	if sctx.Run.HeadSHA != actualHeadSHA {
		t.Errorf("Run.HeadSHA = %s, want %s", sctx.Run.HeadSHA, actualHeadSHA)
	}
	dbRun, err := sctx.DB.GetRun(sctx.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dbRun.HeadSHA != actualHeadSHA {
		t.Errorf("DB HeadSHA = %s, want %s", dbRun.HeadSHA, actualHeadSHA)
	}
}

func TestCIStep_CommitAndPush_NoDirtyChangesButHeadAdvanced_PushesNewHead(t *testing.T) {
	t.Parallel()
	upstream := t.TempDir()
	gitCmd(t, upstream, "init", "--bare")

	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@test.com")
	gitCmd(t, dir, "checkout", "-b", "main")
	os.WriteFile(filepath.Join(dir, "init.txt"), []byte("init"), 0o644)
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "initial")
	baseSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "remote", "add", "origin", upstream)
	gitCmd(t, dir, "push", "origin", "main")

	gitCmd(t, dir, "checkout", "-b", "feature")
	os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature"), 0o644)
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "feature")
	originalHeadSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "push", "origin", "feature")

	os.WriteFile(filepath.Join(dir, "resolved.txt"), []byte("resolved"), 0o644)
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "resolve conflict")
	advancedHeadSHA := gitCmd(t, dir, "rev-parse", "HEAD")

	ag := &mockAgent{name: "test"}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, originalHeadSHA, config.Commands{})
	sctx.Repo.UpstreamURL = upstream
	sctx.Run.Branch = "refs/heads/feature"

	step := &CIStep{}
	pushed, err := step.commitAndPush(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if !pushed {
		t.Fatal("expected commitAndPush to push advanced clean head")
	}

	upstreamSHA := gitCmd(t, upstream, "rev-parse", "refs/heads/feature")
	if upstreamSHA != originalHeadSHA {
		t.Fatalf("upstream SHA = %s, want unchanged head %s", upstreamSHA, originalHeadSHA)
	}
	if sctx.Run.HeadSHA != advancedHeadSHA {
		t.Fatalf("Run.HeadSHA = %s, want %s", sctx.Run.HeadSHA, advancedHeadSHA)
	}
	dbRun, err := sctx.DB.GetRun(sctx.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dbRun.HeadSHA != advancedHeadSHA {
		t.Fatalf("DB HeadSHA = %s, want %s", dbRun.HeadSHA, advancedHeadSHA)
	}
}

func TestCIStep_CommitAndPush_UpdatesLocalBranchRefAfterDetachedPush(t *testing.T) {
	t.Parallel()
	upstream := t.TempDir()
	gitCmd(t, upstream, "init", "--bare")

	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@test.com")
	gitCmd(t, dir, "checkout", "-b", "main")
	os.WriteFile(filepath.Join(dir, "init.txt"), []byte("init"), 0o644)
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "initial")
	baseSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "remote", "add", "origin", upstream)
	gitCmd(t, dir, "push", "origin", "main")

	gitCmd(t, dir, "checkout", "-b", "feature")
	os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature"), 0o644)
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "feature")
	originalHeadSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "push", "origin", "feature")
	gitCmd(t, dir, "checkout", "--detach", originalHeadSHA)
	os.WriteFile(filepath.Join(dir, "fix.txt"), []byte("ci fix"), 0o644)

	ag := &mockAgent{name: "test"}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, originalHeadSHA, config.Commands{})
	sctx.Repo.UpstreamURL = upstream
	sctx.Run.Branch = "refs/heads/feature"

	step := &CIStep{}
	pushed, err := step.commitAndPush(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if !pushed {
		t.Error("expected commitAndPush to report changes were pushed")
	}
	newHeadSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	branchSHA := gitCmd(t, dir, "rev-parse", "refs/heads/feature")
	if branchSHA != newHeadSHA {
		t.Fatalf("branch ref SHA = %s, want %s", branchSHA, newHeadSHA)
	}
	upstreamSHA := gitCmd(t, upstream, "rev-parse", "refs/heads/feature")
	if upstreamSHA != originalHeadSHA {
		t.Fatalf("upstream SHA = %s, want unchanged head %s", upstreamSHA, originalHeadSHA)
	}
}
