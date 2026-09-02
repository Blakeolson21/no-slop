package steps

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/git"
	"github.com/Blakeolson21/no-slop/internal/pipeline"
	"github.com/Blakeolson21/no-slop/internal/types"
)

type fixExecutionOptions struct {
	RequirePreviousFindings bool
	MissingFindingsError    string
	LogMessage              string
	Prompt                  string
	ErrorPrefix             string
	FallbackSummary         string
	AfterAgentRun           func(*agent.Result) error
	// SessionRole, when set, runs the fix turn in that durable review-loop
	// session (the review step's fixer role). Steps outside the review loop
	// leave it empty and stay session-isolated.
	SessionRole pipeline.SessionRole
	// Purpose labels the invocation for local performance telemetry.
	Purpose string
	// Workload records the bounded size of the change under fix for local
	// telemetry. Optional; nil leaves the invocation's workload unknown.
	Workload *agent.InvocationWorkload
}

type commitSummary struct {
	Summary string `json:"summary"`
}

var errRejectedCommitSummary = errors.New("rejected commit summary")

var commitSummarySchema = json.RawMessage(fmt.Sprintf(`{
	"type": "object",
	"properties": {
		"summary": {"type": "string", "maxLength": %d}
	},
	"required": ["summary"]
}`, config.MaxFixMessageSummaryBytes))

// hasBlockingFindings returns true when any finding meets the configured
// severity floor. Unknown finding severities fail closed: an unclassified
// finding must not silently become advisory.
func hasBlockingFindings(items []Finding, floor string) bool {
	if floor == "" {
		floor = config.DefaultBlockingSeverity
	}
	if floor != config.BlockingSeverityWarning && floor != config.BlockingSeverityError {
		return len(items) > 0
	}
	for _, f := range items {
		switch f.Severity {
		case "error":
			return true
		case "warning":
			if floor == config.BlockingSeverityWarning {
				return true
			}
		case "info":
			// Informational findings are advisory under both supported floors.
		default:
			return true
		}
	}
	return false
}

// assertPipelineHeadContinuity fails closed when the worktree HEAD is no longer
// equal to or a descendant of the head the pipeline itself last recorded
// (sctx.Run.HeadSHA). Every post-review step calls this guard at entry, and
// commitAgentFixes calls it around commits that advance the recorded head.
//
// The pipeline advances HEAD only through its own commits, each of which updates
// sctx.Run.HeadSHA in lockstep. If HEAD has diverged from that recorded head -
// e.g. a concurrent process reset the shared worktree to a different commit -
// then the reviewed change the pipeline approved is no longer in HEAD's history,
// and continuing would ship an unreviewed tree. The whole job of this tool is
// to not lose people's code, so we refuse rather than proceed.
//
// Anchor integrity: sctx.Run.HeadSHA is the correct, un-clobberable anchor. It
// is the *recorded* head the pipeline itself produced at its last commit - held
// in the single daemon process's in-memory Run struct (one shared pointer per
// run, never re-read from the DB mid-pipeline) and written only by no-slop
// commit code (commit_fix / rebase / ci_fix / push). An out-of-band `git reset`
// mutates the worktree HEAD on disk but cannot touch this field, so at the check
// point the anchor still holds the reviewed head even after a clobber. The guard
// deliberately compares the *recorded* head against the *live* worktree HEAD
// (git.HeadSHA); it never derives the anchor from the mutable worktree, which
// would be circular and defeatable. Because the guard runs at every post-review
// step entry and at the very top of commitAgentFixes - before any commit that
// would advance sctx.Run.HeadSHA - the next pipeline boundary after a clobber is
// caught while the anchor is still the pre-clobber reviewed head; the anchor can
// never be advanced into a clobbered lineage without first passing this check.
//
// This is what happened in run 01KXC3SD5NZYMERGDS68Z1C8ER: the review step
// committed a correct fix, a sibling worktree sharing the bare repo reset HEAD
// to a divergent commit that lacked it, and the document step committed on the
// clobber and shipped it. A forward-only agent commit (git rebase --continue,
// etc.) keeps the recorded head as an ancestor and is allowed; a divergent
// (sibling) reset or a backward reset both trip this guard. On any failure the
// step and the whole run abort (executor.failRun) before doing more work -
// nothing is committed or shipped.
func assertPipelineHeadContinuity(sctx *pipeline.StepContext, stepName types.StepName) error {
	recorded := strings.TrimSpace(sctx.Run.HeadSHA)
	if recorded == "" {
		return nil
	}
	currentHead, err := git.HeadSHA(sctx.Ctx, sctx.WorkDir)
	if err != nil {
		return fmt.Errorf("resolve head before %s step: %w", stepName, err)
	}
	if currentHead == recorded {
		return nil
	}
	// Fail closed: refuse unless the recorded head is genuinely an ancestor of the
	// live HEAD (a legitimate forward move). A non-ancestor result OR any git error
	// (e.g. an unknown recorded object) aborts rather than proceeds.
	if _, err := git.Run(sctx.Ctx, sctx.WorkDir, "merge-base", "--is-ancestor", recorded, currentHead); err != nil {
		return fmt.Errorf("refusing to run %s step: worktree HEAD %s is not a descendant of the pipeline's recorded head %s; "+
			"the reviewed change was rewritten out-of-band and would be lost - aborting to protect it",
			stepName, currentHead, recorded)
	}
	return nil
}

