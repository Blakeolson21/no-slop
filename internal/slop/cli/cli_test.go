package cli_test

import (
	"bytes"
	"context"
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
	exitCode := slopcli.Run(context.Background(), []string{"gate", "--repo", dir, "--base", base}, &stdout, &stderr, slopcli.Options{})
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
