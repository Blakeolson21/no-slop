package cli_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/config"
	slopcli "github.com/Blakeolson21/no-slop/internal/slop/cli"
	"github.com/Blakeolson21/no-slop/internal/slop/engine"
	"github.com/Blakeolson21/no-slop/internal/slop/provenance"
)

// Gate strength is read from the base ref, so which commit is the base is the
// last control that must not be reachable by the change under test. Three
// review rounds have taken it away in three disguises.
//
// Round 3 took `--base` as given: committing a weakened .no-slop.yaml as the
// previous commit and running `--base HEAD~1` made that file the operator's
// config and passed an authorization weakening at exit 0. The replacement
// resolved the base as merge-base with a canonical ref.
//
// Round 4 took the canonical ref. It was resolved by rev-parsing the string
// "origin/main", and git resolves a bare name through refs/, refs/tags/,
// refs/heads/, and refs/remotes/ in order, so `git branch origin/main`, `git tag
// origin/main`, `git update-ref refs/remotes/origin/main`, and `git fetch .
// +<sha>:refs/remotes/origin/main` each made an author-owned commit the base,
// with a run header byte-identical to an honest run.
//
// These tests pin the rule that closed it: the canonical commit comes from
// `git ls-remote` against the configured remote, or from an orchestrating
// pipeline through the Go API, and from nothing else. No local ref and no flag
// participates, and a run that cannot establish it is pinned to the full tier.

const weakGateConfig = "slop:\n  test_count_floor: false\n  risk:\n    single_review_threshold: 99\n    full_adversarial_threshold: 100\n"

const strictAuthPolicy = "package auth\n\nfunc Allow(role string, mfa bool) bool {\n\treturn role == \"admin\" && mfa\n}\n"

const weakAuthPolicy = "package auth\n\nfunc Allow(role string, mfa bool) bool {\n\treturn role == \"admin\" || mfa\n}\n"

func noReviewer(context.Context, *config.Config, io.Writer) (engine.Reviewer, io.Closer, error) {
	return nil, nil, errors.New("no runnable agent found")
}

func runGateIn(t *testing.T, dir string, args ...string) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	full := append([]string{"gate", "--repo", dir}, args...)
	code := slopcli.Run(context.Background(), full, &stdout, &stderr, slopcli.Options{ReviewerFactory: noReviewer})
	return code, stdout.String() + stderr.String()
}

// shadowRepo is the reviewer's T1 shape: a real clone of a real remote, with
// origin/main present and correct, two commits on the author's branch, the
// first a weakened .no-slop.yaml and the second an authorization weakening. The
// weak config stays at head so an honest run reports drift rather than
// difference.
func shadowRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "internal/auth/policy.go", strictAuthPolicy)
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	trunk := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "feature/weaken")
	writeFile(t, dir, ".no-slop.yaml", weakGateConfig)
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "weak config")
	writeFile(t, dir, "internal/auth/policy.go", weakAuthPolicy)
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "weaken auth")
	return dir, trunk
}

// TestNoLocalRefCanShadowTheCanonicalRef is the T1 probe set, run against a
// repository whose origin/main is real and correct. Every one of these four
// commands made an author-owned commit the base in round 4.
func TestNoLocalRefCanShadowTheCanonicalRef(t *testing.T) {
	t.Parallel()

	for _, probe := range []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{"local branch", func(t *testing.T, dir string) {
			runGit(t, dir, "branch", "origin/main", "HEAD~1")
		}},
		{"annotated tag", func(t *testing.T, dir string) {
			runGit(t, dir, "tag", "-a", "-m", "shadow", "origin/main", "HEAD~1")
		}},
		{"hand-written tracking ref", func(t *testing.T, dir string) {
			runGit(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD~1")
		}},
		{"fetch into the tracking ref", func(t *testing.T, dir string) {
			runGit(t, dir, "branch", "shadow-source", "HEAD~1")
			runGit(t, dir, "fetch", ".", "+refs/heads/shadow-source:refs/remotes/origin/main")
		}},
	} {
		t.Run(probe.name, func(t *testing.T) {
			t.Parallel()

			dir, trunk := shadowRepo(t)
			shadowed := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD~1"))
			probe.setup(t, dir)

			code, output := runGateIn(t, dir)
			if code == 0 {
				t.Fatalf("the auth weakening passed:\n%s", output)
			}
			if strings.Contains(output, "verdict: pass") {
				t.Fatalf("a shadowed ref reached a passing verdict:\n%s", output)
			}
			if !strings.Contains(output, "base: "+trunk+" from merge-base with origin/main at "+trunk) {
				t.Fatalf("the base did not come from the remote:\n%s", output)
			}
			if strings.Contains(output, "base: "+shadowed) {
				t.Fatalf("the shadowing ref supplied the base:\n%s", output)
			}
			if !strings.Contains(output, "gate-config-drift") {
				t.Fatalf("the weakened config was honored rather than reported as drift:\n%s", output)
			}
		})
	}
}

