package engine_test

import (
	"context"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/slop/engine"
	"github.com/Blakeolson21/no-slop/internal/slop/provenance"
	"github.com/Blakeolson21/no-slop/internal/slop/risk"
)

type countingReviewer struct {
	calls    int
	requests []engine.ReviewRequest
}

func (r *countingReviewer) Review(_ context.Context, request engine.ReviewRequest) ([]engine.Finding, error) {
	r.calls++
	r.requests = append(r.requests, request)
	if r.calls == 1 {
		return []engine.Finding{{Lens: "fail-open-default", Path: "policy.go", Line: 8, Description: "unknown becomes allow"}}, nil
	}
	return nil, nil
}

type countingTests struct{ calls int }

func (r *countingTests) Run(context.Context, string, string) (engine.TestResult, error) {
	r.calls++
	return engine.TestResult{ExitCode: 0}, nil
}

type duplicateMechanicalReviewer struct{}

func (duplicateMechanicalReviewer) Review(context.Context, engine.ReviewRequest) ([]engine.Finding, error) {
	return []engine.Finding{{
		Lens:        "vacuous-check",
		Path:        "guard.go",
		Line:        1,
		Description: "reviewer phrased the same source finding differently",
	}}, nil
}

type staticHistory []provenance.Record

func (h staticHistory) Window(string, string) ([]provenance.Record, error) {
	return h, nil
}

func (h staticHistory) HasIdentifiedHistory() (bool, error) {
	return len(h) > 0, nil
}

func TestRunRoutesMarkdownToMandatoryChecksWithoutReviewerOrTests(t *testing.T) {
	t.Parallel()

	reviewer := &countingReviewer{}
	tests := &countingTests{}
	result, err := engine.Run(context.Background(), engine.Input{
		WorkDir:       t.TempDir(),
		Branch:        "docs/readme",
		DefaultBranch: "main",
		Files: []engine.Change{{
			Path:           "README.md",
			Status:         risk.Modified,
			Added:          1,
			Deleted:        1,
			AddedContent:   "A plain engineer note.\n",
			CurrentContent: "A plain engineer note.\n",
		}},
		Config: engine.Config{OutboundPaths: []string{"outbound/**"}, TestCountFloor: true},
	}, engine.Dependencies{Reviewer: reviewer, Tests: tests})
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision.Tier != risk.TierLeakScanOnly {
		t.Fatalf("tier = %q, want %q", result.Decision.Tier, risk.TierLeakScanOnly)
	}
	if reviewer.calls != 0 || tests.calls != 0 {
		t.Fatalf("review calls = %d, test calls = %d, want none", reviewer.calls, tests.calls)
	}
	if !result.Passed {
		t.Fatalf("clean Markdown result = %+v, want pass", result)
	}
}

func TestRunRoutesOrdinarySourceToSingleReview(t *testing.T) {
	t.Parallel()

	reviewer := &countingReviewer{}
	tests := &countingTests{}
	result, err := engine.Run(context.Background(), engine.Input{
		WorkDir:       t.TempDir(),
		Branch:        "feature/cache",
		DefaultBranch: "main",
		Files: []engine.Change{{
			Path:           "cache.go",
			Status:         risk.Modified,
			Added:          12,
			Deleted:        4,
			AddedContent:   "func cacheKey() string { return \"v2\" }\n",
			CurrentContent: "package cache\nfunc cacheKey() string { return \"v2\" }\n",
		}},
		Config: engine.Config{TestCountFloor: true},
	}, engine.Dependencies{Reviewer: reviewer, Tests: tests})
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision.Tier != risk.TierSingleReview || reviewer.calls != 1 || result.ReviewRounds != 1 || tests.calls != 0 {
		t.Fatalf("result = %+v, review calls = %d, test calls = %d", result, reviewer.calls, tests.calls)
	}
}

func TestRunFullTierRequiresReviewAndRunsConfiguredTests(t *testing.T) {
	t.Parallel()

	reviewer := &countingReviewer{}
	tests := &countingTests{}
	result, err := engine.Run(context.Background(), engine.Input{
		WorkDir:       t.TempDir(),
		Branch:        "feature/policy",
		DefaultBranch: "main",
		Intent:        "Make policy reads fail closed.",
		Files: []engine.Change{{
			Path:           "internal/auth/policy.go",
			Status:         risk.Added,
			Added:          100,
			AddedContent:   "package auth\n",
			CurrentContent: "package auth\n",
		}},
		Config: engine.Config{TestCountFloor: true, TestCommand: "go test ./..."},
	}, engine.Dependencies{Reviewer: reviewer, Tests: tests})
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision.Tier != risk.TierFullAdversarial || reviewer.calls != 2 || result.ReviewRounds != 2 || tests.calls != 1 {
		t.Fatalf("result = %+v, review calls = %d, test calls = %d", result, reviewer.calls, tests.calls)
	}
	if reviewer.requests[0].Round != engine.ReviewRoundLensReview || reviewer.requests[1].Round != engine.ReviewRoundAdversarialChallenge {
		t.Fatalf("review rounds = %+v", reviewer.requests)
	}
	if len(reviewer.requests[1].PriorFindings) != 1 || reviewer.requests[1].PriorFindings[0].Lens != "fail-open-default" {
		t.Fatalf("second round prior findings = %+v", reviewer.requests[1].PriorFindings)
	}
	if reviewer.requests[0].Intent != "Make policy reads fail closed." {
		t.Fatalf("reviewer request intent = %q", reviewer.requests[0].Intent)
	}
}

