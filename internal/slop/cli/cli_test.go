package cli_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/config"
	slopcli "github.com/Blakeolson21/no-slop/internal/slop/cli"
	"github.com/Blakeolson21/no-slop/internal/slop/engine"
	"github.com/Blakeolson21/no-slop/internal/slop/provenance"
)

// cheapestTierConfig is the operator's own route to the cheapest tier, written
// at the BASE ref where gate strength belongs. It replaces the
// `--tier leak-scan-only` these tests used as a seam: the tier flag may only
// raise a computed tier now, because on this command line the caller and the
// audited party are the same agent.
const cheapestTierConfig = "slop:\n  risk:\n    single_review_threshold: 90\n    full_adversarial_threshold: 99\n"

type emptyReviewer struct{ calls int }

func (r *emptyReviewer) Review(context.Context, engine.ReviewRequest) ([]engine.Finding, error) {
	r.calls++
	return nil, nil
}

type failingProvenanceStore struct{}

func (failingProvenanceStore) Window(string, string) ([]provenance.Record, error) {
	return nil, nil
}

func (failingProvenanceStore) HasIdentifiedHistory() (bool, error) {
	return false, nil
}

func (failingProvenanceStore) Append(provenance.Record) error {
	return errors.New("write denied")
}

func TestRunGatePrintsMarkdownTierAndReasons(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, ".noslop-blocklist", "# intentionally empty\n")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", ".noslop-blocklist", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "docs")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir, "--tier", "leak-scan-only"}, &stdout, &stderr, slopcli.Options{})
	if exitCode != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"tier: leak-scan-only",
		"blast radius:",
		"novelty:",
		"reversibility:",
		"review: skipped",
		"tests: skipped",
		// Standalone runs are advisory since round 5: the base came from the
		// repository under test, so the run reports rather than certifies.
		"advisory-clean",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestRunGatePrintsMandatoryCheckStatus(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "docs")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir}, &stdout, &stderr, slopcli.Options{})
	if exitCode != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"mandatory check: gate config completed (0 findings)",
		"mandatory check: content integrity completed (0 findings)",
		// The pre-check names the detector it could not arm. Without a stated
		// intent the scope-expansion detector cannot run, and a bare
		// "0 findings" claimed coverage the pass did not have.
		"mandatory check: lens pre-check completed (0 findings, not armed: scope-expansion needs a stated intent)",
		"mandatory check: leak scan completed (0 findings)",
		"mandatory check: test-count floor completed (0 findings)",
		"mandatory check: prose oracle completed (0 findings)",
		// A run that never looked at a thread says so, instead of producing
		// output byte-identical to a run that looked and was satisfied.
		"mandatory check: live thread check disabled",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output missing %q:\n%s", want, stdout.String())
		}
	}
}

// TestRunGateArmsScopeExpansionWhenIntentIsStated is the other half: with an
// intent supplied the detector runs and the line no longer carries a caveat.
func TestRunGateArmsScopeExpansionWhenIntentIsStated(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "docs")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir, "--intent", "Refresh the README only."}, &stdout, &stderr, slopcli.Options{})
	if exitCode != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	if want := "mandatory check: lens pre-check completed (0 findings)\n"; !strings.Contains(stdout.String(), want) {
		t.Fatalf("output missing %q:\n%s", want, stdout.String())
	}
}

// TestRunGateAtTheOperatorsCheapestTierStillBlocksLeak keeps the mandatory scan
// tier-independent. The route to the cheapest tier is the operator's threshold
// config at the base ref, because the flag that used to get there could be set
// by the change under test.
func TestRunGateAtTheOperatorsCheapestTierStillBlocksLeak(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, ".no-slop.yaml", cheapestTierConfig)
	writeFile(t, dir, ".noslop-blocklist", "# intentionally empty\n")
	writeFile(t, dir, "policy.go", "package policy\n")
	runGit(t, dir, "add", ".no-slop.yaml", ".noslop-blocklist", "policy.go")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "feature/policy")
	writeFile(t, dir, "policy.go", "package policy\n\nconst token = \"ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ\"\n") // noslop:allow-leak
	runGit(t, dir, "add", "policy.go")
	runGit(t, dir, "commit", "-m", "change")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir}, &stdout, &stderr, slopcli.Options{})
	if exitCode != 1 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"tier: leak-scan-only",
		"finding: [leak-identity-scan] policy.go:3",
		"advisory-blocked",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output missing %q:\n%s", want, stdout.String())
		}
	}
}

