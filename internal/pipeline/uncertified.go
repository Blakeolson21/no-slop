package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/git"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// BindUncertifiedPipelineRange copies a persisted uncertified recovery boundary
// onto the review step context when this run's head is that range's tip or a
// descendant of it. Unreadable commit ancestry or persisted review truth
// blocks replacement review.
func BindUncertifiedPipelineRange(sctx *StepContext) error {
	if sctx == nil || sctx.DB == nil || sctx.Repo == nil || sctx.Run == nil || sctx.Fixing {
		return nil
	}
	rng, err := sctx.DB.GetUncertifiedPipelineRange(sctx.Repo.ID, sctx.Run.Branch)
	if err != nil {
		return fmt.Errorf("read uncertified pipeline range: %w", err)
	}
	if rng == nil {
		return nil
	}
	plan, err := reconcileReviewFixSelection(rng, false)
	if err != nil {
		return fmt.Errorf("reconcile uncertified review: %w", err)
	}
	head := strings.TrimSpace(sctx.Run.HeadSHA)
	if head == "" {
		head = strings.TrimSpace(sctx.ReviewStartingHeadSHA)
	}
	inLineage, provable, err := rangeTipAncestry(sctx, rng.ToSHA, head)
	if err != nil {
		return fmt.Errorf("verify uncertified pipeline range ancestry: %w", err)
	}
	if provable && !inLineage {
		warnUncertifiedRangeSkipped(sctx, rng, "uncertified range %s..%s not in gate; not applying provenance")
		return nil
	}
	if !provable {
		// The persisted tip names an object this gate cannot read, so nothing
		// the stored span asserts about this head is provable here. Failing the
		// step was the old answer and it stranded every run whose reviewed head
		// predated the current default branch. Keep the claims and the source
		// run, drop every mapping-dependent conclusion, and owe this head a
		// fresh full review instead.
		fresh := freshReviewRange(rng, head)
		changed, casErr := sctx.DB.RestoreUncertifiedPipelineRangeIfCurrent(*rng, &fresh)
		if casErr != nil {
			return fmt.Errorf("invalidate unprovable uncertified range: %w", casErr)
		}
		if !changed {
			return fmt.Errorf("uncertified pipeline range changed before fresh review")
		}
		rng = &fresh
		if plan, err = reconcileReviewFixSelection(rng, false); err != nil {
			return fmt.Errorf("reconcile uncertified review: %w", err)
		}
		if sctx.Log != nil {
			sctx.Log("uncertified review range cannot be proved in this gate; fresh review required on " + head)
		}
	}
	priorRounds, priorFindings, priorLineages, selectedFindings, err := loadUncertifiedPriorReview(
		sctx.DB, rng.SourceRunID, plan.selectionApplied, rng.FindingsJSON, rng.SelectedFindingIDs,
	)
	if err != nil {
		return err
	}
	if rng.RecoveryState == db.ReviewRecoveryFreshReviewRequired {
		// All previous findings remain claims for a fresh full review. No old
		// selection is treated as an applied fix on an unproved mapping.
		selectedFindings = ""
	}
	if plan.reviewOnly && selectedFindings == "" {
		// Older stores recorded a no-delta marker but not the selected IDs.
		// That cannot authorize a review-only selection round. Preserve all
		// findings as claims and require a fresh full review of this head.
		fresh := freshReviewRange(rng, head)
		changed, err := sctx.DB.RestoreUncertifiedPipelineRangeIfCurrent(*rng, &fresh)
		if err != nil {
			return fmt.Errorf("invalidate incomplete recovered selection: %w", err)
		}
		if !changed {
			return fmt.Errorf("recovered selection changed before fresh review")
		}
		rng = &fresh
		plan.reviewOnly = false
		if sctx.Log != nil {
			sctx.Log("recovered selection is incomplete; fresh full review required on " + head)
		}
	}
	if plan.reviewOnly {
		priorFindings = excludeFindingsJSON(priorFindings, findingIDList(selectedFindings))
		sctx.Fixing = true
		sctx.SkipFixExecution = true
		sctx.PreviousFindings = selectedFindings
	}
	sctx.UncertifiedFromSHA = rng.FromSHA
	sctx.UncertifiedToSHA = rng.ToSHA
	sctx.UncertifiedSourceRunID = rng.SourceRunID
	sctx.UncertifiedPriorRounds = priorRounds
	sctx.UncertifiedPriorFindings = priorFindings
	sctx.UncertifiedPriorLineages = priorLineages
	sctx.UncertifiedSelectedFindings = selectedFindings
	return nil
}

