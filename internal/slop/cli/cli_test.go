package cli_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/config"
	slopcli "github.com/kunchenguid/no-mistakes/internal/slop/cli"
	"github.com/kunchenguid/no-mistakes/internal/slop/engine"
	"github.com/kunchenguid/no-mistakes/internal/slop/provenance"
)

type emptyReviewer struct{ calls int }

func (r *emptyReviewer) Review(context.Context, engine.ReviewRequest) ([]engine.Finding, error) {
	r.calls++
	return nil, nil
}

type failingProvenanceStore struct{}

func (failingProvenanceStore) Recent(string, string, int) ([]provenance.Record, error) {
	return nil, nil
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
	base := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "docs")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir, "--base", base, "--tier", "leak-scan-only"}, &stdout, &stderr, slopcli.Options{})
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
		"verdict: pass",
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
	base := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "docs")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir, "--base", base}, &stdout, &stderr, slopcli.Options{})
	if exitCode != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"mandatory check: lens pre-check completed (0 findings)",
		"mandatory check: leak scan completed (0 findings)",
		"mandatory check: test-count floor completed (0 findings)",
		"mandatory check: prose oracle completed (0 findings)",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestRunGatePrintsOverrideAndStillBlocksLeak(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, ".noslop-blocklist", "# intentionally empty\n")
	writeFile(t, dir, "policy.go", "package policy\n")
	runGit(t, dir, "add", ".noslop-blocklist", "policy.go")
	runGit(t, dir, "commit", "-m", "initial")
	base := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "-c", "feature/policy")
	writeFile(t, dir, "policy.go", "package policy\n\nconst token = \"ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ\"\n") // noslop:allow-leak
	runGit(t, dir, "add", "policy.go")
	runGit(t, dir, "commit", "-m", "change")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir, "--base", base, "--tier", "leak-scan-only"}, &stdout, &stderr, slopcli.Options{})
	if exitCode != 1 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"tier: leak-scan-only",
		"override: single-review -> leak-scan-only",
		"finding: [leak-identity-scan] policy.go:3",
		"verdict: fail",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output missing %q:\n%s", want, stdout.String())
		}
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
	base := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "docs")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir, "--base", base}, &stdout, &stderr, slopcli.Options{})
	if exitCode != 0 {
		t.Fatalf("exit = %d, want clean gate without a default blocklist\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	if want := "leak scan: no private-name blocklist (default path .noslop-blocklist not present)"; !strings.Contains(stdout.String(), want) {
		t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
	}
}

