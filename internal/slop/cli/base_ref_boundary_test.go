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

// Gate strength was moved off the worktree and onto the base ref, which closed
// every shape that edited a config file. It left one control behind: --base was
// a plain flag, so the caller chose which commit the operator's config came
// from. Committing a weakened .no-slop.yaml as the previous commit on the
// author's own branch and running --base HEAD~1 made that file the operator's
// config, reported zero drift, and passed an authorization weakening at exit 0.
//
// These tests pin the resolution rule that closed it: the base is the
// merge-base with a canonical ref, and an explicit --base is accepted only when
// it is an ancestor of HEAD that the canonical ref already carries.

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

// TestBaseOnTheAuthorsOwnBranchIsRefused is the I2 probe from round 3.
func TestBaseOnTheAuthorsOwnBranchIsRefused(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "internal/auth/policy.go", strictAuthPolicy)
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	runGit(t, dir, "switch", "-c", "feature/weaken")
	writeFile(t, dir, ".no-slop.yaml", weakGateConfig)
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "weak config")
	writeFile(t, dir, "internal/auth/policy.go", weakAuthPolicy)
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "weaken auth")

	code, output := runGateIn(t, dir, "--base", "HEAD~1")
	if code != 2 {
		t.Fatalf("exit = %d, want the caller-named base refused\n%s", code, output)
	}
	if strings.Contains(output, "verdict: pass") {
		t.Fatalf("a base on the branch under test passed an auth weakening:\n%s", output)
	}
	if !strings.Contains(output, "is not contained in") {
		t.Fatalf("the refusal does not say why the base was rejected:\n%s", output)
	}

	// With no --base at all the same repository resolves to the merge-base with
	// main, so the weakened config is reported as drift rather than honored.
	code, output = runGateIn(t, dir)
	if code == 0 {
		t.Fatalf("the auth weakening passed against the canonical base:\n%s", output)
	}
	if !strings.Contains(output, "gate-config-drift") {
		t.Fatalf("the weakened config was not reported as drift:\n%s", output)
	}
}

// TestDefaultBaseIsTheMergeBaseWithTheCanonicalRef is the positive half.
func TestDefaultBaseIsTheMergeBaseWithTheCanonicalRef(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "internal/auth/policy.go", strictAuthPolicy)
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	mergeBase := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "-c", "feature/weaken")
	writeFile(t, dir, ".no-slop.yaml", weakGateConfig)
	writeFile(t, dir, "internal/auth/policy.go", weakAuthPolicy)
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "weaken")

	code, output := runGateIn(t, dir)
	if code == 0 {
		t.Fatalf("an auth weakening passed:\n%s", output)
	}
	if !strings.Contains(output, "base: "+mergeBase+" from merge-base with main") {
		t.Fatalf("the run does not name the ref its strength came from:\n%s", output)
	}
	if !strings.Contains(output, "gate-config-drift") {
		t.Fatalf("the head config weakening was not reported as drift:\n%s", output)
	}
}

// TestAnAncestorBaseOnTheCanonicalRefIsAccepted keeps the flag usable. A
// pipeline legitimately knows the exact commit its branch left the trunk at,
// and that commit is a commit the operator's history already carries.
func TestAnAncestorBaseOnTheCanonicalRefIsAccepted(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "one")
	first := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	writeFile(t, dir, "README.md", "# Project\n\nSecond.\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "two")
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nSecond.\n\nThird.\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "three")

	code, output := runGateIn(t, dir, "--base", first)
	if code != 0 {
		t.Fatalf("exit = %d, want an older commit on main accepted\n%s", code, output)
	}
	if !strings.Contains(output, "verified as an ancestor of HEAD on main") {
		t.Fatalf("the accepted base is not explained:\n%s", output)
	}
}

// TestABaseThatIsNotAnAncestorOfHeadIsRefused covers the other half of the pair.
func TestABaseThatIsNotAnAncestorOfHeadIsRefused(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "one")
	branchPoint := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nBranch.\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "branch")
	// main advances past the branch point, so its tip is on the canonical ref
	// but is not an ancestor of the head under test.
	runGit(t, dir, "switch", "main")
	writeFile(t, dir, "NOTES.md", "# Notes\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "trunk moves")
	advanced := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "docs/readme")
	_ = branchPoint

	code, output := runGateIn(t, dir, "--base", advanced)
	if code != 2 {
		t.Fatalf("exit = %d, want a non-ancestor base refused\n%s", code, output)
	}
	if !strings.Contains(output, "is not an ancestor of the head revision") {
		t.Fatalf("the refusal does not name the ancestry failure:\n%s", output)
	}
}

// TestPinnedBaseRefSelectsTheCanonicalRef covers the operator-side knob. The
// pin is read from the config at the provisionally resolved base, so moving the
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
	runGit(t, dir, "switch", "-c", "release")
	writeFile(t, dir, "README.md", "# Project\n\nRelease.\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "release")
	releaseTip := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "-c", "feature/work")
	writeFile(t, dir, "README.md", "# Project\n\nRelease.\n\nWork.\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "work")

	code, output := runGateIn(t, dir)
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, output)
	}
	if !strings.Contains(output, "base: "+releaseTip+" from merge-base with release (canonical ref pinned by slop.base_ref)") {
		t.Fatalf("the pinned canonical ref was not used or not reported:\n%s", output)
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
