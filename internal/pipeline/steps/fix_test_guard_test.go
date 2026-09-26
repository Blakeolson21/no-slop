package steps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestFixAgentCannotChangeExistingTests(t *testing.T) {
	for _, mode := range []string{"modify", "delete", "rename", "self-commit", "modify-and-revert", "self-commit-error"} {
		t.Run(mode, func(t *testing.T) {
			dir, base, _ := setupGitRepo(t)
			file := filepath.Join(dir, "trusted_test.go")
			original := "package example\n// trusted assertion\n"
			if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
				t.Fatal(err)
			}
			gitCmd(t, dir, "add", ".")
			gitCmd(t, dir, "commit", "-m", "trusted suite")
			head := gitCmd(t, dir, "rev-parse", "HEAD")
			gitCmd(t, dir, "checkout", "--detach", head)
			calls := 0
			ag := &mockAgent{name: "fixer", runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
				calls++
				if calls > 1 {
					return &agent.Result{Output: json.RawMessage(`{"summary":"reviewed","findings":[]}`)}, nil
				}
				switch mode {
				case "delete":
					if err := os.Remove(file); err != nil {
						t.Fatal(err)
					}
				case "rename":
					if err := os.Rename(file, filepath.Join(dir, "renamed.go")); err != nil {
						t.Fatal(err)
					}
				default:
					if err := os.WriteFile(file, []byte("package example\n// weakened assertion\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "self-commit" || mode == "modify-and-revert" || mode == "self-commit-error" {
					gitCmd(t, dir, "add", "-A")
					gitCmd(t, dir, "commit", "-m", "weaken suite")
				}
				if mode == "modify-and-revert" {
					gitCmd(t, dir, "revert", "--no-edit", "HEAD")
				}
				if mode == "self-commit-error" {
					return nil, errors.New("agent exited after committing")
				}
				return &agent.Result{Output: json.RawMessage(`{"summary":"repair issue","findings":[]}`)}, nil
			}}
			sctx := newTestContextWithDBRecords(t, ag, dir, base, head, config.Commands{})
			sctx.Fixing = true
			sctx.PreviousFindings = `{"findings":[{"id":"review-1","severity":"error","action":"auto-fix","description":"repair behavior"}]}`
			outcome, err := (&ReviewStep{}).Execute(sctx)
			if err != nil {
				t.Fatal(err)
			}
			if outcome == nil || !outcome.NeedsApproval || outcome.AutoFixable {
				t.Fatalf("must ask user: %+v", outcome)
			}
			findings, err := types.ParseFindingsJSON(outcome.Findings)
			if err != nil || len(findings.Items) == 0 {
				t.Fatalf("findings: %v %+v", err, findings)
			}
			if findings.Items[0].Action != types.ActionAskUser || !strings.Contains(findings.Items[0].Description, "diff --git") {
				t.Fatalf("missing ask-user proposed patch: %+v", findings.Items)
			}
			if got := gitCmd(t, dir, "rev-parse", "HEAD"); got != head {
				t.Fatalf("rejected fix remains in history: %s", got)
			}
			content, err := os.ReadFile(file)
			if err != nil || string(content) != original {
				t.Fatalf("trusted test changed: %q %v", content, err)
			}
			stored, err := sctx.DB.GetRun(sctx.Run.ID)
			if err != nil || stored.HeadSHA != head {
				t.Fatalf("adopted rejected fix: %+v %v", stored, err)
			}
			if dirty := gitStatusPorcelain(t, dir); dirty != "" {
				t.Fatalf("rejected delta still staged: %s", dirty)
			}
		})
	}
}

func TestFixAgentMayAddNewTest(t *testing.T) {
	dir, base, head := setupGitRepo(t)
	sctx := newTestContextWithDBRecords(t, &mockAgent{name: "fixer"}, dir, base, head, config.Commands{})
	if err := os.WriteFile(filepath.Join(dir, "new_test.go"), []byte("package example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := commitAgentFixes(sctx, types.StepReview, "add regression", "fallback"); err != nil {
		t.Fatal(err)
	}
	if sctx.Run.HeadSHA == head {
		t.Fatal("new test was not adopted")
	}
}

func TestFixAgentMayProposeTestDiffWithoutApplyingIt(t *testing.T) {
	dir, base, head := setupGitRepo(t)
	ag := &mockAgent{name: "fixer", runFn: func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
		if !strings.Contains(opts.Prompt, "never modify, delete, rename") {
			t.Error("fix prompt does not preserve existing tests")
		}
		return &agent.Result{Output: json.RawMessage(`{"summary":"requires test update","findings":[{"id":"review-1","action":"ask-user","severity":"warning","description":"Proposed test diff:\n--- a/old_test.go\n+++ b/old_test.go\n@@ -1 +1 @@\n-old\n+new"}]}`)}, nil
	}}
	sctx := newTestContextWithDBRecords(t, ag, dir, base, head, config.Commands{})
	sctx.Fixing = true
	sctx.PreviousFindings = `{"findings":[{"id":"review-1","action":"auto-fix","description":"repair"}]}`
	out, err := (&ReviewStep{}).Execute(sctx)
	if err != nil || out == nil || !out.NeedsApproval || !strings.Contains(out.Findings, "old_test.go") {
		t.Fatalf("proposal lost: %+v %v", out, err)
	}
	if len(ag.calls) != 1 || sctx.Run.HeadSHA != head {
		t.Fatal("proposal must park without a rereview or changed head")
	}
}

func TestCIFixCommitCannotModifyExistingTest(t *testing.T) {
	dir, base, _ := setupGitRepo(t)
	file := filepath.Join(dir, "trusted_test.go")
	if err := os.WriteFile(file, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "commit", "-m", "trusted test")
	head := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "checkout", "--detach", head)
	sctx := newTestContextWithDBRecords(t, &mockAgent{name: "fixer"}, dir, base, head, config.Commands{})
	if err := os.WriteFile(file, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := (&CIStep{}).commitRepair(sctx, "repair")
	var blocked *protectedTestChange
	if changed || !errors.As(err, &blocked) {
		t.Fatalf("CI test change accepted: %v %v", changed, err)
	}
	if got := gitCmd(t, dir, "rev-parse", "HEAD"); got != head {
		t.Fatal("CI retained forbidden test commit")
	}
}