func TestRunLensPrecheckStillRunsUnderLightOverride(t *testing.T) {
	t.Parallel()

	result, err := engine.Run(context.Background(), engine.Input{
		WorkDir:       t.TempDir(),
		Branch:        "feature/guard",
		DefaultBranch: "main",
		Files: []engine.Change{{
			Path:            "guard.go",
			Status:          risk.Modified,
			AddedContent:    "if observed != observed {}\n",
			BaselineContent: "if observed != expected {}\n",
			CurrentContent:  "if observed != observed {}\n",
		}},
		Config: engine.Config{Risk: cheapestTierThresholds()},
	}, engine.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed || len(result.Findings) != 1 || result.Findings[0].Lens != "vacuous-check" {
		t.Fatalf("result = %+v, want mandatory vacuous-check finding", result)
	}
	if check := mandatoryCheck(t, result, "lens pre-check"); !check.Enabled || check.Findings != 1 {
		t.Fatalf("lens pre-check = %+v, want enabled with 1 finding", check)
	}
}

// cheapestTierThresholds routes a probe to the lightest tier the way an
// operator legitimately can, through thresholds read from the base ref. These
// tests used `TierOverride: leak-scan-only` for the same purpose, and a tier
// override may only raise now: the flag was a gate-strength control the author
// of the change could set, which is what carried both invariant classes to a
// passing verdict at exit 0.
func cheapestTierThresholds() risk.Config {
	return risk.Config{SingleReviewThreshold: 90, FullReviewThreshold: 99}
}

func mandatoryCheck(t *testing.T, result engine.Result, name string) engine.MandatoryCheck {
	t.Helper()
	for _, check := range result.MandatoryChecks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("mandatory check %q missing from %+v", name, result.MandatoryChecks)
	return engine.MandatoryCheck{}
}

func TestRunRedundantCommentCheckUsesOnlyAddedCommentsAtLightestTier(t *testing.T) {
	t.Parallel()

	result, err := engine.Run(context.Background(), engine.Input{
		WorkDir:       t.TempDir(),
		Branch:        "feature/comments",
		DefaultBranch: "main",
		Files: []engine.Change{
			{
				Path:           "counter.go",
				Status:         risk.Modified,
				AddedContent:   "\n// increment i\n\n\n",
				CurrentContent: "func advance(i int) int {\n\t// increment i\n\ti += 1\n\treturn i\n}\n",
			},
			{
				Path:           "legacy.go",
				Status:         risk.Modified,
				AddedContent:   "\nreturn value\n",
				CurrentContent: "// return value\nreturn value\n",
			},
		},
		Config: engine.Config{Risk: cheapestTierThresholds()},
	}, engine.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed || len(result.Findings) != 1 || result.Findings[0].Lens != "redundant-comment" || result.Findings[0].Path != "counter.go" {
		t.Fatalf("result = %+v, want only the added redundant comment", result)
	}
}

func TestRunDeduplicatesReviewerFindingAlreadyCaughtMechanically(t *testing.T) {
	t.Parallel()

	result, err := engine.Run(context.Background(), engine.Input{
		WorkDir:       t.TempDir(),
		Branch:        "feature/guard",
		DefaultBranch: "main",
		Files: []engine.Change{{
			Path:            "guard.go",
			Status:          risk.Modified,
			Added:           10,
			AddedContent:    "if observed != observed {}\n",
			BaselineContent: "if observed != expected {}\n",
			CurrentContent:  "if observed != observed {}\n",
		}},
		Config: engine.Config{TierOverride: risk.TierSingleReview},
	}, engine.Dependencies{Reviewer: duplicateMechanicalReviewer{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 1 || result.Findings[0].Description == "reviewer phrased the same source finding differently" {
		t.Fatalf("findings = %+v, want one mechanical finding", result.Findings)
	}
}

func TestRunMandatoryLeakScanStillRunsUnderLightOverride(t *testing.T) {
	t.Parallel()

	result, err := engine.Run(context.Background(), engine.Input{
		WorkDir:       t.TempDir(),
		Branch:        "feature/policy",
		DefaultBranch: "main",
		Files: []engine.Change{{
			Path:           "internal/auth/policy.go",
			Status:         risk.Added,
			Added:          100,
			AddedContent:   "token=ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ\n", // noslop:allow-leak
			CurrentContent: "package auth\n",
		}},
		Config: engine.Config{Risk: cheapestTierThresholds()},
	}, engine.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision.Tier != risk.TierLeakScanOnly {
		t.Fatalf("decision = %+v", result.Decision)
	}
	if result.Passed || len(result.Findings) != 1 || result.Findings[0].Lens != "leak-identity-scan" {
		t.Fatalf("result = %+v, want mandatory leak finding", result)
	}
}

func TestRunTestCountFloorStillRunsUnderLightOverride(t *testing.T) {
	t.Parallel()

	result, err := engine.Run(context.Background(), engine.Input{
		WorkDir:       t.TempDir(),
		Branch:        "feature/calculator",
		DefaultBranch: "main",
		Files: []engine.Change{{
			Path:            "calc_test.go",
			Status:          risk.Modified,
			Added:           0,
			Deleted:         4,
			BaselineContent: "package calc\nfunc TestPositive(t *testing.T) {}\nfunc TestNegative(t *testing.T) {}\n",
			CurrentContent:  "package calc\nfunc TestPositive(t *testing.T) {}\n",
		}},
		Config: engine.Config{TestCountFloor: true, Risk: cheapestTierThresholds()},
	}, engine.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed || len(result.Findings) != 1 || result.Findings[0].Lens != "test-capitulation" {
		t.Fatalf("result = %+v, want mandatory test-count finding", result)
	}
}

func TestRunTestCountFloorUsesBaselinePathForModifiedRename(t *testing.T) {
	t.Parallel()

	result, err := engine.Run(context.Background(), engine.Input{
		WorkDir:       t.TempDir(),
		Branch:        "refactor/fixtures",
		DefaultBranch: "main",
		Files: []engine.Change{{
			Path:            "fixtures/widget.txt",
			BaselinePath:    "widget_test.go",
			Status:          risk.Modified,
			Added:           1,
			Deleted:         1,
			BaselineContent: "package widget\nfunc TestWidget(t *testing.T) {}\n",
			CurrentContent:  "widget fixture\n",
		}},
		Config: engine.Config{TestCountFloor: true, Risk: cheapestTierThresholds()},
	}, engine.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed || len(result.Findings) != 1 || result.Findings[0].Lens != "test-capitulation" {
		t.Fatalf("result = %+v, want renamed test removal detected", result)
	}
}

func TestRunScopeExpansionDetectsDestinationEnteringRuntime(t *testing.T) {
	t.Parallel()

	result, err := engine.Run(context.Background(), engine.Input{
		WorkDir:       t.TempDir(),
		Branch:        "docs/server",
		DefaultBranch: "main",
		Intent:        "Move the example without adding runtime behavior.",
		Files: []engine.Change{{
			Path:            "internal/server.go",
			BaselinePath:    "docs/server.go",
			Status:          risk.Modified,
			Added:           1,
			Deleted:         1,
			AddedContent:    "package internal\n",
			BaselineContent: "package example\n",
			CurrentContent:  "package internal\n",
		}},
		Config: engine.Config{Risk: cheapestTierThresholds()},
	}, engine.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed || len(result.Findings) != 1 || result.Findings[0].Lens != "scope-expansion" {
		t.Fatalf("result = %+v, want runtime scope expansion", result)
	}
}

func TestRunConditionedDeterministicProbeRunsWhenConfiguredFloorIsOff(t *testing.T) {
	t.Parallel()

	history := make(staticHistory, 3)
	for index := range history {
		history[index] = provenance.Record{FindingsByLens: map[string]provenance.LensFindings{
			"test-capitulation": {Accepted: []provenance.Finding{{Description: "test weakened"}}},
		}}
	}
	reviewer := &countingReviewer{}
	tests := &countingTests{}
	result, err := engine.Run(context.Background(), engine.Input{
		WorkDir:       t.TempDir(),
		Branch:        "feature/calculator",
		DefaultBranch: "main",
		Files: []engine.Change{{
			Path:            "calc_test.go",
			Status:          risk.Modified,
			Deleted:         4,
			BaselineContent: "package calc\nfunc TestPositive(t *testing.T) {}\nfunc TestNegative(t *testing.T) {}\n",
			CurrentContent:  "package calc\nfunc TestPositive(t *testing.T) {}\n",
		}},
		Config: engine.Config{
			Risk:           risk.Config{ProvenanceStore: history, AgentLaneID: "lane-a", Model: "model-a"},
			TestCountFloor: false,
			TestCommand:    "go test ./...",
		},
	}, engine.Dependencies{Reviewer: reviewer, Tests: tests})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, finding := range result.Findings {
		if finding.Lens == "test-capitulation" {
			found = true
		}
	}
	if !found {
		t.Fatalf("findings = %+v, want history-selected test-count probe", result.Findings)
	}
}