type reviewFixRecoveryPlan struct {
	state            db.ReviewRecoveryState
	selectionApplied bool
	reviewOnly       bool
}

// reconcileReviewFixSelection is the single state transition used by both an
// in-run fixer head promotion and every cross-run bind. The persisted state,
// not from_sha/to_sha equality, decides whether a recovered selection needs a
// review-only post-fix round and whether it has reached the branch.
func reconcileReviewFixSelection(rng *db.UncertifiedPipelineRange, headPromoted bool) (reviewFixRecoveryPlan, error) {
	state := db.ReviewRecoverySelectionApplied
	if rng != nil {
		state = rng.RecoveryState
	}
	if !state.Valid() {
		return reviewFixRecoveryPlan{}, fmt.Errorf("invalid recovery state %q", state)
	}
	if headPromoted {
		switch state {
		case db.ReviewRecoverySelectionRecoveredNoDelta, db.ReviewRecoverySelectionRecoveredWithDelta:
			state = db.ReviewRecoverySelectionRecoveredWithDelta
		case db.ReviewRecoverySelectionApplied:
			// An ordinary post-review promotion carries no pending Review
			// selection, but its delta still requires certification.
		}
	}
	return reviewFixRecoveryPlan{
		state:            state,
		selectionApplied: state != db.ReviewRecoverySelectionRecoveredNoDelta && state != db.ReviewRecoveryFreshReviewRequired,
		reviewOnly:       state == db.ReviewRecoverySelectionRecoveredNoDelta || state == db.ReviewRecoverySelectionRecoveredWithDelta,
	}, nil
}

// PersistUncertifiedPipelineRange records a post-review commit span until a
// review of the new head completes.
func PersistUncertifiedPipelineRange(sctx *StepContext, fromSHA, toSHA string) error {
	_, err := PersistUncertifiedPipelineRangeWithRollback(sctx, fromSHA, toSHA)
	return err
}

func PersistUncertifiedPipelineRangeWithRollback(sctx *StepContext, fromSHA, toSHA string) (func() error, error) {
	if sctx == nil || sctx.DB == nil || sctx.Repo == nil || sctx.Run == nil {
		return nil, fmt.Errorf("persist uncertified pipeline range: missing pipeline context")
	}
	fromSHA = strings.TrimSpace(fromSHA)
	toSHA = strings.TrimSpace(toSHA)
	if fromSHA == "" || toSHA == "" || fromSHA == toSHA {
		return nil, fmt.Errorf("persist uncertified pipeline range: invalid commit range")
	}
	existing, err := sctx.DB.GetUncertifiedPipelineRange(sctx.Repo.ID, sctx.Run.Branch)
	if err != nil {
		return nil, fmt.Errorf("read uncertified pipeline range before persist: %w", err)
	}
	if existing != nil && strings.TrimSpace(existing.FromSHA) != "" {
		inLineage, err := uncertifiedRangeStillInLineage(sctx, existing.ToSHA, fromSHA, toSHA)
		if err != nil {
			return nil, fmt.Errorf("verify uncertified pipeline range lineage before persist: %w", err)
		}
		if inLineage {
			fromSHA = existing.FromSHA
		}
	}
	plan, err := reconcileReviewFixSelection(existing, true)
	if err != nil {
		return nil, fmt.Errorf("reconcile review recovery before persist: %w", err)
	}
	if err := sctx.DB.UpsertUncertifiedPipelineRangeRecovery(sctx.Repo.ID, sctx.Run.Branch, fromSHA, toSHA, sctx.Run.ID, plan.state); err != nil {
		return nil, err
	}
	current := db.UncertifiedPipelineRange{
		RepoID:           sctx.Repo.ID,
		Branch:           sctx.Run.Branch,
		FromSHA:          fromSHA,
		ToSHA:            toSHA,
		SourceRunID:      sctx.Run.ID,
		RecoveryState:    plan.state,
		SelectionApplied: plan.selectionApplied,
	}
	rollback := func() error {
		restored, err := sctx.DB.RestoreUncertifiedPipelineRangeIfCurrent(current, existing)
		if err != nil {
			return err
		}
		if !restored {
			return fmt.Errorf("uncertified pipeline range changed before rollback")
		}
		return nil
	}
	return rollback, nil
}