func TestRunGateReportsEveryHonoredLeakExemption(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	base := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "-c", "test/leak-fixtures")
	writeFile(t, dir, "fixtures/tokens.txt", "TOKEN=ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ # noslop:allow-leak\nAWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE # noslop:allow-leak\n") // noslop:allow-leak
	runGit(t, dir, "add", "fixtures/tokens.txt")
	runGit(t, dir, "commit", "-m", "add fixtures")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir, "--base", base, "--tier", "leak-scan-only"}, &stdout, &stderr, slopcli.Options{})
	if exitCode != 0 {
		t.Fatalf("exit = %d, want exemptions honored\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"leak exemption: fixtures/tokens.txt:1: noslop:allow-leak",
		"leak exemption: fixtures/tokens.txt:2: noslop:allow-leak",
		"leak scan: 2 leak exemptions honored",
		"verdict: pass",
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
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	base := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "-c", "test/leak-fixture")
	writeFile(t, dir, "fixtures/token.txt", "TOKEN=ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ # noslop:allow-leak\n") // noslop:allow-leak
	runGit(t, dir, "add", "fixtures/token.txt")
	runGit(t, dir, "commit", "-m", "add fixture")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir, "--base", base}, &stdout, &stderr, slopcli.Options{
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
	writeFile(t, dir, ".no-mistakes.yaml", "slop:\n  leak_scan:\n    allow_exemptions: false\n")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", ".no-mistakes.yaml", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	base := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "-c", "test/leak-fixtures")
	writeFile(t, dir, "fixtures/tokens.txt", "TOKEN=ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ # noslop:allow-leak\n") // noslop:allow-leak
	runGit(t, dir, "add", "fixtures/tokens.txt")
	runGit(t, dir, "commit", "-m", "add fixture")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir, "--base", base, "--tier", "leak-scan-only"}, &stdout, &stderr, slopcli.Options{})
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
	base := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "docs")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir, "--base", base, "--blocklist", "missing-private-names"}, &stdout, &stderr, slopcli.Options{})
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
	writeFile(t, dir, ".no-mistakes.yaml", "slop:\n  leak_scan:\n    blocklist_file: missing-private-names\n")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", ".no-mistakes.yaml", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	base := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "-c", "docs/readme")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "docs")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir, "--base", base}, &stdout, &stderr, slopcli.Options{})
	if exitCode != 2 {
		t.Fatalf("exit = %d, want evaluation failure\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "read private-name blocklist") {
		t.Fatalf("stderr does not name configured blocklist failure: %s", stderr.String())
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
				writeFile(t, dir, ".no-mistakes.yaml", tc.config)
			}
			if err := os.Mkdir(filepath.Join(dir, tc.blocklist), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, dir, "README.md", "# Project\n")
			runGit(t, dir, "add", "README.md")
			if tc.config != "" {
				runGit(t, dir, "add", ".no-mistakes.yaml")
			}
			runGit(t, dir, "commit", "-m", "initial")
			base := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
			runGit(t, dir, "switch", "-c", "docs/readme")
			writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
			runGit(t, dir, "add", "README.md")
			runGit(t, dir, "commit", "-m", "docs")

			var stdout, stderr bytes.Buffer
			exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir, "--base", base}, &stdout, &stderr, slopcli.Options{})
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
	writeFile(t, dir, ".no-mistakes.yaml", "slop:\n  data_dir: .review-history\n  leak_scan:\n    blocklist_file: .noslop-blocklist\n  test_count_floor: true\n")
	writeFile(t, dir, ".noslop-blocklist", "# intentionally empty\n")
	writeFile(t, dir, "calc_test.go", "package calc\nfunc TestPositive(t *testing.T) {}\nfunc TestNegative(t *testing.T) {}\n")
	runGit(t, dir, "add", ".no-mistakes.yaml", ".noslop-blocklist", "calc_test.go")
	runGit(t, dir, "commit", "-m", "initial")
	base := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "-c", "feature/calculator")
	writeFile(t, dir, "calc_test.go", "package calc\nfunc TestPositive(t *testing.T) {}\n")
	runGit(t, dir, "add", "calc_test.go")
	runGit(t, dir, "commit", "-m", "remove test")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{
		"gate", "--repo", dir, "--base", base, "--tier", "leak-scan-only",
		"--provider", "provider-a", "--model", "model-a", "--reasoning-effort", "high",
		"--lane-id", "lane-a", "--change-class", "tests",
	}, &stdout, &stderr, slopcli.Options{})
	if exitCode != 1 {
		t.Fatalf("exit = %d, want blocking verdict\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}

	history, err := provenance.NewFileStore(filepath.Join(dir, ".review-history")).Recent("lane-a", "model-a", 10)
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

func TestRunGateConditionsDecisionOnConfiguredProvenanceStore(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, ".no-mistakes.yaml", "slop:\n  data_dir: .review-history\n  leak_scan:\n    blocklist_file: .noslop-blocklist\n")
	writeFile(t, dir, ".noslop-blocklist", "# intentionally empty\n")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", ".no-mistakes.yaml", ".noslop-blocklist", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	base := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
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
		"gate", "--repo", dir, "--base", base,
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
		"lane lane-a: 3 test-capitulation findings in last 3 changes, escalating",
		"lens priority: test-capitulation",
		"deterministic probes: test-count-floor",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestRunGateRefusesTierOverrideThatContradictsProvenanceUnlessForced(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	base := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
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
		"gate", "--repo", dir, "--base", base, "--tier", "leak-scan-only",
		"--provider", "provider-a", "--model", "model-a", "--lane-id", "lane-a",
	}
	var refusedOut, refusedErr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), baseArgs, &refusedOut, &refusedErr, slopcli.Options{ProvenanceStore: store})
	if exitCode != 2 {
		t.Fatalf("exit = %d, want contradictory override refused\nstdout:\n%s\nstderr:\n%s", exitCode, refusedOut.String(), refusedErr.String())
	}
	for _, want := range []string{
		"tier: single-review",
		"provenance: lane lane-a: 3 test-capitulation findings in last 3 changes, escalating",
		"override refused: single-review -> leak-scan-only",
	} {
		if !strings.Contains(refusedOut.String(), want) {
			t.Errorf("refusal output missing %q:\n%s", want, refusedOut.String())
		}
	}
	if !strings.Contains(refusedErr.String(), "use --force-tier to accept the lower tier") {
		t.Fatalf("refusal error does not explain force flag:\n%s", refusedErr.String())
	}

	var forcedOut, forcedErr bytes.Buffer
	forcedArgs := append(append([]string(nil), baseArgs...), "--force-tier")
	exitCode = slopcli.Run(context.Background(), forcedArgs, &forcedOut, &forcedErr, slopcli.Options{ProvenanceStore: store})
	if exitCode != 0 {
		t.Fatalf("forced exit = %d, want explicit lower tier accepted\nstdout:\n%s\nstderr:\n%s", exitCode, forcedOut.String(), forcedErr.String())
	}
	for _, want := range []string{
		"tier: leak-scan-only",
		"provenance: lane lane-a: 3 test-capitulation findings",
		"override forced: single-review -> leak-scan-only",
	} {
		if !strings.Contains(forcedOut.String(), want) {
			t.Errorf("forced output missing %q:\n%s", want, forcedOut.String())
		}
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
	base := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "-c", "feature/policy")
	writeFile(t, dir, "policy.go", "package policy\n\nfunc allowed(isAdmin bool) bool { return isAdmin && permissiveMode }\n")
	runGit(t, dir, "add", "policy.go")
	runGit(t, dir, "commit", "-m", "change policy")

	reviewer := &emptyReviewer{}
	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir, "--base", base}, &stdout, &stderr, slopcli.Options{
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

func TestRunGatePrintsVerdictBeforeReportingProvenanceAppendFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "calc_test.go", "package calc\nfunc TestPositive(t *testing.T) {}\nfunc TestNegative(t *testing.T) {}\n")
	runGit(t, dir, "add", "calc_test.go")
	runGit(t, dir, "commit", "-m", "initial")
	base := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "-c", "test/remove-case")
	writeFile(t, dir, "calc_test.go", "package calc\nfunc TestPositive(t *testing.T) {}\n")
	runGit(t, dir, "add", "calc_test.go")
	runGit(t, dir, "commit", "-m", "remove test")

	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir, "--base", base, "--tier", "leak-scan-only"}, &stdout, &stderr, slopcli.Options{ProvenanceStore: failingProvenanceStore{}})
	if exitCode != 2 {
		t.Fatalf("exit = %d, want bookkeeping failure\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}
	for _, want := range []string{"finding: [test-capitulation]", "verdict: fail"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout missing completed result %q:\n%s", want, stdout.String())
		}
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
	writeFile(t, root, "case-a/case.json", `{"schema_version":1,"id":"case-a","description":"original","expected_findings":[]}`)
	writeFile(t, root, "case-a/change.diff", "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n")
	writeFile(t, root, "case-b/case.json", `{"schema_version":1,"id":"case-b","description":"later","expected_findings":[]}`)
	writeFile(t, root, "case-b/change.diff", "--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n-old\n+new\n")
	writeFile(t, root, "historical.json", `{"schema_version":1,"name":"historical","case_ids":["case-a"]}`)
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