// TestTheBaseFlagIsRemovedRatherThanValidated pins the shape of the fix. Round 3
// validated `--base` against a canonical ref and round 4 defeated the canonical
// ref, so the flag is gone: validating an input the author writes is the fix
// shape that failed twice.
func TestTheBaseFlagIsRemovedRatherThanValidated(t *testing.T) {
	t.Parallel()

	dir, _ := shadowRepo(t)
	for _, probe := range []string{"HEAD~1", "HEAD", strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD~2"))} {
		code, output := runGateIn(t, dir, "--base", probe)
		if code != 2 {
			t.Fatalf("--base %s: exit = %d, want the flag refused\n%s", probe, code, output)
		}
		if !strings.Contains(output, "--base was removed and no longer selects the base revision") {
			t.Fatalf("--base %s: the refusal does not explain itself:\n%s", probe, output)
		}
		if strings.Contains(output, "verdict:") {
			t.Fatalf("--base %s: the run reached a verdict after refusing the flag:\n%s", probe, output)
		}
	}
}

// TestTheVerifiedBaseNamesTheRemoteAndTheCommitItAnswered is the disclosure
// half. The old header said "from merge-base with origin/main" whether
// origin/main was the operator's remote branch or a branch the author created
// ten seconds earlier, which is what made a defeated run unreadable.
func TestTheVerifiedBaseNamesTheRemoteAndTheCommitItAnswered(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	trunk := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	remote := attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "docs")

	code, output := runGateIn(t, dir)
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, output)
	}
	want := "base: " + trunk + " from merge-base with origin/main at " + trunk + ", resolved by ls-remote against "
	if !strings.Contains(output, want) {
		t.Fatalf("run header missing %q:\n%s", want, output)
	}
	// The URL itself is the round-5 addition. "verified by ls-remote against
	// the configured remote" named no remote, so a run redirected by one
	// insteadOf rewrite printed the same sentence as an honest one.
	if !strings.Contains(output, remote) {
		t.Fatalf("run header does not name the remote it asked (%s):\n%s", remote, output)
	}
}

// TestAnUnverifiableBaseIsPinnedToTheFullTier is resolution step three. A
// repository with no remote has no history outside the change under test, so
// there is nothing to read gate strength from and the cheap routes are removed
// rather than made conditional.
func TestAnUnverifiableBaseIsPinnedToTheFullTier(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	// The operator's own cheapest tier, at the base ref. It must not be reached
	// through a base this run could not verify.
	writeFile(t, dir, ".no-slop.yaml", "slop:\n  risk:\n    single_review_threshold: 90\n    full_adversarial_threshold: 99\n")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "docs")

	code, output := runGateIn(t, dir)
	if code == 0 {
		t.Fatalf("a run that could not verify its base passed:\n%s", output)
	}
	for _, want := range []string{
		"UNVERIFIED",
		"this repository has no remote named \"origin\"",
		"tier: full-adversarial",
		"escalation: the canonical base could not be verified against the operator's remote",
		"base-ref-unverified",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "tier: leak-scan-only") {
		t.Fatalf("the base config was honored through an unverified base:\n%s", output)
	}
}

// TestAnUnverifiableBaseCannotBeLoweredByTheTierFlag keeps the pin a pin.
func TestAnUnverifiableBaseCannotBeLoweredByTheTierFlag(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "docs")

	code, output := runGateIn(t, dir, "--tier", "leak-scan-only", "--force-tier")
	if code == 0 {
		t.Fatalf("the tier flag lowered an unverified run:\n%s", output)
	}
	if !strings.Contains(output, "would lower the computed tier full-adversarial") {
		t.Fatalf("the refusal does not name the pinned tier:\n%s", output)
	}
}

