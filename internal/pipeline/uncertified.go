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

// BindUncertifiedPipelineRange copies a persisted uncertified fixer range
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
	head := strings.TrimSpace(sctx.Run.HeadSHA)
	if head == "" {
		head = strings.TrimSpace(sctx.ReviewStartingHeadSHA)
	}
	inLineage, err := commitIsSelfOrAncestor(sctx.Ctx, sctx.WorkDir, rng.ToSHA, head)
	if err != nil {
		return fmt.Errorf("verify uncertified pipeline range ancestry: %w", err)
	}
	if !inLineage {
		warnUncertifiedRangeSkipped(sctx, rng, "uncertified range %s..%s not in gate; not applying provenance")
		return nil
	}
	priorRounds, priorFindings, priorLineages, err := loadUncertifiedPriorReview(sctx.DB, rng.SourceRunID)
	if err != nil {
		return err
	}
	sctx.UncertifiedFromSHA = rng.FromSHA
	sctx.UncertifiedToSHA = rng.ToSHA
	sctx.UncertifiedSourceRunID = rng.SourceRunID
	sctx.UncertifiedPriorRounds = priorRounds
	sctx.UncertifiedPriorFindings = priorFindings
	sctx.UncertifiedPriorLineages = priorLineages
	return nil
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
	if err := sctx.DB.UpsertUncertifiedPipelineRange(sctx.Repo.ID, sctx.Run.Branch, fromSHA, toSHA, sctx.Run.ID); err != nil {
		return nil, err
	}
	current := db.UncertifiedPipelineRange{
		RepoID:      sctx.Repo.ID,
		Branch:      sctx.Run.Branch,
		FromSHA:     fromSHA,
		ToSHA:       toSHA,
		SourceRunID: sctx.Run.ID,
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
	oldInNew, err := commitIsSelfOrAncestor(sctx.Ctx, sctx.WorkDir, oldHead, newHead)
	if err != nil {
		return nil, fmt.Errorf("verify rebased head ancestry: %w", err)
	}
	if oldInNew {
		return nil, nil
	}
	rangeInOld, err := commitIsSelfOrAncestor(sctx.Ctx, sctx.WorkDir, rng.ToSHA, oldHead)
	if err != nil {
		return nil, fmt.Errorf("verify uncertified range against pre-rebase head: %w", err)
	}
	if !rangeInOld {
		return nil, nil
	}
	rangeInNew, err := commitIsSelfOrAncestor(sctx.Ctx, sctx.WorkDir, rng.ToSHA, newHead)
	if err != nil {
		return nil, fmt.Errorf("verify uncertified range against rebased head: %w", err)
	}
	if rangeInNew {
		return nil, nil
	}
	fromBehind, err := commitBehindCount(sctx.Ctx, sctx.WorkDir, rng.FromSHA, oldHead)
	if err != nil {
		return nil, fmt.Errorf("map uncertified range start %s after rebase: %w", rng.FromSHA, err)
	}
	toBehind, err := commitBehindCount(sctx.Ctx, sctx.WorkDir, rng.ToSHA, oldHead)
	if err != nil {
		return nil, fmt.Errorf("map uncertified range end %s after rebase: %w", rng.ToSHA, err)
	}
	newFrom, err := commitNthAncestor(sctx.Ctx, sctx.WorkDir, newHead, fromBehind)
	if err != nil {
		return nil, fmt.Errorf("resolve remapped uncertified range start after rebase: %w", err)
	}
	newTo, err := commitNthAncestor(sctx.Ctx, sctx.WorkDir, newHead, toBehind)
	if err != nil || newFrom == "" || newTo == "" || newFrom == newTo {
		return nil, fmt.Errorf("resolve remapped uncertified range end after rebase")
	}
	if err := sctx.DB.UpsertUncertifiedPipelineRange(sctx.Repo.ID, sctx.Run.Branch, newFrom, newTo, rng.SourceRunID); err != nil {
		return nil, fmt.Errorf("persist remapped uncertified pipeline range: %w", err)
	}
	current := db.UncertifiedPipelineRange{RepoID: rng.RepoID, Branch: rng.Branch, FromSHA: newFrom, ToSHA: newTo, SourceRunID: rng.SourceRunID}
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

func loadUncertifiedPriorReview(database uncertifiedReviewStore, sourceRunID string) ([]*db.StepRound, string, string, error) {
	sourceRunID = strings.TrimSpace(sourceRunID)
	if database == nil || sourceRunID == "" {
		return nil, "", "", fmt.Errorf("load uncertified review: missing source run")
	}
	steps, err := database.GetStepsByRun(sourceRunID)
	if err != nil {
		return nil, "", "", fmt.Errorf("read uncertified source-run steps: %w", err)
	}
	for _, step := range steps {
		if step.StepName != types.StepReview {
			continue
		}
		findings := ""
		lineages := ""
		if step.FindingsJSON != nil {
			findings = *step.FindingsJSON
			if _, err := types.ParseFindingsJSON(findings); err != nil {
				return nil, "", "", fmt.Errorf("read uncertified source-run findings: %w", err)
			}
			lineages = findings
		}
		selectedRaw, err := database.GetLatestStepRoundSelection(step.ID)
		if err != nil {
			return nil, "", "", fmt.Errorf("read uncertified source-run selection: %w", err)
		}
		if selectedRaw != nil {
			var selected []string
			if err := json.Unmarshal([]byte(*selectedRaw), &selected); err != nil {
				return nil, "", "", fmt.Errorf("read uncertified source-run selection: %w", err)
			}
			findings = excludeFindingsJSON(findings, selected)
		}
		rounds, err := database.GetRoundsByStep(step.ID)
		if err != nil {
			slog.Warn("failed to read uncertified source-run review rounds", "run_id", sourceRunID, "error", err)
			return nil, findings, lineages, nil
		}
		return rounds, findings, lineages, nil
	}
	return nil, "", "", fmt.Errorf("uncertified source run %s has no review step", sourceRunID)
}