func certifiedUncertifiedPipelineRange(ctx context.Context, database *db.DB, repoID, branch, approvedHead, workDir string) (*db.UncertifiedPipelineRange, error) {
	if database == nil {
		return nil, nil
	}
	rng, err := database.GetUncertifiedPipelineRange(repoID, branch)
	if err != nil {
		return nil, fmt.Errorf("read uncertified pipeline range before certification: %w", err)
	}
	if rng == nil {
		return nil, nil
	}
	approvedHead = strings.TrimSpace(approvedHead)
	if approvedHead == "" {
		return nil, fmt.Errorf("certify uncertified pipeline range: missing approved head")
	}
	if rng.ToSHA != approvedHead {
		inLineage, err := commitIsSelfOrAncestor(ctx, workDir, rng.ToSHA, approvedHead)
		if err != nil {
			return nil, fmt.Errorf("verify uncertified pipeline range before certification: %w", err)
		}
		if !inLineage {
			return nil, nil
		}
	}
	return rng, nil
}

// RemapUncertifiedPipelineRangeAfterRebase rewrites a persisted uncertified
// range onto the new head when rebase replaced a head that contained it.
func RemapUncertifiedPipelineRangeAfterRebase(sctx *StepContext, oldHead, newHead string) (func() error, error) {
	if sctx == nil || sctx.DB == nil || sctx.Repo == nil || sctx.Run == nil {
		return nil, fmt.Errorf("remap uncertified pipeline range: missing pipeline context")
	}
	oldHead = strings.TrimSpace(oldHead)
	newHead = strings.TrimSpace(newHead)
	if oldHead == "" || newHead == "" || oldHead == newHead {
		return nil, nil
	}
	rng, err := sctx.DB.GetUncertifiedPipelineRange(sctx.Repo.ID, sctx.Run.Branch)
	if err != nil {
		return nil, fmt.Errorf("read uncertified pipeline range before rebase remap: %w", err)
	}
	if rng == nil {
		return nil, nil
	}
	// oldHead and newHead are execution context the rebase step just observed,
	// so a git failure about either of them is a real fault and stays an error.
	oldInNew, err := commitIsSelfOrAncestor(sctx.Ctx, sctx.WorkDir, oldHead, newHead)
	if err != nil {
		return nil, fmt.Errorf("verify rebased head ancestry: %w", err)
	}
	if oldInNew {
		return nil, nil
	}
	// The persisted tip is stored provenance, not execution context: it can name
	// a commit this worktree no longer has. Ask the rebased head first, because
	// a tip still reachable there needs no remap at all.
	tipInNew, tipProvable, err := rangeTipAncestry(sctx, rng.ToSHA, newHead)
	if err != nil {
		return nil, fmt.Errorf("verify uncertified range against rebased head: %w", err)
	}
	if tipProvable && tipInNew {
		return nil, nil
	}
	tipInOld := false
	if tipProvable {
		tipInOld, tipProvable, err = rangeTipAncestry(sctx, rng.ToSHA, oldHead)
		if err != nil {
			return nil, fmt.Errorf("verify uncertified range against pre-rebase head: %w", err)
		}
	}
	newFrom, newTo, mapped := "", "", false
	if tipProvable && tipInOld {
		newFrom, newTo, mapped = remapRangeEndpoints(sctx, rng, oldHead, newHead)
	}
	current := *rng
	if mapped {
		current.FromSHA, current.ToSHA = newFrom, newTo
	} else {
		// Three unprovable shapes land here and they get one answer. The
		// endpoints may be unreadable in this worktree; they may be readable
		// but reach neither head, which the old code let stand as a silent
		// no-op that left a stale unrelated span persisted as if it still
		// described this branch; or distance may propose endpoints whose deltas
		// do not survive. Keep the historical findings and the source-run fix
		// budget, invalidate the selection's relationship to the rewritten
		// tree, and owe newHead a full review. Equal endpoints here mark that
		// debt, never a certificate: BindUncertifiedPipelineRange reads
		// ReviewRecoveryFreshReviewRequired as "no selection is applied", so a
		// fresh-review marker can never grant head approval.
		current = freshReviewRange(rng, newHead)
		if sctx.Log != nil {
			sctx.Log("uncertified review range cannot be proved after rebase; fresh review required on " + newHead)
		}
	}
	changed, err := sctx.DB.RestoreUncertifiedPipelineRangeIfCurrent(*rng, &current)
	if err != nil {
		return nil, fmt.Errorf("persist remapped uncertified pipeline range: %w", err)
	}
	if !changed {
		return nil, fmt.Errorf("uncertified pipeline range changed during rebase remap")
	}
	rollback := func() error {
		restored, err := sctx.DB.RestoreUncertifiedPipelineRangeIfCurrent(current, rng)
		if err != nil {
			return err
		}
		if !restored {
			return fmt.Errorf("uncertified pipeline range changed before rollback")
		}
		return nil
	}
	return rollback, nil
}

