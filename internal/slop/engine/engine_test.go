package engine_test

import (
	"context"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/slop/engine"
	"github.com/kunchenguid/no-mistakes/internal/slop/risk"
)

type countingReviewer struct{ calls int }

func (r *countingReviewer) Review(context.Context, engine.ReviewRequest) ([]engine.Finding, error) {
	r.calls++
	return nil, nil
}

type countingTests struct{ calls int }

func (r *countingTests) Run(context.Context, string, string) (engine.TestResult, error) {
	r.calls++
	return engine.TestResult{ExitCode: 0}, nil
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
	if result.Decision.Tier != risk.TierSingleReview || reviewer.calls != 1 || tests.calls != 0 {
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
	if result.Decision.Tier != risk.TierFullAdversarial || reviewer.calls != 1 || tests.calls != 1 {
		t.Fatalf("result = %+v, review calls = %d, test calls = %d", result, reviewer.calls, tests.calls)
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
			AddedContent:   "token=ghp_" + "abcdefghijklmnopqrstuvwxyzABCDEFGHIJ\n",
			CurrentContent: "package auth\n",
		}},
		Config: engine.Config{TierOverride: risk.TierLeakScanOnly},
	}, engine.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision.Tier != risk.TierLeakScanOnly || !result.Decision.Overridden {
		t.Fatalf("decision = %+v", result.Decision)
	}
	if result.Passed || len(result.Findings) != 1 || result.Findings[0].Lens != "leak-identity-scan" {
		t.Fatalf("result = %+v, want mandatory leak finding", result)
	}
}