// TestRunGateRaisesButNeverLowersTheComputedTier is the command-line half of
// the escalate-only ruling. The round-3 review carried an authorization
// weakening and a fleet-instruction rewrite to "verdict: pass" at exit 0 with
// nothing but `--tier leak-scan-only`, and `--force-tier` did the same over a
// live provenance escalation.
func TestRunGateRaisesButNeverLowersTheComputedTier(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "internal/auth/policy.go", "package auth\n\nfunc Allow(role string, mfa bool) bool {\n\treturn role == \"admin\" && mfa\n}\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "feature/weaken")
	writeFile(t, dir, "internal/auth/policy.go", "package auth\n\nfunc Allow(role string, mfa bool) bool {\n\treturn role == \"admin\" || mfa\n}\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "weaken")

	for _, probe := range []struct {
		name string
		args []string
	}{
		{name: "plain lowering", args: []string{"--tier", "leak-scan-only"}},
		{name: "forced lowering", args: []string{"--tier", "leak-scan-only", "--force-tier"}},
		{name: "one step down", args: []string{"--tier", "single-review"}},
	} {
		t.Run(probe.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := append([]string{"gate", "--repo", dir}, probe.args...)
			exitCode := slopcli.Run(context.Background(), args, &stdout, &stderr, slopcli.Options{})
			if exitCode != 2 {
				t.Fatalf("exit = %d, want the lowering refused\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String(), "verdict: pass") {
				t.Fatalf("a lowered tier reached a passing verdict:\n%s", stdout.String())
			}
			if !strings.Contains(stdout.String(), "override refused: full-adversarial") {
				t.Fatalf("the refusal is not printed with the computed tier:\n%s", stdout.String())
			}
			if !strings.Contains(stderr.String(), "would lower the computed tier full-adversarial") {
				t.Fatalf("the error does not name the computed tier:\n%s", stderr.String())
			}
		})
	}

	var stdout, stderr bytes.Buffer
	raise := []string{"gate", "--repo", dir, "--tier", "full-adversarial"}
	if code := slopcli.Run(context.Background(), raise, &stdout, &stderr, slopcli.Options{
		ReviewerFactory: func(context.Context, *config.Config, io.Writer) (engine.Reviewer, io.Closer, error) {
			return nil, nil, errors.New("no runnable agent found")
		},
	}); code == 0 {
		t.Fatalf("an auth weakening passed at the full tier:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "override refused") {
		t.Fatalf("a request for the tier already computed was reported as refused:\n%s", stdout.String())
	}
}

func TestRunGateUsesNoBlocklistWhenDefaultFileIsMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "docs")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir}, &stdout, &stderr, slopcli.Options{})
	if exitCode != 0 {
		t.Fatalf("exit = %d, want clean gate without a default blocklist\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	if want := "leak scan: no private-name blocklist (default path .noslop-blocklist not present)"; !strings.Contains(stdout.String(), want) {
		t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
	}
}

// TestRunGateNamesAPathScannedWholeBecauseGitEmittedNoHunks pins the reporting
// half of the diff-suppressed fallback. A committed `-diff` attribute makes git
// produce no hunks, so the scanner reads the whole head blob instead of the
// added lines. The state was recorded on the loaded change and read by nothing,
// so the run printed "leak scan completed (N findings)" with no qualifier and an
// operator could not tell that path had been read through a different window
// than every other path.
func TestRunGateNamesAPathScannedWholeBecauseGitEmittedNoHunks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, ".no-slop.yaml", cheapestTierConfig)
	writeFile(t, dir, ".gitattributes", "NOTES.md -diff\n")
	writeFile(t, dir, "NOTES.md", "# Notes\n\nnothing here yet\n")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/notes")
	writeFile(t, dir, "NOTES.md", "# Notes\n\nplain prose with no credential\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "notes")

	var stdout, stderr bytes.Buffer
	slopcli.Run(context.Background(), []string{"gate", "--repo", dir}, &stdout, &stderr, slopcli.Options{})
	output := stdout.String() + stderr.String()
	want := "reduced coverage: NOTES.md produced no diff hunks, so the whole head blob was scanned instead of the added lines"
	if !strings.Contains(output, want) {
		t.Fatalf("the leak scan did not name the path it read through a different window:\nwant %q\n%s", want, output)
	}
}

