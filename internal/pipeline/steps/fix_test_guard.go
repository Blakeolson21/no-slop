package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/git"
	"github.com/Blakeolson21/no-slop/internal/pipeline"
	"github.com/Blakeolson21/no-slop/internal/safeurl"
	"github.com/Blakeolson21/no-slop/internal/types"
)

const preserveExistingTestsPrompt = `

Existing tests are immutable to the fix agent. You may create new test files, but never modify, delete, rename, or replace an existing test file, even to fix a flaky or platform-specific test. If a repair requires an existing test change, leave it unchanged and return the unresolved finding as ask-user with the proposed unified test diff in its description. For summary-only output, include it in an optional "findings" array alongside "summary". This restriction overrides any instruction to repair existing tests.`

type protectedTestChange struct{ findings string }

func (e *protectedTestChange) Error() string {
	return "fix requires changes to existing tests; adjudication required"
}

// A rejected candidate is restored before this error can become an approval
// gate. Thus approving the finding cannot accidentally publish the bad commit.
func returnProtectedTestFinding(sctx *pipeline.StepContext, outcome **pipeline.StepOutcome, err *error) {
	// An agent may commit before returning an error or malformed output. Cover
	// that path too, before terminal head reconciliation can adopt its commit.
	if *err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(sctx.Ctx), 30*time.Second)
		defer cancel()
		cleanup := *sctx
		cleanup.Ctx = cleanupCtx
		head, readErr := git.HeadSHA(cleanupCtx, sctx.WorkDir)
		if readErr == nil && head != sctx.Run.HeadSHA {
			if guardErr := guardFixTestCommits(&cleanup, head); guardErr != nil {
				*err = guardErr
			}
		}
	}

	var blocked *protectedTestChange
	if errors.As(*err, &blocked) {
		if sctx.Ctx.Err() != nil {
			*err = context.Cause(sctx.Ctx)
			return
		}
		*outcome = &pipeline.StepOutcome{NeedsApproval: true, Findings: blocked.findings}
		*err = nil
	}
}

func protectedTestFindings(sctx *pipeline.StepContext, diff string) error {
	findings, _ := types.ParseFindingsJSON(sctx.PreviousFindings)
	if len(findings.Items) == 0 {
		findings.Items = []types.Finding{{ID: "fix-test-guard", Severity: "error", Description: "Repair requires an existing test change."}}
	}
	sctx.Log("rejected existing-test diff:\n" + safeurl.RedactText(diff))
	// Bound the gate payload; identify truncation explicitly rather than claiming
	// that a partial proposal is the complete patch.
	const maxProposalBytes = 128 * 1024
	limit := maxProposalBytes / len(findings.Items)
	if len(diff) > limit {
		diff = diff[:limit] + "\n[proposed diff truncated to bound the gate payload]"
	}
	for i := range findings.Items {
		findings.Items[i].Action = types.ActionAskUser
		findings.Items[i].Description += "\nExisting tests were preserved; the rejected fix was not adopted. Proposed test diff:\n" + safeurl.RedactText(diff)
	}
	findings.Summary = "Existing test changes require adjudication"
	raw, err := json.Marshal(findings)
	if err != nil {
		return err
	}
	return &protectedTestChange{findings: string(raw)}
}

// guardFixTestCommits checks every candidate commit, not just the net diff:
// an agent's edit-and-revert still leaves a forbidden fix commit in history.
// Disable rename detection so deleting/renaming an old test cannot look like
// a permitted new file. NUL paths preserve spaces, newlines and quoting.
// The baseline is the pipeline's recorded head, never an agent-controlled ref.
func guardFixTestCommits(sctx *pipeline.StepContext, candidate string) (err error) {
	baseline := sctx.Run.HeadSHA
	if candidate == baseline {
		return nil
	}
	if baseline == "" {
		return fmt.Errorf("cannot protect existing tests without a recorded starting head")
	}
	// Even an unreadable diff fails closed: reject this repair, never let
	// terminal head reconciliation adopt a candidate we could not inspect.
	defer func() {
		if err == nil {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(sctx.Ctx), 30*time.Second)
		defer cancel()
		if _, restoreErr := git.Run(cleanupCtx, sctx.WorkDir, "reset", "--hard", baseline); restoreErr != nil {
			err = fmt.Errorf("%v; restore rejected repair: %w", err, restoreErr)
		}
	}()
	commits, err := git.Run(sctx.Ctx, sctx.WorkDir, "rev-list", "--reverse", baseline+".."+candidate)
	if err != nil {
		return fmt.Errorf("inspect fix commits: %w", err)
	}
	var proposals []string
	for _, commit := range strings.Fields(commits) {
		changed, err := git.Output(sctx.Ctx, sctx.WorkDir, "diff-tree", "--no-commit-id", "-r", "-m", "--no-renames", "--diff-filter=MDT", "--name-only", "-z", commit)
		if err != nil {
			return fmt.Errorf("inspect fix test paths: %w", err)
		}
		var paths []string
		for _, file := range strings.Split(string(changed), "\x00") {
			if file != "" && isTestFile(file) {
				paths = append(paths, ":(literal)"+file)
			}
		}
		if len(paths) == 0 {
			continue
		}
		args := []string{"show", "--format=", "--no-ext-diff", "--no-textconv", "--no-renames", "--binary", "-m", commit, "--"}
		patch, err := git.Output(sctx.Ctx, sctx.WorkDir, append(args, paths...)...)
		if err != nil {
			return fmt.Errorf("read proposed test patch: %w", err)
		}
		proposals = append(proposals, string(patch))
	}
	if len(proposals) == 0 {
		return nil
	}
	// All candidate changes belong to this pipeline repair. Reject the entire
	// repair so dependent product changes cannot ship against the old tests.
	// No branch ref, run head, or uncertified range has been advanced yet.
	return protectedTestFindings(sctx, strings.Join(proposals, "\n"))
}

func proposedTestFindings(result *agent.Result) error {
	if result == nil || len(result.Output) == 0 {
		return nil
	}
	findings, err := types.ParseFindingsJSON(string(result.Output))
	if err != nil || len(findings.Items) == 0 {
		return nil
	}
	for i := range findings.Items {
		findings.Items[i].Action = types.ActionAskUser
	}
	raw, err := json.Marshal(findings)
	if err != nil {
		return err
	}
	return &protectedTestChange{findings: string(raw)}
}