// TestAPipelineSuppliedBaseIsTrustedAndNamed covers resolution step one. The
// channel is a Go field with no flag, file, or ref equivalent, which is the
// property that makes it safe when a flag was not.
func TestAPipelineSuppliedBaseIsTrustedAndNamed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	base := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "docs")

	var stdout, stderr bytes.Buffer
	code := slopcli.Run(context.Background(), []string{"gate", "--repo", dir}, &stdout, &stderr, slopcli.Options{
		ReviewerFactory: noReviewer,
		PipelineBase:    &slopcli.PipelineBase{Commit: base, Origin: "no-mistakes run 42", DefaultBranch: "main"},
	})
	output := stdout.String() + stderr.String()
	if code != 0 {
		t.Fatalf("exit = %d, want the pipeline base honored in a repository with no remote\n%s", code, output)
	}
	want := "base: " + base + " supplied by the orchestrating pipeline (no-mistakes run 42); no local ref and no flag took part"
	if !strings.Contains(output, want) {
		t.Fatalf("run header missing %q:\n%s", want, output)
	}
}

// TestNoConventionalTrunkNeedsAPinAlreadyInHistory covers the bootstrap, and
// with it T6. A repository whose trunk is neither main nor master has to be able
// to name it, and the head tree is the only place that name can come from before
// a base exists, so the base it resolves to must independently carry the same
// pin.
func TestNoConventionalTrunkNeedsAPinAlreadyInHistory(t *testing.T) {
	t.Parallel()

	newRepo := func(t *testing.T, pinAtTrunk bool) (string, string) {
		t.Helper()
		dir := t.TempDir()
		runGit(t, dir, "init", "-b", "trunk")
		runGit(t, dir, "config", "user.email", "test@example.com")
		runGit(t, dir, "config", "user.name", "Test")
		if pinAtTrunk {
			writeFile(t, dir, ".no-slop.yaml", "slop:\n  base_ref:\n    branch: trunk\n")
		}
		writeFile(t, dir, "README.md", "# Project\n")
		runGit(t, dir, "add", "-A")
		runGit(t, dir, "commit", "-m", "initial")
		tip := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
		remote := t.TempDir()
		runGit(t, remote, "init", "--bare", "-b", "trunk")
		runGit(t, dir, "remote", "add", "origin", remote)
		runGit(t, dir, "push", "-q", "origin", "trunk")
		runGit(t, dir, "switch", "-c", "feature/work")
		writeFile(t, dir, ".no-slop.yaml", "slop:\n  base_ref:\n    branch: trunk\n")
		writeFile(t, dir, "README.md", "# Project\n\nWork.\n")
		runGit(t, dir, "add", "-A")
		runGit(t, dir, "commit", "-m", "work")
		return dir, tip
	}

	established, tip := newRepo(t, true)
	code, output := runGateIn(t, established)
	if code != 0 {
		t.Fatalf("exit = %d, want an established pin honored\n%s", code, output)
	}
	if !strings.Contains(output, "base: "+tip+" from merge-base with origin/trunk at "+tip+", resolved by ls-remote against ") || !strings.Contains(output, "pinned by slop.base_ref") {
		t.Fatalf("the established pin was not used or not reported:\n%s", output)
	}

	asserted, _ := newRepo(t, false)
	code, output = runGateIn(t, asserted)
	if code != 2 {
		t.Fatalf("exit = %d, want a head-only pin refused\n%s", code, output)
	}
	if !strings.Contains(output, "is naming its own canonical ref") {
		t.Fatalf("the refusal does not explain the bootstrap rule:\n%s", output)
	}
}

// TestTheBootstrapPinCannotBeSatisfiedByALocalBranch is T6 stated directly.
// Round 4 bootstrapped the pin by committing it with weak thresholds, pointing a
// LOCAL branch at that commit, and naming the local branch: the pin resolved
// locally, so the author's own commit certified itself. The candidate is
// resolved on the remote now, so a local branch answers nothing.
func TestTheBootstrapPinCannotBeSatisfiedByALocalBranch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "work")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "internal/auth/policy.go", strictAuthPolicy)
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	remote := t.TempDir()
	runGit(t, remote, "init", "--bare", "-b", "work")
	runGit(t, dir, "remote", "add", "origin", remote)
	runGit(t, dir, "push", "-q", "origin", "work")

	// Commit one carries the pin and the weak thresholds together, exactly as
	// the probe did, so the config at the commit the pin names does carry the
	// pin. Only the ref is local.
	writeFile(t, dir, ".no-slop.yaml", "slop:\n  base_ref:\n    branch: weakbase\n"+
		"  risk:\n    single_review_threshold: 99\n    full_adversarial_threshold: 100\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "pin plus weak thresholds")
	runGit(t, dir, "branch", "weakbase", "HEAD")
	writeFile(t, dir, "internal/auth/policy.go", weakAuthPolicy)
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "weaken auth")

	code, output := runGateIn(t, dir)
	if code == 0 {
		t.Fatalf("a locally pinned base passed an auth weakening:\n%s", output)
	}
	if strings.Contains(output, "verdict: pass") {
		t.Fatalf("a locally pinned base reached a passing verdict:\n%s", output)
	}
	if strings.Contains(output, "tier: leak-scan-only") {
		t.Fatalf("the self-certified weak thresholds took effect:\n%s", output)
	}
}