func TestRunGateReportsEveryHonoredLeakExemption(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	// Inline exemptions default OFF, so honoring one is an operator decision
	// taken at the base ref like every other gate-strength decision.
	writeFile(t, dir, ".no-slop.yaml", cheapestTierConfig+"  leak_scan:\n    allow_exemptions: true\n")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", ".no-slop.yaml", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "test/leak-fixtures")
	writeFile(t, dir, "fixtures/tokens.txt", "TOKEN=ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ # noslop:allow-leak\nAWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE # noslop:allow-leak\n") // noslop:allow-leak
	runGit(t, dir, "add", "fixtures/tokens.txt")
	runGit(t, dir, "commit", "-m", "add fixtures")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir}, &stdout, &stderr, slopcli.Options{})
	if exitCode != 0 {
		t.Fatalf("exit = %d, want exemptions honored\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"leak exemption: fixtures/tokens.txt:1: noslop:allow-leak",
		"leak exemption: fixtures/tokens.txt:2: noslop:allow-leak",
		"leak scan: 2 leak exemptions honored",
		"advisory-clean",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestRunGateReportsLeakExemptionBeforeLaterReviewerError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, ".no-slop.yaml", "slop:\n  leak_scan:\n    allow_exemptions: true\n")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "test/leak-fixture")
	writeFile(t, dir, "fixtures/token.txt", "TOKEN=ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ # noslop:allow-leak\n") // noslop:allow-leak
	runGit(t, dir, "add", "fixtures/token.txt")
	runGit(t, dir, "commit", "-m", "add fixture")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir}, &stdout, &stderr, slopcli.Options{
		ReviewerFactory: func(context.Context, *config.Config, io.Writer) (engine.Reviewer, io.Closer, error) {
			return nil, nil, errors.New("review unavailable")
		},
	})
	if exitCode != 2 {
		t.Fatalf("exit = %d, want later reviewer error\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	if want := "leak exemption: fixtures/token.txt:1: noslop:allow-leak"; !strings.Contains(stdout.String(), want) {
		t.Fatalf("stdout missing exemption audit before error %q:\n%s", want, stdout.String())
	}
}

func TestRunGateCanRefuseInlineLeakExemptions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, ".no-slop.yaml", "slop:\n  leak_scan:\n    allow_exemptions: false\n  risk:\n    single_review_threshold: 90\n    full_adversarial_threshold: 99\n")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", ".no-slop.yaml", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "test/leak-fixtures")
	writeFile(t, dir, "fixtures/tokens.txt", "TOKEN=ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ # noslop:allow-leak\n") // noslop:allow-leak
	runGit(t, dir, "add", "fixtures/tokens.txt")
	runGit(t, dir, "commit", "-m", "add fixture")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir}, &stdout, &stderr, slopcli.Options{})
	if exitCode != 1 {
		t.Fatalf("exit = %d, want refused exemption to fail the gate\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	want := "finding: [leak-identity-scan] fixtures/tokens.txt:1: inline leak exemption noslop:allow-leak is disabled by configuration"
	if !strings.Contains(stdout.String(), want) {
		t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
	}
	if strings.Contains(stdout.String(), "leak exemptions honored") {
		t.Fatalf("refused exemption was reported as honored:\n%s", stdout.String())
	}
}

func TestRunGateFailsClosedWhenExplicitBlocklistIsMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "docs")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir, "--blocklist", "missing-private-names"}, &stdout, &stderr, slopcli.Options{})
	if exitCode != 2 {
		t.Fatalf("exit = %d, want evaluation failure\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "read private-name blocklist") {
		t.Fatalf("stderr does not name blocklist failure: %s", stderr.String())
	}
}

func TestRunGateFailsClosedWhenConfiguredBlocklistIsMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, ".no-slop.yaml", "slop:\n  leak_scan:\n    blocklist_file: missing-private-names\n")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", ".no-slop.yaml", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "docs")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir}, &stdout, &stderr, slopcli.Options{})
	if exitCode != 2 {
		t.Fatalf("exit = %d, want evaluation failure\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "read private-name blocklist") {
		t.Fatalf("stderr does not name configured blocklist failure: %s", stderr.String())
	}
}

