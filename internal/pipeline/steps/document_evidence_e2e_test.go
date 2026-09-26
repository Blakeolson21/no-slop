//go:build e2e

package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/config"
)

// The fake verdict agent reads only its prompt: these tests exercise the
// evidence boundary without giving it tools to compensate for missing input.
func TestDocumentEvidence_VerdictReceivesDiff(t *testing.T) {
	for _, lint := range []string{"configured-lint", ""} {
		t.Run(fmt.Sprintf("lint=%q", lint), func(t *testing.T) {
			dir, base, _ := setupGitRepo(t)
			writeDocumentEvidenceFile(t, dir, "README.md", "# Project\nUse --output json for JSON output.\n")
			gitCmd(t, dir, "add", "README.md")
			gitCmd(t, dir, "commit", "-m", "document JSON output")
			head := gitCmd(t, dir, "rev-parse", "HEAD")
			ag := &mockAgent{name: "verdict", runFn: func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
				for _, want := range []string{"Changed paths", `"README.md"`, "diff --git a/README.md b/README.md", "+Use --output json for JSON output.", "+feature code"} {
					if !strings.Contains(opts.Prompt, want) {
						t.Errorf("verdict prompt lacks %q", want)
					}
				}
				return &agent.Result{Output: json.RawMessage(`{"findings":[],"summary":"docs current"}`)}, nil
			}}
			sctx := newHousekeepingContext(t, ag, dir, base, head, config.Commands{Lint: lint})
			if _, err := (&DocumentStep{}).Execute(sctx); err != nil {
				t.Fatal(err)
			}
			if len(ag.calls) != 1 {
				t.Fatalf("agent calls = %d, want 1", len(ag.calls))
			}
		})
	}
}

func TestDocumentEvidence_NoDocRelevantChange(t *testing.T) {
	dir, _, base := setupGitRepo(t)
	writeDocumentEvidenceFile(t, dir, "regression_test.go", "package example\n// Test fixture spelling corrected.\n")
	gitCmd(t, dir, "add", "regression_test.go")
	gitCmd(t, dir, "commit", "-m", "correct test fixture spelling")
	head := gitCmd(t, dir, "rev-parse", "HEAD")
	ag := &mockAgent{name: "verdict", runFn: func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
		// Model the tool-free verdict contract: a missing patch is an evidence
		// finding, while this test-only patch needs no documentation changes.
		if !strings.Contains(opts.Prompt, "+// Test fixture spelling corrected.") {
			return &agent.Result{Output: json.RawMessage(`{"findings":[{"id":"DOC-EVIDENCE-MISSING","severity":"warning","description":"no diff supplied","action":"ask-user"}],"summary":"missing evidence"}`)}, nil
		}
		return &agent.Result{Output: json.RawMessage(`{"findings":[],"summary":"no documentation changes needed"}`)}, nil
	}}
	sctx := newHousekeepingContext(t, ag, dir, base, head, config.Commands{Lint: "configured-lint"})
	// This repository has no known default branch; exercise the submitted
	// base fallback rather than including the template's earlier feature.
	sctx.Repo.DefaultBranch = "unknown"
	outcome, err := (&DocumentStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ag.calls) != 1 || outcome.NeedsApproval || strings.Contains(outcome.Findings, "DOC-EVIDENCE") {
		t.Fatalf("test-only change parked: calls=%d outcome=%+v", len(ag.calls), outcome)
	}
}

func TestDocumentEvidence_CapsEachFileAndWholeDiff(t *testing.T) {
	dir, base, _ := setupGitRepo(t)
	for i := 0; i < 12; i++ {
		writeDocumentEvidenceFile(t, dir, fmt.Sprintf("large-%02d.md", i), strings.Repeat("large documentation line\n", 2000)+"OMITTED_TAIL\n")
	}
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "commit", "-m", "large docs change")
	head := gitCmd(t, dir, "rev-parse", "HEAD")
	ag := &mockAgent{name: "verdict", runFn: func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
		for _, want := range []string{"[file diff truncated]", "[remaining diffs omitted: total byte limit]", "diff --git a/large-01.md b/large-01.md", `"large-11.md"`} {
			if !strings.Contains(opts.Prompt, want) {
				t.Errorf("bounded evidence lacks %q", want)
			}
		}
		if strings.Contains(opts.Prompt, "OMITTED_TAIL") || len(opts.Prompt) > 180*1024 {
			t.Errorf("unbounded document prompt (%d bytes)", len(opts.Prompt))
		}
		return &agent.Result{Output: json.RawMessage(`{"findings":[],"summary":"docs current"}`)}, nil
	}}
	sctx := newHousekeepingContext(t, ag, dir, base, head, config.Commands{Lint: "configured-lint"})
	if _, err := (&DocumentStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
}

func TestDocumentEvidence_PreservesUnusualPathsAndNonTextChanges(t *testing.T) {
	dir, base, _ := setupGitRepo(t)
	// A pathspec wildcard must not expand to the other changed files, and
	// spaces in a filename must survive the changed-path inventory.
	for name, contents := range map[string]string{
		"literal[1].md": "literal path content\n",
		"space name.md": "space path content\n",
		"binary.dat":    "binary\x00payload",
	} {
		writeDocumentEvidenceFile(t, dir, name, contents)
	}
	gitCmd(t, dir, "rm", "base.txt")
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "commit", "-m", "change unusual paths")
	head := gitCmd(t, dir, "rev-parse", "HEAD")
	ag := &mockAgent{name: "verdict", runFn: func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
		for _, want := range []string{`"space name.md"`, "+space path content", "+literal path content", "deleted file mode", "Binary files /dev/null and b/binary.dat differ"} {
			if !strings.Contains(opts.Prompt, want) {
				t.Errorf("document evidence lacks %q", want)
			}
		}
		return &agent.Result{Output: json.RawMessage(`{"findings":[],"summary":"docs current"}`)}, nil
	}}
	sctx := newHousekeepingContext(t, ag, dir, base, head, config.Commands{Lint: "configured-lint"})
	if _, err := (&DocumentStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
}

func TestDocumentEvidence_ReadFailureStopsBeforeVerdict(t *testing.T) {
	dir, base, head := setupGitRepo(t)
	ag := &mockAgent{name: "verdict", runFn: func(_ context.Context, _ agent.RunOpts) (*agent.Result, error) {
		t.Error("agent must not receive an evidence-free prompt after a diff read failure")
		return &agent.Result{Output: json.RawMessage(`{"findings":[],"summary":"docs current"}`)}, nil
	}}
	sctx := newHousekeepingContext(t, ag, dir, base, head, config.Commands{Lint: "configured-lint"})
	// The evidence subprocess respects the step's environment. Prevent that
	// subprocess from starting, after the changed-path discovery succeeds.
	sctx.Env = []string{"PATH=" + t.TempDir()}
	_, err := (&DocumentStep{}).Execute(sctx)
	if err == nil || !strings.Contains(err.Error(), "get document evidence") {
		t.Fatalf("expected an evidence read error, got %v", err)
	}
}

func writeDocumentEvidenceFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