// freshReviewRange is the single shape of the fresh-review transition: keep the
// findings, the source run and its spent fix budget, drop every conclusion that
// depended on a mapping we can no longer prove, and point both endpoints at the
// head that now owes a full review.
func freshReviewRange(rng *db.UncertifiedPipelineRange, head string) db.UncertifiedPipelineRange {
	fresh := *rng
	fresh.FromSHA, fresh.ToSHA = head, head
	fresh.RecoveryState = db.ReviewRecoveryFreshReviewRequired
	fresh.SelectionApplied = false
	return fresh
}

// rangeTipAncestry answers an ancestry question whose subject is a PERSISTED
// range tip rather than a head this run just observed. Git reports a commit it
// does not have with exit 128, exactly as it reports a directory that is not a
// repository, and the old code surfaced both as a step error - which stranded
// every rebase whose reviewed head predated the current default branch.
//
// provable is false when the question cannot be answered about the tip; the
// caller routes that to the fresh-review transition, which is strictly more
// review and never less. An error is returned only for a genuine fault: a
// cancelled or expired context, or an execution context that cannot answer the
// question at all. The head is the discriminator - it is caller-supplied and
// must resolve, so a head that does not resolve means the worktree is the
// problem, not the stored provenance.
func rangeTipAncestry(sctx *StepContext, tip, head string) (inLineage bool, provable bool, err error) {
	if sctx == nil {
		return false, false, fmt.Errorf("commit ancestry requires pipeline context")
	}
	inLineage, err = commitIsSelfOrAncestor(sctx.Ctx, sctx.WorkDir, tip, head)
	if err == nil {
		return inLineage, true, nil
	}
	if fault := ancestryContextFault(sctx.Ctx, err); fault != nil {
		return false, false, fault
	}
	if _, headErr := resolveCommitObject(sctx.Ctx, sctx.WorkDir, head); headErr != nil {
		return false, false, err
	}
	return false, false, nil
}