func TestRunGateReportsBlocklistEntryCountSoAnEmptyListIsVisible(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		blocklist string
		want      string
	}{
		"entries present": {blocklist: "zephyrbox\nquiethollow\n", want: "leak scan: loaded configured private-name blocklist from .noslop-blocklist at the base ref (2 entries)"},
		"comments only":   {blocklist: "# add one private name per line\n\n", want: "leak scan: loaded configured private-name blocklist from .noslop-blocklist at the base ref (0 entries)"},
		"file is empty":   {blocklist: "", want: "leak scan: loaded configured private-name blocklist from .noslop-blocklist at the base ref (0 entries)"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			runGit(t, dir, "init", "-b", "main")
			runGit(t, dir, "config", "user.email", "test@example.com")
			runGit(t, dir, "config", "user.name", "Test")
			writeFile(t, dir, ".no-mistakes.yaml", "slop:\n  leak_scan:\n    blocklist_file: .noslop-blocklist\n")
			writeFile(t, dir, ".noslop-blocklist", tc.blocklist)
			writeFile(t, dir, "README.md", "# Project\n")
			runGit(t, dir, "add", ".no-mistakes.yaml", ".noslop-blocklist", "README.md")
			runGit(t, dir, "commit", "-m", "initial")
			attachRemote(t, dir)
			runGit(t, dir, "switch", "-c", "docs/readme")
			writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
			runGit(t, dir, "add", "README.md")
			runGit(t, dir, "commit", "-m", "docs")

			var stdout, stderr bytes.Buffer
			exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir}, &stdout, &stderr, slopcli.Options{})
			if exitCode != 0 {
				t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.want) {
				t.Fatalf("output missing %q:\n%s", tc.want, stdout.String())
			}
		})
	}
}

func TestRunGateFailsClosedWhenDefaultOrConfiguredBlocklistIsUnreadable(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		config    string
		blocklist string
		wantState string
	}{
		{name: "default", blocklist: ".noslop-blocklist", wantState: "default path .noslop-blocklist"},
		{name: "configured", config: "slop:\n  leak_scan:\n    blocklist_file: private-names\n", blocklist: "private-names", wantState: "configured path private-names"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			runGit(t, dir, "init", "-b", "main")
			runGit(t, dir, "config", "user.email", "test@example.com")
			runGit(t, dir, "config", "user.name", "Test")
			if tc.config != "" {
				writeFile(t, dir, ".no-slop.yaml", tc.config)
			}
			if err := os.Mkdir(filepath.Join(dir, tc.blocklist), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, dir, "README.md", "# Project\n")
			runGit(t, dir, "add", "README.md")
			if tc.config != "" {
				runGit(t, dir, "add", ".no-slop.yaml")
			}
			runGit(t, dir, "commit", "-m", "initial")
			attachRemote(t, dir)
			runGit(t, dir, "switch", "-c", "docs/readme")
			writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
			runGit(t, dir, "add", "README.md")
			runGit(t, dir, "commit", "-m", "docs")

			var stdout, stderr bytes.Buffer
			exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir}, &stdout, &stderr, slopcli.Options{})
			if exitCode != 2 {
				t.Fatalf("exit = %d, want unreadable blocklist failure\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.wantState) {
				t.Fatalf("stderr missing state %q:\n%s", tc.wantState, stderr.String())
			}
		})
	}
}

