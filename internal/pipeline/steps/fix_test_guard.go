package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/pipeline"
	"github.com/Blakeolson21/no-slop/internal/safeurl"
	"github.com/Blakeolson21/no-slop/internal/shellenv"
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
	guardFailedFix(sctx, err)

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

func guardFailedFix(sctx *pipeline.StepContext, err *error) {
	// An agent may commit before returning an error or malformed output. Cover
	// that path too, before terminal head reconciliation can adopt its commit.
	if *err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(sctx.Ctx), 30*time.Second)
		defer cancel()
		cleanup := *sctx
		cleanup.Ctx = cleanupCtx
		head, readErr := stepGitHeadSHA(&cleanup)
		if readErr == nil && head != sctx.Run.HeadSHA {
			// Entry continuity errors must remain read-only. Only inspect a forward
			// repair, never try to restore a missing, backward, or sibling baseline.
			if _, ancestryErr := stepGitRun(&cleanup, "merge-base", "--is-ancestor", sctx.Run.HeadSHA, head); ancestryErr != nil {
				return
			}
			if guardErr := guardFixTestCommits(&cleanup, head); guardErr != nil {
				*err = guardErr
			}
		}
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
	baseline := strings.TrimSpace(sctx.Run.HeadSHA)
	if baseline == "" {
		return fmt.Errorf("cannot protect existing tests without a recorded starting head")
	}
	candidate = strings.TrimSpace(candidate)
	if candidate == baseline {
		return nil
	}
	// Even an unreadable diff fails closed: reject this repair, never let
	// terminal head reconciliation adopt a candidate we could not inspect.
	defer func() {
		if err == nil {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(sctx.Ctx), 30*time.Second)
		defer cancel()
		cleanup := *sctx
		cleanup.Ctx = cleanupCtx
		if _, restoreErr := stepGitRun(&cleanup, "reset", "--hard", baseline); restoreErr != nil {
			err = fmt.Errorf("%v; restore rejected repair: %w", err, restoreErr)
		}
	}()
	// A CI fixer may rebase the feature commits onto an advanced base, which
	// replaces their SHAs and makes the recorded head a sibling of the result.
	// Keep rejecting rewinds, but inspect the tree against the recorded baseline
	// for a rewritten history instead of requiring the old head to be an ancestor.
	mergeBase, err := stepGitRun(sctx, "merge-base", baseline, candidate)
	if err != nil {
		return fmt.Errorf("inspect fix candidate history: %w", err)
	}
	mergeBase = strings.TrimSpace(mergeBase)
	switch mergeBase {
	case baseline:
		// Ordinary forward repair: inspect every commit below, including edits
		// later reverted in the candidate history.
	case candidate:
		return fmt.Errorf("fix candidate rewinds recorded starting head %s", baseline)
	default:
		// Rebased repair: compare every existing-test change in the rewritten
		// history with the original commit series, then verify the final tree.
		return guardRebasedFixTests(sctx, mergeBase, baseline, candidate)
	}
	commits, err := stepGitRun(sctx, "rev-list", "--reverse", baseline+".."+candidate)
	if err != nil {
		return fmt.Errorf("inspect fix commits: %w", err)
	}
	var proposals []string
	for _, commit := range strings.Fields(commits) {
		changed, err := fixTestGitOutput(sctx, "diff-tree", "--no-commit-id", "-r", "-m", "--no-renames", "--diff-filter=MDT", "--name-only", "-z", commit)
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
		patch, err := fixTestGitOutput(sctx, append(args, paths...)...)
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

type fixTestCommitChange struct {
	commit    string
	signature string
	paths     []string
}

func guardRebasedFixTests(sctx *pipeline.StepContext, mergeBase, baseline, candidate string) error {
	protected, err := baselineTestPaths(sctx, baseline)
	if err != nil {
		return fmt.Errorf("inspect baseline test paths: %w", err)
	}
	if len(protected) == 0 {
		return nil
	}

	baselineChanges, err := fixTestHistoryChanges(sctx, mergeBase, baseline, protected)
	if err != nil {
		return fmt.Errorf("inspect recorded test history: %w", err)
	}
	candidateChanges, err := fixTestHistoryChanges(sctx, mergeBase, candidate, protected)
	if err != nil {
		return fmt.Errorf("inspect rebased test history: %w", err)
	}
	// Match rewritten copies of test-changing commits from the recorded history.
	// Rebased merge histories can interleave commits from several parents, so
	// compare a multiset of transitions instead of a first-parent sequence.
	baselineTransitions := make(map[string]int, len(baselineChanges))
	for _, change := range baselineChanges {
		baselineTransitions[change.signature]++
	}
	var proposals []string
	for _, change := range candidateChanges {
		if baselineTransitions[change.signature] > 0 {
			baselineTransitions[change.signature]--
			continue
		}
		patch, err := fixTestCommitPatch(sctx, change.commit, change.paths)
		if err != nil {
			return fmt.Errorf("read rebased repair test patch: %w", err)
		}
		proposals = append(proposals, string(patch))
	}
	if len(proposals) > 0 {
		return protectedTestFindings(sctx, strings.Join(proposals, "\n"))
	}

	changed, err := fixTestGitOutput(sctx, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-only", "-z", "--diff-filter=MDT", baseline, candidate, "--")
	if err != nil {
		return fmt.Errorf("inspect existing tests across CI rebase: %w", err)
	}
	var paths []string
	for _, file := range strings.Split(string(changed), "\x00") {
		if file != "" && protected[file] {
			paths = append(paths, ":(literal)"+file)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--binary", baseline, candidate, "--"}
	patch, err := fixTestGitOutput(sctx, append(args, paths...)...)
	if err != nil {
		return fmt.Errorf("read existing-test changes across CI rebase: %w", err)
	}
	return protectedTestFindings(sctx, string(patch))
}

func baselineTestPaths(sctx *pipeline.StepContext, baseline string) (map[string]bool, error) {
	paths, err := fixTestGitOutput(sctx, "ls-tree", "-r", "-z", "--name-only", baseline)
	if err != nil {
		return nil, err
	}
	protected := make(map[string]bool)
	for _, path := range strings.Split(string(paths), "\x00") {
		if path != "" && isTestFile(path) {
			protected[path] = true
		}
	}
	return protected, nil
}

// fixTestHistoryChanges returns the per-commit transitions to test files that
// existed at the run's recorded head. Rebased feature commits may have new
// commit IDs, but unchanged test transitions still have the same path, modes,
// and blob IDs. Walking every reachable commit and comparing merges to each
// parent exposes side-branch and merge-resolution edits even when the final
// tree matches the recorded head.
func fixTestHistoryChanges(sctx *pipeline.StepContext, from, to string, protected map[string]bool) ([]fixTestCommitChange, error) {
	commits, err := stepGitRun(sctx, "rev-list", "--topo-order", "--reverse", from+".."+to)
	if err != nil {
		return nil, err
	}
	var changes []fixTestCommitChange
	for _, commit := range strings.Fields(commits) {
		raw, err := fixTestGitOutput(sctx, "diff-tree", "--root", "--no-commit-id", "-r", "-m", "--raw", "-z", "--no-renames", commit)
		if err != nil {
			return nil, err
		}
		fields := strings.Split(string(raw), "\x00")
		var transitions []string
		var paths []string
		for i := 0; i < len(fields) && fields[i] != ""; {
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("malformed raw diff for commit %s", commit)
			}
			metadata := fields[i]
			path := fields[i+1]
			i += 2
			if !strings.HasPrefix(metadata, ":") || !protected[path] {
				if !strings.HasPrefix(metadata, ":") {
					return nil, fmt.Errorf("malformed raw diff metadata for commit %s", commit)
				}
				continue
			}
			parts := strings.Fields(strings.TrimPrefix(metadata, ":"))
			if len(parts) != 5 {
				return nil, fmt.Errorf("unexpected raw diff metadata for commit %s", commit)
			}
			var transition strings.Builder
			for _, part := range append([]string{path}, parts...) {
				fmt.Fprintf(&transition, "%d:", len(part))
				transition.WriteString(part)
			}
			transitions = append(transitions, transition.String())
			paths = append(paths, path)
		}
		if len(transitions) == 0 {
			continue
		}
		sort.Strings(transitions)
		sort.Strings(paths)
		changes = append(changes, fixTestCommitChange{
			commit:    commit,
			signature: strings.Join(transitions, "\x00"),
			paths:     paths,
		})
	}
	return changes, nil
}

func fixTestCommitPatch(sctx *pipeline.StepContext, commit string, paths []string) ([]byte, error) {
	args := []string{"show", "--format=", "-m", "--no-ext-diff", "--no-textconv", "--no-renames", "--binary", commit, "--"}
	for _, path := range paths {
		args = append(args, ":(literal)"+path)
	}
	return fixTestGitOutput(sctx, args...)
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

// A review must still certify the restored (or safely repaired) head before
// an adjudicator can approve this gate. Keep the protected proposal alongside
// that independent review, even when the reviewer reports no findings.
func mergeProtectedTestProposal(proposal string, outcome **pipeline.StepOutcome, err *error) {
	if proposal == "" || *err != nil || *outcome == nil {
		return
	}
	protected, parseErr := types.ParseFindingsJSON(proposal)
	if parseErr != nil {
		*err = parseErr
		return
	}
	reviewed, parseErr := types.ParseFindingsJSON((*outcome).Findings)
	if parseErr != nil {
		*err = parseErr
		return
	}
	// The proposal wins for a repeated finding ID; reviewer silence or a
	// softer action cannot turn a prohibited test change back into auto-fix.
	ids := make(map[string]bool)
	for _, f := range protected.Items {
		if f.ID != "" {
			ids[f.ID] = true
		}
	}
	for _, f := range reviewed.Items {
		if !ids[f.ID] {
			protected.Items = append(protected.Items, f)
		}
	}
	raw, marshalErr := json.Marshal(protected)
	if marshalErr != nil {
		*err = marshalErr
		return
	}
	(*outcome).Findings = string(raw)
	(*outcome).FindingsNormalized = false
	(*outcome).NeedsApproval = true
	(*outcome).AutoFixable = false
}

// Preserve raw NUL-delimited paths and patch whitespace, with the same
// step-scoped PATH, credential environment and bounds as other CI git work.
func fixTestGitOutput(sctx *pipeline.StepContext, args ...string) ([]byte, error) {
	cmd, cancel := stepGitCmd(sctx, args...)
	defer cancel()
	out, err := shellenv.OutputShellCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("git test-change inspection: %w", err)
	}
	return out, nil
}