// ancestryContextFault reports the cancellation or deadline that ended a git
// probe, so a cancelled run never mistakes its own shutdown for unprovable
// provenance and silently rewrites a range on the way out.
func ancestryContextFault(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func resolveCommitObject(ctx context.Context, workDir, sha string) (string, error) {
	sha = strings.TrimSpace(sha)
	if sha == "" || workDir == "" {
		return "", fmt.Errorf("invalid commit resolution request")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	out, err := git.Run(ctx, workDir, "rev-parse", "--verify", "--quiet", sha+"^{commit}")
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", fmt.Errorf("commit %s not present", sha)
	}
	return out, nil
}

// Distance proposes endpoints; it is not proof. Rebase may drop commits already
// upstream or resolve conflicts differently. Exact deltas for the range AND
// its suffix must survive. Conservative rejection costs a fresh review only.
func remapRangeEndpoints(sctx *StepContext, rng *db.UncertifiedPipelineRange, oldHead, newHead string) (string, string, bool) {
	fromBehind, err := commitBehindCount(sctx.Ctx, sctx.WorkDir, rng.FromSHA, oldHead)
	if err != nil {
		return "", "", false
	}
	toBehind, err := commitBehindCount(sctx.Ctx, sctx.WorkDir, rng.ToSHA, oldHead)
	if err != nil {
		return "", "", false
	}
	from, err := commitNthAncestor(sctx.Ctx, sctx.WorkDir, newHead, fromBehind)
	if err != nil {
		return "", "", false
	}
	to, err := commitNthAncestor(sctx.Ctx, sctx.WorkDir, newHead, toBehind)
	if err != nil {
		return "", "", false
	}
	for _, pair := range [][4]string{{rng.FromSHA, rng.ToSHA, from, to}, {rng.ToSHA, oldHead, to, newHead}} {
		before, err := git.Run(sctx.Ctx, sctx.WorkDir, "diff", "--no-ext-diff", "--no-textconv", "--binary", pair[0], pair[1], "--")
		if err != nil {
			return "", "", false
		}
		after, err := git.Run(sctx.Ctx, sctx.WorkDir, "diff", "--no-ext-diff", "--no-textconv", "--binary", pair[2], pair[3], "--")
		if err != nil || before != after {
			return "", "", false
		}
	}
	return from, to, true
}

func uncertifiedRangeStillInLineage(sctx *StepContext, existingTo, newFrom, newTo string) (bool, error) {
	if sctx == nil {
		return false, fmt.Errorf("missing pipeline context")
	}
	inFrom, err := commitIsSelfOrAncestor(sctx.Ctx, sctx.WorkDir, existingTo, newFrom)
	if err != nil || inFrom {
		return inFrom, err
	}
	return commitIsSelfOrAncestor(sctx.Ctx, sctx.WorkDir, existingTo, newTo)
}

func commitBehindCount(ctx context.Context, workDir, ancestor, descendent string) (int, error) {
	inLineage, err := commitIsSelfOrAncestor(ctx, workDir, ancestor, descendent)
	if err != nil {
		return 0, err
	}
	if !inLineage {
		return 0, fmt.Errorf("%s is not an ancestor of %s", ancestor, descendent)
	}
	if strings.TrimSpace(ancestor) == strings.TrimSpace(descendent) {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	out, err := git.Run(ctx, workDir, "rev-list", "--count", ancestor+".."+descendent)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid commit distance %q", out)
	}
	return n, nil
}

func commitNthAncestor(ctx context.Context, workDir, sha string, n int) (string, error) {
	sha = strings.TrimSpace(sha)
	if sha == "" || n < 0 || workDir == "" {
		return "", fmt.Errorf("invalid commit ancestor request")
	}
	if n == 0 {
		return sha, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	out, err := git.Run(ctx, workDir, "rev-parse", "--verify", fmt.Sprintf("%s~%d", sha, n))
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", fmt.Errorf("resolved empty commit ancestor")
	}
	return out, nil
}

func warnUncertifiedRangeSkipped(sctx *StepContext, rng *db.UncertifiedPipelineRange, format string) {
	msg := fmt.Sprintf(format, rng.FromSHA, rng.ToSHA)
	slog.Warn(msg, "repo_id", sctx.Repo.ID, "branch", sctx.Run.Branch)
	if sctx.Log != nil {
		sctx.Log("warning: " + msg)
	}
}

func commitIsSelfOrAncestor(ctx context.Context, workDir, ancestor, descendent string) (bool, error) {
	ancestor = strings.TrimSpace(ancestor)
	descendent = strings.TrimSpace(descendent)
	if ancestor == "" || descendent == "" || workDir == "" {
		return false, fmt.Errorf("commit ancestry requires worktree and two commits")
	}
	if ancestor == descendent {
		return true, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := git.Run(ctx, workDir, "merge-base", "--is-ancestor", ancestor, descendent)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

type uncertifiedReviewStore interface {
	GetStepsByRun(string) ([]*db.StepResult, error)
	GetRoundsByStep(string) ([]*db.StepRound, error)
	GetLatestStepRoundSelection(string) (*string, error)
}

func loadUncertifiedPriorReview(database uncertifiedReviewStore, sourceRunID string, selectionApplied bool, snapshotFindings, snapshotSelection *string) ([]*db.StepRound, string, string, string, error) {
	sourceRunID = strings.TrimSpace(sourceRunID)
	if database == nil || sourceRunID == "" {
		return nil, "", "", "", fmt.Errorf("load uncertified review: missing source run")
	}
	steps, err := database.GetStepsByRun(sourceRunID)
	if err != nil {
		return nil, "", "", "", fmt.Errorf("read uncertified source-run steps: %w", err)
	}
	for _, step := range steps {
		if step.StepName != types.StepReview {
			continue
		}
		findings := ""
		lineages := ""
		findingsSource := step.FindingsJSON
		if snapshotFindings != nil {
			findingsSource = snapshotFindings
		}
		if findingsSource != nil {
			findings = *findingsSource
			if _, err := types.ParseFindingsJSON(findings); err != nil {
				return nil, "", "", "", fmt.Errorf("read uncertified source-run findings: %w", err)
			}
			lineages = findings
		}
		selectedRaw := snapshotSelection
		if selectedRaw == nil {
			selectedRaw, err = database.GetLatestStepRoundSelection(step.ID)
			if err != nil {
				return nil, "", "", "", fmt.Errorf("read uncertified source-run selection: %w", err)
			}
		}
		selectedFindings := ""
		if selectedRaw != nil {
			var selected []string
			if err := json.Unmarshal([]byte(*selectedRaw), &selected); err != nil {
				return nil, "", "", "", fmt.Errorf("read uncertified source-run selection: %w", err)
			}
			selectedFindings = filterFindingsJSON(findings, selected)
			if selectionApplied {
				findings = excludeFindingsJSON(findings, selected)
			}
		}
		rounds, err := database.GetRoundsByStep(step.ID)
		if err != nil {
			slog.Warn("failed to read uncertified source-run review rounds", "run_id", sourceRunID, "error", err)
			return nil, findings, lineages, selectedFindings, nil
		}
		return rounds, findings, lineages, selectedFindings, nil
	}
	return nil, "", "", "", fmt.Errorf("uncertified source run %s has no review step", sourceRunID)
}