func TestRunGateAppendsProvenanceForBlockingFinding(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, ".no-slop.yaml", "slop:\n  data_dir: .review-history\n  leak_scan:\n    blocklist_file: .noslop-blocklist\n  test_count_floor: true\n  risk:\n    single_review_threshold: 90\n    full_adversarial_threshold: 99\n")
	writeFile(t, dir, ".noslop-blocklist", "# intentionally empty\n")
	writeFile(t, dir, "calc_test.go", "package calc\nfunc TestPositive(t *testing.T) {}\nfunc TestNegative(t *testing.T) {}\n")
	runGit(t, dir, "add", ".no-slop.yaml", ".noslop-blocklist", "calc_test.go")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "feature/calculator")
	writeFile(t, dir, "calc_test.go", "package calc\nfunc TestPositive(t *testing.T) {}\n")
	runGit(t, dir, "add", "calc_test.go")
	runGit(t, dir, "commit", "-m", "remove test")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{
		"gate", "--repo", dir,
		"--provider", "provider-a", "--model", "model-a", "--reasoning-effort", "high",
		"--lane-id", "lane-a", "--change-class", "tests",
	}, &stdout, &stderr, slopcli.Options{})
	if exitCode != 1 {
		t.Fatalf("exit = %d, want blocking verdict\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}

	history, err := provenance.NewFileStore(filepath.Join(dir, ".review-history")).Window("lane-a", "model-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("history = %+v, want one record", history)
	}
	record := history[0]
	if record.Provider != "provider-a" || record.ReasoningEffort != "high" || record.ChangeClass != "tests" {
		t.Fatalf("record metadata = %+v", record)
	}
	if record.SelectedTier != "leak-scan-only" || record.Outcome != "fail" || record.Rounds != 0 || record.FixGrowth != 0 {
		t.Fatalf("record result = %+v", record)
	}
	findings := record.FindingsByLens["test-capitulation"]
	if len(findings.Accepted) != 1 || len(findings.Rejected) != 0 {
		t.Fatalf("recorded findings = %+v", record.FindingsByLens)
	}
}

// TestRunGateRecordsANoticeWithoutRatchetingProvenance is the mirror of the test
// above, at the branch point that actually routes a finding. The store-level
// proof hand-builds records that already carry Noticed, so it passes even if
// provenanceRecord still filed notices as accepted; only a gated run that
// produces a real notice proves the routing. A notice filed as accepted pins its
// record past retention forever and counts toward the lens escalation, which is
// the permanent penalty the non-blocking severity exists to remove.
func TestRunGateRecordsANoticeWithoutRatchetingProvenance(t *testing.T) {
	t.Parallel()

	upstream := t.TempDir()
	runGit(t, upstream, "init", "-b", "main")
	runGit(t, upstream, "config", "user.email", "test@example.com")
	runGit(t, upstream, "config", "user.name", "Test")
	writeFile(t, upstream, "lib.txt", "one\n")
	runGit(t, upstream, "add", "-A")
	runGit(t, upstream, "commit", "-m", "one")
	first := strings.TrimSpace(runGit(t, upstream, "rev-parse", "HEAD"))
	writeFile(t, upstream, "lib.txt", "two\n")
	runGit(t, upstream, "add", "-A")
	runGit(t, upstream, "commit", "-m", "two")
	second := strings.TrimSpace(runGit(t, upstream, "rev-parse", "HEAD"))

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, ".no-slop.yaml", "slop:\n  data_dir: .review-history\n  risk:\n    single_review_threshold: 90\n    full_adversarial_threshold: 99\n")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", upstream, "sub")
	runGit(t, dir, "-C", "sub", "checkout", "-q", first)
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "feature/bump")
	runGit(t, dir, "-C", "sub", "checkout", "-q", second)
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "bump submodule")

	var stdout, stderr bytes.Buffer
	slopcli.Run(context.Background(), []string{
		"gate", "--repo", dir,
		"--provider", "provider-a", "--model", "model-a", "--reasoning-effort", "high",
		"--lane-id", "lane-a", "--change-class", "deps",
	}, &stdout, &stderr, slopcli.Options{ReviewerFactory: noReviewer})
	output := stdout.String() + stderr.String()
	if !strings.Contains(output, "submodule-pointer-unscanned") {
		t.Fatalf("the run produced no submodule notice:\n%s", output)
	}

	history, err := provenance.NewFileStore(filepath.Join(dir, ".review-history")).Window("lane-a", "model-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("history = %+v, want one record", history)
	}
	findings := history[0].FindingsByLens["submodule-pointer-unscanned"]
	if len(findings.Accepted) != 0 {
		t.Errorf("a notice was recorded as an accepted finding, so it ratchets: %+v", findings)
	}
	if len(findings.Noticed) != 1 {
		t.Errorf("recorded notices = %+v, want the notice kept for visibility", findings)
	}
}

