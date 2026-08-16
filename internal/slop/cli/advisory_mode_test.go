package cli_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	slopcli "github.com/Blakeolson21/no-slop/internal/slop/cli"
)

// Five review rounds took the base away from the author five times, and each
// time the control moved one input further out: the flag, then the local ref,
// then the remote-tracking ref, then the remote URL in .git/config, then the
// ambient GIT_CONFIG_* environment, which writes nothing to disk at all. The
// theorem the rounds proved is that any base a run resolves from inside a
// repository the author controls is author-writable, because refs, config, the
// environment, and the remote list are all things the author writes.
//
// So the capability is removed instead of the instance. A standalone run can no
// longer certify anything, whatever base it resolved and however it resolved
// it. It still runs every check and still prints every finding, and the line it
// prints in place of a verdict says what it is.
//
// These tests are the reviewer's U1 probes, and every one of them now asks a
// different question: not "did the defeated base reach a passing verdict" but
// "can this run reach a verdict at all".

const advisoryBanner = "advisory: base supplied by this repository; not a certification"

// advisoryRepo is the reviewer's U1 shape. A real clone of a real bare remote
// with origin/main present and correct, and two commits on the author's branch:
// a weakened .no-slop.yaml, then an authorization weakening. It returns the
// worktree, the honest remote path, and the author-owned commit an attack wants
// to install as the base.
func advisoryRepo(t *testing.T) (dir, honest, authorCommit string) {
	t.Helper()
	dir, _ = shadowRepo(t)
	honest = strings.TrimSpace(runGit(t, dir, "remote", "get-url", "origin"))
	authorCommit = strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD~1"))
	return dir, honest, authorCommit
}

// authorOwnedRemote publishes the author's own commit as main on a bare
// repository the author created. Every on-disk U1 route ends here.
func authorOwnedRemote(t *testing.T, dir, commit string) string {
	t.Helper()
	remote := t.TempDir()
	runGit(t, remote, "init", "--bare", "-b", "main")
	runGit(t, dir, "push", "-q", remote, commit+":refs/heads/main")
	return remote
}

// TestNoRepositoryLocalRouteCanReachACertification is U1 P1, P2, and P4. Each
// probe makes an author-owned commit the one the remote answers with, by a
// different local mechanism, and each carried both never-passes classes to
// "verdict: pass" at exit 0 in round 5.
func TestNoRepositoryLocalRouteCanReachACertification(t *testing.T) {
	t.Parallel()

	for _, probe := range []struct {
		name  string
		setup func(t *testing.T, dir, honest, authorCommit string)
	}{
		{"the remote URL is repointed", func(t *testing.T, dir, honest, authorCommit string) {
			runGit(t, dir, "remote", "set-url", "origin", authorOwnedRemote(t, dir, authorCommit))
		}},
		{"an insteadOf rewrite redirects the honest URL", func(t *testing.T, dir, honest, authorCommit string) {
			evil := authorOwnedRemote(t, dir, authorCommit)
			runGit(t, dir, "config", "url."+evil+".insteadOf", honest)
		}},
		{"origin is pointed at the worktree's own repository", func(t *testing.T, dir, honest, authorCommit string) {
			runGit(t, dir, "branch", "-f", "main", authorCommit)
			runGit(t, dir, "remote", "set-url", "origin", dir)
		}},
	} {
		t.Run(probe.name, func(t *testing.T) {
			t.Parallel()

			dir, honest, authorCommit := advisoryRepo(t)
			probe.setup(t, dir, honest, authorCommit)

			_, output := runGateIn(t, dir)
			assertNeverCertifies(t, output)
		})
	}
}

// TestAmbientGitConfigInjectionCannotReachACertification is U1 P3, the cheapest
// route the reviewer found and the only one that writes nothing to disk: one
// environment variable, .git/config untouched, git status clean, and the
// variable gone the moment the process exits. It is the probe that made
// "repointing the remote is a materially louder act than creating a ref" false,
// and it is why this family ends by removing the capability rather than by
// scrubbing another input.
//
// This test cannot be parallel: it injects process-wide environment, which is
// exactly the channel under test.
func TestAmbientGitConfigInjectionCannotReachACertification(t *testing.T) {
	dir, honest, authorCommit := advisoryRepo(t)
	evil := authorOwnedRemote(t, dir, authorCommit)

	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+evil+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", honest)

	_, output := runGateIn(t, dir)
	assertNeverCertifies(t, output)
}