// commitAgentFixes records everything the fix agent produced onto the run:
// uncommitted worktree changes are staged and committed, and commits the agent
// created itself are adopted by advancing the branch ref and the recorded run
// head. Agents routinely self-commit (many repos' agent instructions demand
// it); before adoption existed, a self-committing agent left the worktree
// clean, the early "no agent changes to commit" return skipped the branch-ref
// update, and the fix commit was stranded on the worktree's detached HEAD and
// destroyed with the worktree while the run shipped without it.
//
// The continuity guard brackets the fix commit: the first call catches an
// out-of-band clobber before anything is committed on top of it, and the
// second call re-verifies ancestry against the live HEAD immediately before
// adoption, so a reset that races the pipeline's own commit is also refused.
// A forward HEAD move passes both checks and is adopted below.
func commitAgentFixes(sctx *pipeline.StepContext, stepName types.StepName, summary, fallbackSummary string) error {
	ctx := sctx.Ctx
	if err := assertPipelineHeadContinuity(sctx, stepName); err != nil {
		return err
	}
	var commitMessage string
	status, _ := git.Run(ctx, sctx.WorkDir, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		if summary == "" {
			summary = fallbackSummary
		}
		if summary == "" {
			summary = "apply fixes"
		}
		var err error
		commitMessage, err = sctx.Config.Commit.RenderFixMessage(stepName, summary)
		if err != nil {
			return fmt.Errorf("render %s fix commit message: %w", stepName, err)
		}
		if _, err := git.Run(ctx, sctx.WorkDir, "add", "-A"); err != nil {
			return fmt.Errorf("stage %s changes: %w", stepName, err)
		}
		if _, err := git.Run(ctx, sctx.WorkDir, "commit", "-m", commitMessage); err != nil {
			return fmt.Errorf("commit %s changes: %w", stepName, err)
		}
	}
	headSHA, err := git.HeadSHA(ctx, sctx.WorkDir)
	if err != nil {
		return fmt.Errorf("resolve head after %s fix round: %w", stepName, err)
	}
	recorded := strings.TrimSpace(sctx.Run.HeadSHA)
	if headSHA == recorded {
		sctx.Log("no agent changes to commit")
		return nil
	}
	if err := assertPipelineHeadContinuity(sctx, stepName); err != nil {
		return err
	}
	startingHead := strings.TrimSpace(sctx.ReviewStartingHeadSHA)
	if startingHead == "" {
		startingHead = sctx.Run.HeadSHA
	}
	var rollbackRange func() error
	if stepPersistsUncertifiedReview(stepName) {
		rollbackRange, err = pipeline.PersistUncertifiedPipelineRangeWithRollback(sctx, startingHead, headSHA)
		if err != nil {
			return fmt.Errorf("persist uncertified review range before %s head adoption: %w", stepName, err)
		}
	}
	if err := adoptBranchRef(sctx, headSHA); err != nil {
		if rollbackRange != nil {
			if rollbackErr := rollbackRange(); rollbackErr != nil {
				return fmt.Errorf("%w; restore uncertified review range: %v", err, rollbackErr)
			}
		}
		return err
	}
	sctx.Run.HeadSHA = headSHA
	if err := sctx.DB.UpdateRunHeadSHA(sctx.Run.ID, headSHA); err != nil {
		return err
	}
	if commitMessage != "" {
		sctx.Log(fmt.Sprintf("committed agent fixes: %s", commitMessage))
	} else {
		sctx.Log(fmt.Sprintf("adopted agent-created commit(s): head advanced to %s", headSHA))
	}
	return nil
}