func TestRunGateConditionsDecisionOnConfiguredProvenanceStore(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, ".no-slop.yaml", "slop:\n  data_dir: .review-history\n  leak_scan:\n    blocklist_file: .noslop-blocklist\n")
	writeFile(t, dir, ".noslop-blocklist", "# intentionally empty\n")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", ".no-slop.yaml", ".noslop-blocklist", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "docs")

	store := provenance.NewFileStore(filepath.Join(dir, ".review-history"))
	for index := 0; index < 3; index++ {
		if err := store.Append(provenance.Record{
			Provider:        "provider-a",
			Model:           "model-a",
			ReasoningEffort: "high",
			AgentLaneID:     "lane-a",
			ChangeClass:     "tests",
			SelectedTier:    "single-review",
			FindingsByLens: map[string]provenance.LensFindings{
				"test-capitulation": {Accepted: []provenance.Finding{{Description: "test weakened"}}},
			},
			Rounds:  1,
			Outcome: "fail",
		}); err != nil {
			t.Fatal(err)
		}
	}

	reviewer := &emptyReviewer{}
	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{
		"gate", "--repo", dir,
		"--provider", "provider-a", "--model", "model-a", "--reasoning-effort", "high", "--lane-id", "lane-a",
	}, &stdout, &stderr, slopcli.Options{
		ReviewerFactory: func(context.Context, *config.Config, io.Writer) (engine.Reviewer, io.Closer, error) {
			return reviewer, nil, nil
		},
	})
	if exitCode != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	if reviewer.calls != 1 {
		t.Fatalf("reviewer calls = %d, want conditioned single review", reviewer.calls)
	}
	for _, want := range []string{
		"tier: single-review",
		"lane lane-a: 3 test-capitulation findings across 3 retained changes, escalating",
		"lens priority: test-capitulation",
		"deterministic probes: test-count-floor",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output missing %q:\n%s", want, stdout.String())
		}
	}
}

// TestRunGateRefusesToLowerAProvenanceEscalatedTier replaces an earlier test
// that pinned --force-tier as an accepted way to take the lower tier anyway.
// That escape hatch is gone: the flag is escalate-only now, so the property
// being proven changed and its old proof had to go with it. What is left is the
// half that was always right, plus the half --force-tier used to undo.
func TestRunGateRefusesToLowerAProvenanceEscalatedTier(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "docs")

	store := provenance.NewFileStore(filepath.Join(dir, ".review-history"))
	for index := 0; index < 3; index++ {
		if err := store.Append(provenance.Record{
			Provider:        "provider-a",
			Model:           "model-a",
			ReasoningEffort: "high",
			AgentLaneID:     "lane-a",
			ChangeClass:     "documentation",
			SelectedTier:    "single-review",
			FindingsByLens: map[string]provenance.LensFindings{
				"test-capitulation": {Accepted: []provenance.Finding{{Description: "test weakened"}}},
			},
			Rounds:  1,
			Outcome: "fail",
		}); err != nil {
			t.Fatal(err)
		}
	}

	baseArgs := []string{
		"gate", "--repo", dir, "--tier", "leak-scan-only",
		"--provider", "provider-a", "--model", "model-a", "--lane-id", "lane-a",
	}
	var refusedOut, refusedErr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), baseArgs, &refusedOut, &refusedErr, slopcli.Options{ProvenanceStore: store})
	if exitCode != 2 {
		t.Fatalf("exit = %d, want contradictory override refused\nstdout:\n%s\nstderr:\n%s", exitCode, refusedOut.String(), refusedErr.String())
	}
	for _, want := range []string{
		"tier: single-review",
		"provenance: lane lane-a: 3 test-capitulation findings across 3 retained changes, escalating",
		"override refused: single-review -> leak-scan-only",
	} {
		if !strings.Contains(refusedOut.String(), want) {
			t.Errorf("refusal output missing %q:\n%s", want, refusedOut.String())
		}
	}
	if !strings.Contains(refusedErr.String(), "this tier came from provenance escalation") {
		t.Fatalf("refusal error does not name the escalation:\n%s", refusedErr.String())
	}

	var forcedOut, forcedErr bytes.Buffer
	forcedArgs := append(append([]string(nil), baseArgs...), "--force-tier")
	exitCode = slopcli.Run(context.Background(), forcedArgs, &forcedOut, &forcedErr, slopcli.Options{ProvenanceStore: store})
	if exitCode != 2 {
		t.Fatalf("forced exit = %d, want --force-tier refused as well\nstdout:\n%s\nstderr:\n%s", exitCode, forcedOut.String(), forcedErr.String())
	}
	if strings.Contains(forcedOut.String(), "verdict: pass") {
		t.Fatalf("--force-tier cleared a live escalation and passed:\n%s", forcedOut.String())
	}
	for _, want := range []string{
		"tier: single-review",
		"override refused: single-review -> leak-scan-only",
	} {
		if !strings.Contains(forcedOut.String(), want) {
			t.Errorf("forced output missing %q:\n%s", want, forcedOut.String())
		}
	}
	if !strings.Contains(forcedErr.String(), "--force-tier no longer lowers a computed tier") {
		t.Fatalf("forced refusal does not say what the flag now does:\n%s", forcedErr.String())
	}
}