// TestPinnedBaseRefSelectsTheCanonicalRef covers the operator-side knob. The pin
// is read from the config at the provisionally resolved base, so moving the
// canonical ref is authorized by the previous canonical ref rather than by the
// change proposing the move.
func TestPinnedBaseRefSelectsTheCanonicalRef(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, ".no-slop.yaml", "slop:\n  base_ref:\n    branch: release\n")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "release")
	writeFile(t, dir, "README.md", "# Project\n\nRelease.\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "release")
	releaseTip := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "push", "-q", "origin", "release")
	runGit(t, dir, "switch", "-c", "feature/work")
	writeFile(t, dir, "README.md", "# Project\n\nRelease.\n\nWork.\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "work")

	code, output := runGateIn(t, dir)
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, output)
	}
	want := "base: " + releaseTip + " from merge-base with origin/release at " + releaseTip + ", resolved by ls-remote against "
	if !strings.Contains(output, want) || !strings.Contains(output, "pinned by slop.base_ref") {
		t.Fatalf("the pinned canonical ref was not used or not reported:\n%s", output)
	}
}

// TestACanonicalCommitAbsentFromTheObjectStoreRefuses is the honest failure for
// a stale clone. Guessing a nearer commit would put the base back inside the
// author's reach through the object store.
func TestACanonicalCommitAbsentFromTheObjectStoreRefuses(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	remote := attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "docs")

	// The remote advances out of band, so its main names a commit this clone
	// has never seen.
	other := t.TempDir()
	runGit(t, other, "clone", "-q", remote, ".")
	runGit(t, other, "config", "user.email", "test@example.com")
	runGit(t, other, "config", "user.name", "Test")
	writeFile(t, other, "NOTES.md", "# Notes\n")
	runGit(t, other, "add", "-A")
	runGit(t, other, "commit", "-m", "trunk moves")
	runGit(t, other, "push", "-q", "origin", "main")

	code, output := runGateIn(t, dir)
	if code != 2 {
		t.Fatalf("exit = %d, want a refusal for an unfetched canonical commit\n%s", code, output)
	}
	if !strings.Contains(output, "does not hold that commit; fetch origin before gating") {
		t.Fatalf("the refusal does not name the missing commit:\n%s", output)
	}
}

// TestTheRunHeaderStatesTheSelfAssertedIdentity is the S6 disclosure half. The
// lane and model are strings the caller asserts about itself, and a reviewer
// reading the output has to be able to see that.
func TestTheRunHeaderStatesTheSelfAssertedIdentity(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "docs")

	code, output := runGateIn(t, dir, "--lane-id", "lane-a", "--model", "model-x")
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, output)
	}
	want := "provenance identity: lane lane-a, model model-x, self-asserted by the caller and not authenticated; history at .noslop-data"
	if !strings.Contains(output, want) {
		t.Fatalf("run header missing %q:\n%s", want, output)
	}
	if strings.Contains(output, dir) {
		t.Fatalf("the run header printed an absolute path, which NoSlop's own identity scan treats as a leak:\n%s", output)
	}
}

