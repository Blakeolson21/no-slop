package cli_test

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/config"
	slopcli "github.com/Blakeolson21/no-slop/internal/slop/cli"
	"github.com/Blakeolson21/no-slop/internal/slop/engine"
	"github.com/Blakeolson21/no-slop/internal/slop/provenance"
)

// A reviewed clean pass clears a lens score only when the change it passed on
// could have contained that lens's defect, and the content kinds it judged are
// derived from the changed paths. The derivation asked the directory before the
// content type, so every markdown file filed under tests/ counted as test
// content: a documentation-only pass over tests/README.md cleared an
// accumulated test-capitulation escalation while containing no tests at all,
// which is the exact failure the derivation replaced a self-asserted change
// class to prevent.

const judgedContentConfig = "slop:\n  data_dir: .review-history\n  test_command: \"true\"\n"

const judgedContentSeedLens = "test-capitulation"

type passingTestRunner struct{}

func (passingTestRunner) Run(context.Context, string, string) (engine.TestResult, error) {
	return engine.TestResult{Command: "true", ExitCode: 0, Output: "ok\n"}, nil
}

func judgedContentRepo(t *testing.T, changedPath, changedContent string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, ".no-slop.yaml", judgedContentConfig)
	writeFile(t, dir, "README.md", "# Project\n")
	writeFile(t, dir, "tests/README.md", "# Test suite\n")
	writeFile(t, dir, "tests/helper_test.go", "package tests\n\nimport \"testing\"\n\nfunc TestHelper(t *testing.T) {\n\tif len(\"a\") != 1 {\n\t\tt.Fatal(\"length\")\n\t}\n}\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "change/under-tests")
	writeFile(t, dir, changedPath, changedContent)
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "change")
	return dir
}

func seedLensEscalation(t *testing.T, dir string) {
	t.Helper()
	store := provenance.NewFileStore(filepath.Join(dir, ".review-history"))
	for index := 0; index < 3; index++ {
		if err := store.Append(provenance.Record{
			Provider:        "provider-a",
			Model:           "model-a",
			ReasoningEffort: "high",
			AgentLaneID:     "lane-a",
			ChangeID:        string(rune('a' + index)),
			SelectedTier:    "single-review",
			FindingsByLens: map[string]provenance.LensFindings{
				judgedContentSeedLens: {Accepted: []provenance.Finding{{Description: "test weakened"}}},
			},
			Rounds:  1,
			Outcome: "fail",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func runReviewedPass(t *testing.T, dir string) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := slopcli.Run(context.Background(), []string{
		"gate", "--repo", dir, "--tier", "full-adversarial",
		"--provider", "provider-a", "--model", "model-a", "--reasoning-effort", "high", "--lane-id", "lane-a",
	}, &stdout, &stderr, slopcli.Options{
		ReviewerFactory: func(context.Context, *config.Config, io.Writer) (engine.Reviewer, io.Closer, error) {
			return &emptyReviewer{}, nil, nil
		},
		TestRunner: passingTestRunner{},
	})
	return code, stdout.String() + stderr.String()
}

func lensScoreAfterRun(t *testing.T, dir string) (int, []provenance.Record) {
	t.Helper()
	window, err := provenance.NewFileStore(filepath.Join(dir, ".review-history")).Window("lane-a", "model-a")
	if err != nil {
		t.Fatal(err)
	}
	return provenance.LensScores(window)[judgedContentSeedLens], window
}

// TestAReviewedPassOnTestDirectoryProseDoesNotClearATestLens is the probe. The
// pass is genuine and the file lives under tests/; it is still prose, and prose
// is no evidence about how this lane writes tests. Markdown was the first
// spelling of this and it is not the only one: every file kind the classifier
// already knows carries no executable behavior reaches the same clearing route
// through the same directory arm, so the location alone can never decide it.
func TestAReviewedPassOnTestDirectoryProseDoesNotClearATestLens(t *testing.T) {
	t.Parallel()

	for _, probe := range []struct {
		path    string
		content string
	}{
		{"tests/README.md", "# Test suite\n\nHow to run the suite locally.\n"},
		{"tests/notes.txt", "run the suite before pushing\nkeep fixtures small\n"},
		{"tests/schema.yaml", "fixtures:\n  - name: minimal\n    rows: 3\n"},
	} {
		t.Run(probe.path, func(t *testing.T) {
			t.Parallel()

			dir := judgedContentRepo(t, probe.path, probe.content)
			seedLensEscalation(t, dir)

			code, output := runReviewedPass(t, dir)
			if code != 0 {
				t.Fatalf("exit = %d, want a clean reviewed pass\n%s", code, output)
			}
			score, window := lensScoreAfterRun(t, dir)
			if score == 0 {
				t.Fatalf("prose at %s cleared the %s escalation:\n%+v", probe.path, judgedContentSeedLens, window)
			}
			for _, record := range window {
				if record.Outcome != "pass" {
					continue
				}
				for _, kind := range record.JudgedContent {
					if kind == provenance.ContentTests {
						t.Fatalf("the run recorded %s as test content: %+v", probe.path, record.JudgedContent)
					}
				}
			}
		})
	}
}

// TestAReviewedPassOnTestCodeStillClearsATestLens is the control that keeps the
// probe honest. If a reviewed pass could not clear the escalation at all, the
// probe above would pass for the wrong reason and the escalation would be
// permanent, which is the failure a clearing route exists to prevent.
func TestAReviewedPassOnTestCodeStillClearsATestLens(t *testing.T) {
	t.Parallel()

	dir := judgedContentRepo(t, "tests/helper_test.go", "package tests\n\nimport \"testing\"\n\nfunc TestHelper(t *testing.T) {\n\tif len(\"a\") != 1 {\n\t\tt.Fatal(\"length\")\n\t}\n}\n\nfunc TestHelperAgain(t *testing.T) {\n\tif len(\"ab\") != 2 {\n\t\tt.Fatal(\"length\")\n\t}\n}\n")
	seedLensEscalation(t, dir)

	code, output := runReviewedPass(t, dir)
	if code != 0 {
		t.Fatalf("exit = %d, want a clean reviewed pass\n%s", code, output)
	}
	score, window := lensScoreAfterRun(t, dir)
	if score != 0 {
		t.Fatalf("%s = %d after a reviewed pass over test code, so the escalation has no route out:\n%+v", judgedContentSeedLens, score, window)
	}
}