func TestRunGateTreatsSiblingSymbolSubstitutionAsChangedLogic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "flags.go", "package policy\n\nconst strictMode = true\nconst permissiveMode = false\n")
	writeFile(t, dir, "policy.go", "package policy\n\nfunc allowed(isAdmin bool) bool { return isAdmin && strictMode }\n")
	runGit(t, dir, "add", "flags.go", "policy.go")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "feature/policy")
	writeFile(t, dir, "policy.go", "package policy\n\nfunc allowed(isAdmin bool) bool { return isAdmin && permissiveMode }\n")
	runGit(t, dir, "add", "policy.go")
	runGit(t, dir, "commit", "-m", "change policy")

	reviewer := &emptyReviewer{}
	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir}, &stdout, &stderr, slopcli.Options{
		ReviewerFactory: func(context.Context, *config.Config, io.Writer) (engine.Reviewer, io.Closer, error) {
			return reviewer, nil, nil
		},
	})
	if exitCode != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	if reviewer.calls != 1 {
		t.Fatalf("reviewer calls = %d, want sibling substitution reviewed\n%s", reviewer.calls, stdout.String())
	}
	for _, want := range []string{"tier: single-review", "novelty: 2, existing source logic changed"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output missing %q:\n%s", want, stdout.String())
		}
	}
}