// TestAFreshSelfAssertedLaneCostsTheSameAsAnOmittedOne is T5. Omitting the lane
// escalated on a store that records identities, and asserting a lane the store
// has never seen took the v1 route, so `--lane-id lane-zzz` cleared an
// escalation that saying nothing could not. Lying was cheaper than silence,
// which inverts the incentive the omission rule exists to create.
func TestAFreshSelfAssertedLaneCostsTheSameAsAnOmittedOne(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, ".no-slop.yaml", "slop:\n  data_dir: .review-history\n")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "docs")

	store := provenance.NewFileStore(filepath.Join(dir, ".review-history"))
	if err := store.Append(provenance.Record{
		ChangeID:     "aaa..bbb",
		Model:        "model-x",
		AgentLaneID:  "lane-a",
		SelectedTier: "single-review",
		Outcome:      "fail",
		FindingsByLens: map[string]provenance.LensFindings{
			"test-capitulation": {Accepted: []provenance.Finding{{Description: "test weakened"}}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	for _, probe := range []struct {
		name string
		args []string
	}{
		{"omitted", nil},
		{"fresh lane", []string{"--lane-id", "lane-zzz", "--model", "model-x"}},
		{"fresh model", []string{"--lane-id", "lane-a", "--model", "model-zzz"}},
		{"both fresh", []string{"--lane-id", "lane-zzz", "--model", "model-zzz"}},
	} {
		code, output := runGateIn(t, dir, probe.args...)
		if code == 0 {
			t.Fatalf("%s: an unverifiable identity took the cheap route:\n%s", probe.name, output)
		}
		if !strings.Contains(output, "tier: full-adversarial") {
			t.Fatalf("%s: identity did not escalate:\n%s", probe.name, output)
		}
		if !strings.Contains(output, "escalating to full-adversarial") {
			t.Fatalf("%s: the escalation is not explained:\n%s", probe.name, output)
		}
	}

	// Round 5 found the escalation cost exactly one throwaway invocation. The
	// escalated run cannot reach a reviewer, so it exits 2 and appends its own
	// record with outcome "error", and the next run under the same fresh
	// identity found history and took the v1 route. The record is still written,
	// because a refused run belongs in the audit trail; what it no longer does
	// is answer "has this lane been judged here". Running the same probes a
	// second time is the whole test.
	for _, probe := range []struct {
		name string
		args []string
	}{
		{"omitted, second run", nil},
		{"fresh lane, second run", []string{"--lane-id", "lane-zzz", "--model", "model-x"}},
		{"both fresh, second run", []string{"--lane-id", "lane-zzz", "--model", "model-zzz"}},
	} {
		code, output := runGateIn(t, dir, probe.args...)
		if code == 0 {
			t.Fatalf("%s: one throwaway run bought the cheap route:\n%s", probe.name, output)
		}
		if !strings.Contains(output, "tier: full-adversarial") {
			t.Fatalf("%s: the escalation was shed by the first run's own record:\n%s", probe.name, output)
		}
	}
}

// TestDeletingTheProvenanceStoreEscalatesTheRun is the S6 probe end to end.
// Removing the history used to print the same line an honest first-time lane
// prints and drop the tier back to the v1 route.
func TestDeletingTheProvenanceStoreEscalatesTheRun(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, ".no-slop.yaml", "slop:\n  data_dir: .review-history\n")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "docs")

	store := provenance.NewFileStore(filepath.Join(dir, ".review-history"))
	if err := store.Append(provenance.Record{
		ChangeID:     "aaa..bbb",
		Model:        "model-x",
		AgentLaneID:  "lane-a",
		SelectedTier: "single-review",
		Outcome:      "fail",
		FindingsByLens: map[string]provenance.LensFindings{
			"test-capitulation": {Accepted: []provenance.Finding{{Description: "test weakened"}}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(dir, ".review-history", provenance.FileName)); err != nil {
		t.Fatal(err)
	}

	code, output := runGateIn(t, dir, "--lane-id", "lane-a", "--model", "model-x")
	if code == 0 {
		t.Fatalf("deleting the history passed a run that would otherwise consult it:\n%s", output)
	}
	if !strings.Contains(output, "tier: full-adversarial") {
		t.Fatalf("a removed store did not escalate:\n%s", output)
	}
	if !strings.Contains(output, "escalating to full-adversarial") {
		t.Fatalf("the escalation is not explained:\n%s", output)
	}
}

// TestProvenanceRequiredRefusesAnAbsentStore covers the operator-side half of
// the same defect, for a repository that depends on escalation history.
func TestProvenanceRequiredRefusesAnAbsentStore(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, ".no-slop.yaml", "slop:\n  provenance_required: true\n")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "docs")

	code, output := runGateIn(t, dir)
	if code != 2 {
		t.Fatalf("exit = %d, want the run refused with no history present\n%s", code, output)
	}
	if !strings.Contains(output, "slop.provenance_required is set at the base ref") {
		t.Fatalf("the refusal does not name the requirement:\n%s", output)
	}
}
