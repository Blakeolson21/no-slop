package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/pipeline"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// The tests in this file pin the trusted-config boundary repairs measured on
// live gate runs 2026-09-08 (brief s12-mo3852, gate stores whose
// remote.origin.url pointed at a FOREIGN repository):
//
//   - A trusted default-branch snapshot that shares no git history with the
//     pushed head must REFUSE the run (it is another repo's config, and its
//     commands - including a dead `sh scripts/nm-gate-test.sh` - must never
//     execute on the daemon host).
//   - A code branch whose effective trusted config resolves NO test command
//     must refuse with a named reason instead of reaching a test step that
//     can pass without a suite (the silent skip that landed run 4373
//     review-approved but suite-unverified).
//
// Both tests FAIL against the pre-fix build: the old startRun proceeded past
// the trusted-config reads and only died later (or not at all), so the
// assertions below on the refusal messages are the cutover verification.

const gateIdentityZeroSHA = "0000000000000000000000000000000000000000"

// makeForeignGateStore creates a bare repo with an UNRELATED history whose
// main branch carries the measured severity-1 trusted snapshot shape: a
// .no-slop.yaml whose commands.test names the dead pre-rename script.
func makeForeignGateStore(t *testing.T) string {
	t.Helper()
	foreignWork := filepath.Join(t.TempDir(), "foreign-work")
	if err := os.MkdirAll(foreignWork, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, foreignWork, "init")
	gitCmd(t, foreignWork, "config", "user.email", "foreign@test.com")
	gitCmd(t, foreignWork, "config", "user.name", "Foreign")
	if err := os.WriteFile(filepath.Join(foreignWork, "foreign.txt"), []byte("foreign\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreignWork, ".no-slop.yaml"), []byte("commands:\n  test: \"sh scripts/nm-gate-test.sh\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, foreignWork, "add", ".")
	gitCmd(t, foreignWork, "commit", "-m", "foreign snapshot with dead test command")
	foreignBare := filepath.Join(t.TempDir(), "foreign.git")
	gitCmd(t, "", "init", "--bare", foreignBare)
	gitCmd(t, foreignWork, "remote", "add", "origin", foreignBare)
	gitCmd(t, foreignWork, "push", "origin", "HEAD:refs/heads/main")
	return foreignBare
}

func runErrorForRepo(t *testing.T, database *db.DB, repoID string) string {
	t.Helper()
	runs, err := database.GetRunsByRepo(repoID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 {
		t.Fatalf("no run rows persisted for repo %s", repoID)
	}
	for _, r := range runs {
		if r.Error != nil {
			return *r.Error
		}
	}
	return ""
}

func TestStartRun_RefusesForeignTrustedSnapshot(t *testing.T) {
	ctx := context.Background()
	p, database := newRefreshRunFixture(t)
	foreignBare := makeForeignGateStore(t)

	repo, head := setupTestGitRepo(t, p, database, "identity-foreign")
	// Mis-register the gate store's origin exactly like the measured defect:
	// the store holds MO-style heads but its origin points at a foreign repo.
	gitCmd(t, p.RepoDir(repo.ID), "remote", "set-url", "origin", foreignBare)

	manager := NewRunManager(database, p, nil)
	t.Cleanup(manager.Shutdown)

	_, err := manager.startRun(ctx, repo, "main", head, gateIdentityZeroSHA, "test", nil, "")
	if err == nil {
		t.Fatalf("startRun must refuse a foreign trusted snapshot; persisted error = %q", runErrorForRepo(t, database, repo.ID))
	}
	if !strings.Contains(err.Error(), "shares no history") {
		t.Fatalf("refusal must name the ancestry mismatch, got: %v", err)
	}
	if !strings.Contains(err.Error(), head) {
		t.Fatalf("refusal must name the pushed head %s, got: %v", head, err)
	}
	foreignMain := gitOutput(t, foreignBare, "rev-parse", "refs/heads/main")
	if !strings.Contains(err.Error(), foreignMain) {
		t.Fatalf("refusal must name the trusted commit %s, got: %v", foreignMain, err)
	}
	if !strings.Contains(err.Error(), foreignBare) {
		t.Fatalf("refusal must name the registered origin, got: %v", err)
	}
	// The refusal must be persisted on the run record, not just returned.
	if persisted := runErrorForRepo(t, database, repo.ID); !strings.Contains(persisted, "shares no history") {
		t.Fatalf("persisted run error must carry the refusal, got: %q", persisted)
	}
}

func TestStartRun_RefusesCodeBranchWithoutTrustedTestCommand(t *testing.T) {
	ctx := context.Background()
	p, database := newRefreshRunFixture(t)

	repo, base := setupTestGitRepo(t, p, database, "identity-no-test-cmd")
	// A code change (Go source, not docs) pushed on top of the first commit,
	// so the branch diff genuinely touches non-documentation files.
	work := repo.WorkingPath
	if err := os.WriteFile(filepath.Join(work, "app.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, work, "add", ".")
	gitCmd(t, work, "commit", "-m", "code change")
	head := gitOutput(t, work, "rev-parse", "HEAD")
	gitCmd(t, work, "push", "gate", "HEAD:refs/heads/main")

	manager := NewRunManager(database, p, nil)
	t.Cleanup(manager.Shutdown)

	_, err := manager.startRun(ctx, repo, "main", head, base, "test", nil, "")
	if err == nil {
		t.Fatalf("startRun must refuse a code branch with no trusted test command; persisted error = %q", runErrorForRepo(t, database, repo.ID))
	}
	if !strings.Contains(err.Error(), "no trusted test command resolvable - refusing agent-graded tests") {
		t.Fatalf("refusal must name the reason, got: %v", err)
	}
	if persisted := runErrorForRepo(t, database, repo.ID); !strings.Contains(persisted, "refusing agent-graded tests") {
		t.Fatalf("persisted run error must carry the refusal, got: %q", persisted)
	}
}

// TestStartRun_DocsOnlyBranchWithoutTestCommandStillRuns proves the
// no-test-command refusal is scoped to CODE branches: a docs-only delta keeps
// running, and an explicit --skip test is honored on a code delta.
func TestStartRun_DocsOnlyBranchWithoutTestCommandStillRuns(t *testing.T) {
	ctx := context.Background()
	p, _ := newRefreshRunFixture(t)

	// Give the manager a resolvable agent (mock claude) and mock steps so the
	// run can complete end-to-end without demo mode - the guard must be
	// genuinely exercised, not skipped.
	mockClaude := writeMockClaude(t, t.TempDir())
	configYAML := "agent: claude\nagent_path_override:\n  claude: " + mockClaude + "\n"
	if err := os.WriteFile(p.ConfigFile(), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name      string
		docsOnly  bool
		skipSteps []types.StepName
	}{
		{name: "docs-only delta runs without a test command", docsOnly: true},
		{name: "explicit skip test is honored on a code delta", skipSteps: []types.StepName{types.StepTest}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, database := newRefreshRunFixture(t)
			if err := os.WriteFile(p.ConfigFile(), []byte(configYAML), 0o644); err != nil {
				t.Fatal(err)
			}
			repo, base := setupTestGitRepo(t, p, database, "identity-docs-only")
			work := repo.WorkingPath
			if tc.docsOnly {
				if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# docs\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(filepath.Join(work, "app.go"), []byte("package main\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			gitCmd(t, work, "add", ".")
			gitCmd(t, work, "commit", "-m", "delta")
			head := gitOutput(t, work, "rev-parse", "HEAD")
			gitCmd(t, work, "push", "gate", "HEAD:refs/heads/main")

			step := &mockPassStep{name: types.StepReview}
			manager := NewRunManager(database, p, func() []pipeline.Step {
				return []pipeline.Step{step}
			})
			t.Cleanup(manager.Shutdown)

			runID, err := manager.startRun(ctx, repo, "main", head, base, "test", tc.skipSteps, "")
			if err != nil {
				t.Fatalf("run must not be refused (%s): %v; persisted error = %q", tc.name, err, runErrorForRepo(t, database, repo.ID))
			}
			run := waitForRunTerminalState(t, database, runID)
			if run.Status != types.RunCompleted {
				t.Fatalf("run status = %s, error = %v", run.Status, run.Error)
			}
		})
	}
}

func TestLoadRecoveredConfig_RefusesForeignTrustedSnapshot(t *testing.T) {
	foreignBare := makeForeignGateStore(t)

	// A worktree whose own history is unrelated to the foreign snapshot the
	// registered origin serves, exactly like a recovered run on a
	// mis-registered gate store.
	workDir := filepath.Join(t.TempDir(), "recovery-work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, workDir, "init")
	gitCmd(t, workDir, "config", "user.email", "test@test.com")
	gitCmd(t, workDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(workDir, ".no-slop.yaml"), []byte("auto_fix:\n  lint: 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, workDir, "add", ".")
	gitCmd(t, workDir, "commit", "-m", "own history")
	head := gitOutput(t, workDir, "rev-parse", "HEAD")
	gitCmd(t, workDir, "remote", "add", "origin", foreignBare)

	mgrP, _ := newRefreshRunFixture(t)
	mgr := NewRunManager(nil, mgrP, nil)
	_, err := mgr.loadRecoveredConfig(context.Background(),
		&db.Run{ID: "run", HeadSHA: head},
		&db.Repo{DefaultBranch: "main", UpstreamURL: foreignBare},
		workDir)
	if err == nil {
		t.Fatal("loadRecoveredConfig must refuse a foreign trusted snapshot")
	}
	if !strings.Contains(err.Error(), "shares no history") || !strings.Contains(err.Error(), head) {
		t.Fatalf("recovery refusal must name the mismatch and both commits, got: %v", err)
	}
}

func TestLoadRecoveredConfig_AllowsSharedHistoryTrustedSnapshot(t *testing.T) {
	// The healthy case: trusted default branch and head share history (the
	// worktree's own repo), so recovery proceeds past the identity boundary.
	workDir := filepath.Join(t.TempDir(), "recovery-work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, workDir, "init")
	gitCmd(t, workDir, "config", "user.email", "test@test.com")
	gitCmd(t, workDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(workDir, ".no-slop.yaml"), []byte("commands:\n  lint: echo trusted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, workDir, "add", ".")
	gitCmd(t, workDir, "commit", "-m", "own history")
	head := gitOutput(t, workDir, "rev-parse", "HEAD")
	bare := filepath.Join(t.TempDir(), "own.git")
	gitCmd(t, "", "init", "--bare", bare)
	gitCmd(t, workDir, "remote", "add", "origin", bare)
	gitCmd(t, workDir, "push", "origin", "HEAD:refs/heads/main")

	mgrP, _ := newRefreshRunFixture(t)
	mgr := NewRunManager(nil, mgrP, nil)
	cfg, err := mgr.loadRecoveredConfig(context.Background(),
		&db.Run{ID: "run", HeadSHA: head},
		&db.Repo{DefaultBranch: "main", UpstreamURL: bare},
		workDir)
	if err != nil {
		t.Fatalf("shared-history trusted snapshot must not be refused: %v", err)
	}
	if cfg == nil || cfg.Commands.Lint != "echo trusted" {
		t.Fatalf("recovered config must keep trusted commands, got: %+v", cfg)
	}
}

func TestBranchTouchesCodeClassification(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.email", "test@test.com")
	gitCmd(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "commit", "-m", "base")
	base := gitOutput(t, dir, "rev-parse", "HEAD")

	if branchTouchesCode(ctx, dir, base, base) {
		t.Fatal("empty diff (identical SHAs) must not classify as code")
	}

	// Docs-only head.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# docs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "commit", "-m", "docs")
	docsHead := gitOutput(t, dir, "rev-parse", "HEAD")
	if branchTouchesCode(ctx, dir, base, docsHead) {
		t.Fatal("docs-only delta must not classify as code")
	}

	// Code head.
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "commit", "-m", "code")
	codeHead := gitOutput(t, dir, "rev-parse", "HEAD")
	if !branchTouchesCode(ctx, dir, base, codeHead) {
		t.Fatal("code delta must classify as code")
	}

	// Unresolvable diff fails closed as code.
	if !branchTouchesCode(ctx, dir, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", base) {
		t.Fatal("unresolvable diff must fail closed as code")
	}
}