// TestRunGateWithholdsTheVerdictWhenProvenanceCannotBeRecorded replaces an
// earlier test that pinned the opposite order. Printing "verdict: fail" and
// then exiting 2 because the record could not be written left stdout and the
// exit code describing different runs. The checks that did complete still
// print, because withholding the verdict must not also withhold the evidence.
func TestRunGateWithholdsTheVerdictWhenProvenanceCannotBeRecorded(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, ".no-slop.yaml", cheapestTierConfig)
	writeFile(t, dir, "calc_test.go", "package calc\nfunc TestPositive(t *testing.T) {}\nfunc TestNegative(t *testing.T) {}\n")
	runGit(t, dir, "add", ".no-slop.yaml", "calc_test.go")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "test/remove-case")
	writeFile(t, dir, "calc_test.go", "package calc\nfunc TestPositive(t *testing.T) {}\n")
	runGit(t, dir, "add", "calc_test.go")
	runGit(t, dir, "commit", "-m", "remove test")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir}, &stdout, &stderr, slopcli.Options{ProvenanceStore: failingProvenanceStore{}})
	if exitCode != 2 {
		t.Fatalf("exit = %d, want bookkeeping failure\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	for _, want := range []string{"mandatory check: test-count floor completed (1 findings)", "finding: [test-capitulation]"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout missing completed check %q:\n%s", want, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "verdict:") {
		t.Errorf("stdout printed a verdict the run could not record:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "record provenance: write denied") {
		t.Fatalf("stderr does not report append failure:\n%s", stderr.String())
	}
}

func TestRunEvaluateComparesCapturedPolicyResults(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, root, "case-a/case.json", `{"schema_version":1,"id":"case-a","description":"fail-open","expected_findings":[{"lens":"fail-open-default","path":"policy.go","line":8}]}`)
	writeFile(t, root, "case-a/change.diff", "--- a/policy.go\n+++ b/policy.go\n@@ -8 +8 @@\n-return false\n+return true\n")
	writeFile(t, root, "unconditioned.json", `{"schema_version":1,"policy":"unconditioned","cases":[{"case_id":"case-a","findings":[]}]}`)
	writeFile(t, root, "conditioned.json", `{"schema_version":1,"policy":"conditioned","cases":[{"case_id":"case-a","findings":[{"lens":"fail-open-default","path":"policy.go","line":8}]}]}`)

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{
		"evaluate",
		"--corpus", root,
		"--unconditioned-results", filepath.Join(root, "unconditioned.json"),
		"--conditioned-results", filepath.Join(root, "conditioned.json"),
	}, &stdout, &stderr, slopcli.Options{})
	if exitCode != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"unconditioned: found 0, missed 1, false-positive 0",
		"conditioned: found 1, missed 0, false-positive 0",
		"delta: found +1, missed -1, false-positive +0",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestRunEvaluateAttributesReplayedResultSources(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	unconditionedPath := filepath.Join(root, "unconditioned.json")
	conditionedPath := filepath.Join(root, "conditioned.json")
	writeFile(t, root, "case-a/case.json", `{"schema_version":1,"id":"case-a","description":"fail-open","expected_findings":[{"lens":"fail-open-default","path":"policy.go","line":8}]}`)
	writeFile(t, root, "case-a/change.diff", "--- a/policy.go\n+++ b/policy.go\n@@ -8 +8 @@\n-return false\n+return true\n")
	writeFile(t, root, "unconditioned.json", `{"schema_version":1,"policy":"baseline-policy","cases":[{"case_id":"case-a","findings":[]}]}`)
	writeFile(t, root, "conditioned.json", `{"schema_version":1,"policy":"history-policy","cases":[{"case_id":"case-a","findings":[{"lens":"fail-open-default","path":"policy.go","line":8}]}]}`)

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{
		"evaluate",
		"--corpus", root,
		"--unconditioned-results", unconditionedPath,
		"--conditioned-results", conditionedPath,
	}, &stdout, &stderr, slopcli.Options{})
	if exitCode != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"corpus: synthetic replay cases from " + root,
		"results: replayed captures, not produced by this run",
		`unconditioned policy "baseline-policy" from ` + unconditionedPath,
		`conditioned policy "history-policy" from ` + conditionedPath,
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestRunEvaluateUsesExplicitHistoricalCaseSet(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	caseJSON := `{"schema_version":1,"id":"case-a","description":"original","expected_findings":[]}`
	caseDiff := "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n"
	writeFile(t, root, "case-a/case.json", caseJSON)
	writeFile(t, root, "case-a/change.diff", caseDiff)
	writeFile(t, root, "case-b/case.json", `{"schema_version":1,"id":"case-b","description":"later","expected_findings":[]}`)
	writeFile(t, root, "case-b/change.diff", "--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n-old\n+new\n")
	digest := caseSetDigest("case-a", []byte(caseJSON), []byte(caseDiff))
	writeFile(t, root, "historical.json", fmt.Sprintf(`{"schema_version":1,"name":"historical","content_sha256":"%x","case_ids":["case-a"]}`, digest))
	writeFile(t, root, "unconditioned.json", `{"schema_version":1,"policy":"unconditioned","cases":[{"case_id":"case-a","findings":[]}]}`)
	writeFile(t, root, "conditioned.json", `{"schema_version":1,"policy":"conditioned","cases":[{"case_id":"case-a","findings":[]}]}`)

	args := []string{
		"evaluate",
		"--corpus", root,
		"--unconditioned-results", filepath.Join(root, "unconditioned.json"),
		"--conditioned-results", filepath.Join(root, "conditioned.json"),
	}
	var stdout, stderr bytes.Buffer
	if exitCode := slopcli.Run(context.Background(), args, &stdout, &stderr, slopcli.Options{}); exitCode != 2 || !strings.Contains(stderr.String(), `results are missing case "case-b"`) {
		t.Fatalf("unselected exit = %d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}

	args = append(args, "--case-set", filepath.Join(root, "historical.json"))
	stdout.Reset()
	stderr.Reset()
	if exitCode := slopcli.Run(context.Background(), args, &stdout, &stderr, slopcli.Options{}); exitCode != 0 {
		t.Fatalf("selected exit = %d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
}

func caseSetDigest(id string, caseJSON, diff []byte) [sha256.Size]byte {
	var snapshot bytes.Buffer
	writeDigestFrame(&snapshot, caseJSON)
	writeDigestFrame(&snapshot, diff)
	var aggregate bytes.Buffer
	writeDigestFrame(&aggregate, []byte(id))
	writeDigestFrame(&aggregate, snapshot.Bytes())
	return sha256.Sum256(aggregate.Bytes())
}

func writeDigestFrame(buffer *bytes.Buffer, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	buffer.Write(size[:])
	buffer.Write(value)
}

// attachRemote gives a probe repository a real remote holding the current main,
// because that is now the only thing the standalone gate will read a canonical
// base from. `git ls-remote` works over a filesystem path, so this needs no
// network and stays a local, deterministic test.
//
// Before round 4 these tests passed `--base <sha>` and never exercised base
// resolution at all. The flag is gone: a base the caller names is a gate the
// caller configures.
func attachRemote(t *testing.T, dir string) string {
	t.Helper()
	remote := t.TempDir()
	runGit(t, remote, "init", "--bare", "-b", "main")
	runGit(t, dir, "remote", "add", "origin", remote)
	runGit(t, dir, "push", "-q", "origin", "main")
	return remote
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