func stepPersistsUncertifiedReview(stepName types.StepName) bool {
	switch stepName {
	case types.StepReview, types.StepTest, types.StepDocument, types.StepLint, types.StepCI:
		return true
	default:
		return false
	}
}

func extractCommitSummary(result *agent.Result) (string, error) {
	var summary commitSummary
	if result.Output == nil {
		return "", fmt.Errorf("agent returned no structured summary")
	}
	if !utf8.Valid(result.Output) {
		return "", fmt.Errorf("%w: agent output must contain valid UTF-8", errRejectedCommitSummary)
	}
	if err := json.Unmarshal(result.Output, &summary); err != nil {
		return "", fmt.Errorf("parse commit summary: %w", err)
	}
	cleaned := strings.Join(strings.Fields(summary.Summary), " ")
	cleaned = stripThreadStatusFooter(cleaned)
	cleaned = strings.Trim(cleaned, " \t\r\n\"'.;:,-")
	if len(cleaned) > config.MaxFixMessageSummaryBytes {
		return "", fmt.Errorf("%w: commit summary must not exceed %d bytes", errRejectedCommitSummary, config.MaxFixMessageSummaryBytes)
	}
	return cleaned, nil
}

// isThreadStatusGlyph reports whether r opens a chat status-footer line, such
// as "✅ done", "⏳ blocked on: <x>", or "🙋 waiting: <x>". Status footers are a
// common convention in user instruction files (AGENTS.md, CLAUDE.md), which
// direct an agent to close every turn-ending message with one.
func isThreadStatusGlyph(r rune) bool {
	switch r {
	case '✅', // white heavy check mark
		'⏳',          // hourglass with flowing sand
		'\U0001f64b': // happy person raising one hand
		return true
	}
	return false
}

// stripThreadStatusFooter removes a trailing chat status footer from an
// agent-authored commit summary.
//
// The fix agent reads the user's instruction files, so a repository that asks
// for a status footer on every turn-ending message gets one appended to the
// structured summary too: the agent treats finishing its fix as ending a turn.
// That summary becomes the commit subject, so the footer lands in permanent
// history as "no-slop(review): Harden the parser ✅ done". The footer
// addresses a human reading a chat thread and carries no meaning in a subject
// line, so truncating at the glyph keeps what the agent actually described.
//
// Normalizing rather than rejecting is deliberate: the round's code changes are
// already applied and valid, and failing over a cosmetic suffix would discard
// real work.
func stripThreadStatusFooter(summary string) string {
	if idx := strings.IndexFunc(summary, isThreadStatusGlyph); idx >= 0 {
		return summary[:idx]
	}
	return summary
}

// executeFixMode runs the fix agent and commits any resulting changes. It
// returns the agent's one-line fix summary (empty when the agent returned
// nothing parseable), which the caller should place on StepOutcome.FixSummary
// so the executor can persist it on the round record.
func executeFixMode(sctx *pipeline.StepContext, stepName types.StepName, opts fixExecutionOptions) (string, error) {
	if !sctx.Fixing {
		return "", nil
	}
	if opts.RequirePreviousFindings && sctx.PreviousFindings == "" {
		return "", errors.New(opts.MissingFindingsError)
	}
	if opts.LogMessage != "" {
		sctx.Log(opts.LogMessage)
	}
	purpose := opts.Purpose
	if purpose == "" {
		purpose = string(stepName) + "-fix"
	}
	runOpts := agent.RunOpts{
		Prompt:     opts.Prompt,
		CWD:        sctx.WorkDir,
		JSONSchema: commitSummarySchema,
		OnChunk:    sctx.LogChunk,
		Purpose:    purpose,
		Workload:   opts.Workload,
	}
	var result *agent.Result
	var err error
	if opts.SessionRole != "" {
		result, err = sctx.RunAgentSession(opts.SessionRole, runOpts)
	} else {
		result, err = sctx.Agent.Run(sctx.Ctx, runOpts)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", opts.ErrorPrefix, err)
	}
	if opts.AfterAgentRun != nil {
		if err := opts.AfterAgentRun(result); err != nil {
			return "", err
		}
	}
	summary, err := extractCommitSummary(result)
	if err != nil {
		if errors.Is(err, errRejectedCommitSummary) {
			return "", fmt.Errorf("validate %s fix summary: %w", stepName, err)
		}
		sctx.Log(fmt.Sprintf("warning: could not parse fix summary: %v", err))
	}
	if err := commitAgentFixes(sctx, stepName, summary, opts.FallbackSummary); err != nil {
		return "", err
	}
	return summary, nil
}
