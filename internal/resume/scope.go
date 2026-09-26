// Package resume checks the immutable candidate scope for a proposed repair.
// A scope check is not authority to resume a run or reuse its test evidence.
package resume

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/git"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// Scope describes only the ancestry and changed-file checks. Base freshness,
// test relevance, receipt verification, and applying a resume are separate work.
type Scope struct {
	CandidateSHA string
	HeadSHA      string
	ChangedFiles []string
}

// CheckScope reads commits from dir without fetching, moving refs, or writing
// run state. Callers must supply the run and its stored review from the same
// registered repository. All returned facts are advisory snapshots; an eventual
// mutation must revalidate them inside its admission protocol.
func CheckScope(ctx context.Context, dir string, run *db.Run, review *db.StepResult, head string) (Scope, error) {
	if run == nil || run.Status != types.RunRunning {
		return Scope{}, fmt.Errorf("run must be running and parked at review")
	}
	if review == nil || review.RunID != run.ID || review.StepName != types.StepReview ||
		(review.Status != types.StepStatusParkedForApproval && review.Status != types.StepStatusParkedAfterFix) {
		return Scope{}, fmt.Errorf("run must be parked at review")
	}
	candidate := ""
	if run.ReviewApprovedHeadSHA != nil && *run.ReviewApprovedHeadSHA != "" {
		candidate = *run.ReviewApprovedHeadSHA
	} else if run.SubmittedHeadSHA != nil {
		candidate = *run.SubmittedHeadSHA
	}
	if !fullObjectID(candidate) {
		return Scope{}, fmt.Errorf("run has no valid stored approved or submitted candidate")
	}
	if !fullObjectID(head) {
		return Scope{}, fmt.Errorf("head must be a full commit SHA")
	}
	if review.FindingsJSON == nil {
		return Scope{}, fmt.Errorf("review has no stored findings")
	}
	findings, err := types.ParseFindingsJSON(*review.FindingsJSON)
	if err != nil {
		return Scope{}, fmt.Errorf("decode stored review findings: %w", err)
	}
	allowed := make(map[string]bool)
	for _, finding := range findings.Items {
		if finding.Severity == "error" && finding.File != "" {
			allowed[finding.File] = true
		}
	}
	if len(allowed) == 0 {
		return Scope{}, fmt.Errorf("review has no error findings naming files")
	}
	// Scope depends only on the stored object graph. Ignore replacement refs and
	// legacy grafts so local history overlays cannot manufacture ancestry, and
	// disable lazy fetching so missing objects fail closed without network or
	// credential-helper activity.
	for _, sha := range []string{candidate, head} {
		resolved, err := scopeGitRun(ctx, dir, "rev-parse", "--verify", sha+"^{commit}")
		if err != nil || resolved != sha {
			return Scope{}, fmt.Errorf("commit %s is unavailable or is not a commit", sha)
		}
	}
	if _, err := scopeGitRun(ctx, dir, "merge-base", "--is-ancestor", candidate, head); err != nil {
		return Scope{}, fmt.Errorf("cannot prove head descends from stored candidate: %w", err)
	}
	// No rename detection: a rename must authorize both the deletion and the
	// addition. NUL delimiters preserve whitespace and newlines in file names.
	raw, err := git.OutputWithEnv(ctx, dir, scopeGitEnvironment(), "--no-replace-objects", "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--ignore-submodules=none", "--name-only", "-z", candidate, head, "--")
	if err != nil {
		return Scope{}, fmt.Errorf("compare candidate to head: %w", err)
	}
	if raw == "" {
		return Scope{}, fmt.Errorf("head has no file changes from stored candidate")
	}
	if !strings.HasSuffix(raw, "\x00") {
		return Scope{}, fmt.Errorf("invalid changed-file output")
	}
	files := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
	for _, file := range files {
		if !allowed[file] {
			return Scope{}, fmt.Errorf("changed file %q is not named by a review error finding", file)
		}
	}
	return Scope{CandidateSHA: candidate, HeadSHA: head, ChangedFiles: files}, nil
}

func scopeGitRun(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := git.OutputWithEnv(ctx, dir, scopeGitEnvironment(), append([]string{"--no-replace-objects"}, args...)...)
	return strings.TrimSpace(out), err
}

func scopeGitEnvironment() []string {
	return []string{
		"GIT_GRAFT_FILE=" + os.DevNull,
		"GIT_NO_LAZY_FETCH=1",
	}
}

func fullObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}