// TestAnHonestStandaloneRunIsStillOnlyAdvisory is the half that makes the
// removal a removal. The reviewer could reach a passing verdict because the
// honest route printed one; if an honest standalone run still certified, every
// probe above would only have to look honest. Nothing here is defeated: the
// remote is the operator's, the base is the true merge-base, and the change is
// a plain documentation edit that has nothing wrong with it. It still cannot be
// certified by a run whose base came from this repository.
func TestAnHonestStandaloneRunIsStillOnlyAdvisory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	trunk := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "docs")

	code, output := runGateIn(t, dir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0: nothing about this change blocks\n%s", code, output)
	}
	assertNeverCertifies(t, output)
	if !strings.Contains(output, "advisory-clean") {
		t.Fatalf("a clean advisory run must say so:\n%s", output)
	}
	// The base line still has to name what it resolved and how. Advisory does
	// not mean uninformative: the reviewer's complaint about the old header was
	// that it never named the URL it asked, so a defeated run read exactly like
	// an honest one.
	if !strings.Contains(output, "base: "+trunk+" from merge-base with origin/main at "+trunk) {
		t.Fatalf("the advisory run did not name the base it resolved:\n%s", output)
	}
	if !strings.Contains(output, "resolved by ls-remote against ") {
		t.Fatalf("the advisory run did not name the remote it asked:\n%s", output)
	}
}

// TestOnlyAnOrchestratorSuppliedBaseCanCertify is the other side of the split,
// and it is what keeps the product from being only advisory. The same
// repository and the same change reach a real verdict when the base comes from
// a pipeline that carved the worktree itself, because that base is not one the
// audited party wrote.
func TestOnlyAnOrchestratorSuppliedBaseCanCertify(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	trunk := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "docs")

	var stdout, stderr bytes.Buffer
	code := slopcli.Run(context.Background(), []string{"gate", "--repo", dir}, &stdout, &stderr, slopcli.Options{
		ReviewerFactory: noReviewer,
		PipelineBase: &slopcli.PipelineBase{
			Commit:        trunk,
			Origin:        "test orchestrator",
			DefaultBranch: "main",
		},
	})
	output := stdout.String() + stderr.String()
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, output)
	}
	if !strings.Contains(output, "verdict: pass") {
		t.Fatalf("a pipeline-supplied base must be able to certify:\n%s", output)
	}
	if strings.Contains(output, advisoryBanner) {
		t.Fatalf("a certifying run must not print the advisory banner:\n%s", output)
	}
	if strings.Contains(output, "advisory-clean") {
		t.Fatalf("a certifying run must not print the advisory verdict line:\n%s", output)
	}
}

// TestAnAdvisoryRunStillRunsEveryCheck pins the other half of the mode split.
// Advisory means the run cannot certify, never that it does less work: a
// standalone run still scans, still blocks, and still exits non-zero on a real
// finding. A mode that stopped looking would have traded one silent pass for
// another.
func TestAnAdvisoryRunStillRunsEveryCheck(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/notes")
	writeFile(t, dir, "docs/notes.md", "aws key = AKIAIOSFODNN7EXAMPLE\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "notes")

	code, output := runGateIn(t, dir)
	if code != 1 {
		t.Fatalf("exit = %d, want 1: a live credential blocks in advisory mode too\n%s", code, output)
	}
	assertNeverCertifies(t, output)
	if !strings.Contains(output, "advisory-blocked") {
		t.Fatalf("a blocked advisory run must say so:\n%s", output)
	}
	if !strings.Contains(output, "possible AWS access key shape") {
		t.Fatalf("the leak scan did not run in advisory mode:\n%s", output)
	}
}

// assertNeverCertifies is the whole U1 assertion in one place. A run that
// cannot certify must not print a verdict of any kind, and must say plainly
// where its base came from.
func assertNeverCertifies(t *testing.T, output string) {
	t.Helper()
	if strings.Contains(output, "verdict: pass") {
		t.Fatalf("a standalone run certified a change:\n%s", output)
	}
	if strings.Contains(output, "verdict:") {
		t.Fatalf("a standalone run printed a verdict line, which only a certifying run may do:\n%s", output)
	}
	if !strings.Contains(output, advisoryBanner) {
		t.Fatalf("the run did not print the advisory banner:\n%s", output)
	}
}

// TestMain keeps ambient GIT_CONFIG_* injection from an agent harness out of
// the tests that are not about it. The one test that exercises the injected
// channel re-sets the variables with t.Setenv, which restores them afterwards.
// See the Testing Conventions section of AGENTS.md.
func TestMain(m *testing.M) {
	os.Unsetenv("GIT_CONFIG_COUNT")
	os.Unsetenv("GIT_CONFIG_KEY_0")
	os.Unsetenv("GIT_CONFIG_VALUE_0")
	os.Exit(m.Run())
}
