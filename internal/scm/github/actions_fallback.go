package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Blakeolson21/no-slop/internal/scm"
)

const githubActionsAppID int64 = 15368

var (
	ErrRollupUnavailable        = fmt.Errorf("GitHub status check rollup is unreadable: %w", scm.ErrChecksUnavailable)
	ErrActionsEvidenceMissing   = fmt.Errorf("GitHub Actions evidence is missing: %w", scm.ErrChecksUnavailable)
	ErrActionsEvidenceAmbiguous = fmt.Errorf("GitHub Actions evidence is ambiguous: %w", scm.ErrChecksUnavailable)
	ErrActionsHeadMismatch      = fmt.Errorf("GitHub Actions evidence does not belong to the head under test: %w", scm.ErrChecksUnavailable)
	ErrActionsAPIFailure        = fmt.Errorf("GitHub Actions API read failed: %w", scm.ErrChecksUnavailable)
)

var rollupUnavailableMarkers = []string{
	"resource not accessible",
	"insufficient scopes",
	"not authorized to read",
	"http 403",
	"403 forbidden",
	"statuscheckrollup",
}

func rollupReadError(stage, output string, err error) error {
	trimmed := strings.TrimSpace(output)
	if classifyRollupUnavailable(trimmed, err) {
		return fmt.Errorf("%s: %s: %w: %w", stage, trimmed, err, ErrRollupUnavailable)
	}
	if trimmed == "" {
		return fmt.Errorf("%s: %w", stage, err)
	}
	return fmt.Errorf("%s: %s: %w", stage, trimmed, err)
}

func classifyRollupUnavailable(output string, err error) bool {
	haystack := strings.ToLower(output)
	if err != nil {
		haystack += "\n" + strings.ToLower(err.Error())
	}
	for _, marker := range rollupUnavailableMarkers {
		if strings.Contains(haystack, marker) {
			return true
		}
	}
	return false
}

type fallbackWorkflowRun struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_title"`
	HeadSHA     string `json:"head_sha"`
	RunAttempt  int    `json:"run_attempt"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	UpdatedAt   string `json:"updated_at"`
	HTMLURL     string `json:"html_url"`
}

type fallbackWorkflowJob struct {
	ID          int64  `json:"id"`
	RunID       int64  `json:"run_id"`
	RunAttempt  int    `json:"run_attempt"`
	HeadSHA     string `json:"head_sha"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	CompletedAt string `json:"completed_at"`
	HTMLURL     string `json:"html_url"`
}

func (h *Host) getActionsFallbackChecks(ctx context.Context, pr *scm.PR, headSHA string) ([]scm.Check, error) {
	headSHA = strings.TrimSpace(headSHA)
	if headSHA == "" || h.repoSlug() == "" {
		return nil, fmt.Errorf("missing repository or head binding: %w", ErrActionsEvidenceMissing)
	}
	runs, err := h.listFallbackWorkflowRuns(ctx, headSHA)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", err, ErrActionsAPIFailure)
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("no Actions run for %s: %w", headSHA, ErrActionsEvidenceMissing)
	}
	var checks []scm.Check
	for _, run := range runs {
		if !sameCommit(run.HeadSHA, headSHA) || run.RunAttempt <= 0 {
			return nil, fmt.Errorf("workflow run %d is not the current exact-head attempt: %w", run.ID, ErrActionsHeadMismatch)
		}
		jobs, err := h.listFallbackWorkflowJobs(ctx, run.ID)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", err, ErrActionsAPIFailure)
		}
		runChecks, err := h.fallbackChecksForRun(run, jobs, headSHA)
		if err != nil {
			return nil, err
		}
		checks = append(checks, runChecks...)
	}
	if len(checks) == 0 {
		return nil, fmt.Errorf("no Actions result for %s: %w", headSHA, ErrActionsEvidenceMissing)
	}
	if actionsEvidenceWouldCertify(checks) {
		if err := h.assertRequiredActionsChecksMapped(ctx, pr, checks); err != nil {
			return nil, err
		}
	}
	return checks, nil
}

func (h *Host) listFallbackWorkflowRuns(ctx context.Context, headSHA string) ([]fallbackWorkflowRun, error) {
	endpoint := "repos/" + h.repoSlug() + "/actions/runs"
	args := []string{"api"}
	if h.host != "" {
		args = append(args, "--hostname", h.host)
	}
	args = append(args, "--method", "GET", endpoint, "-f", "head_sha="+headSHA, "-f", "per_page=100", "--paginate", "--slurp")
	out, err := h.cmd(ctx, "gh", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh api workflow runs: %s: %w", strings.TrimSpace(string(out)), err)
	}
	var pages []struct {
		TotalCount   *int                  `json:"total_count"`
		WorkflowRuns []fallbackWorkflowRun `json:"workflow_runs"`
	}
	if err := json.Unmarshal(out, &pages); err != nil {
		return nil, fmt.Errorf("parse workflow runs: %w", err)
	}
	var runs []fallbackWorkflowRun
	if err := validateFallbackPages("workflow run", "runs", pages, func(page int, run fallbackWorkflowRun, seen map[int64]struct{}) error {
		if run.ID == 0 {
			return fmt.Errorf("workflow run page %d contains an id-less run", page)
		}
		if _, ok := seen[run.ID]; ok {
			return fmt.Errorf("workflow run %d appears more than once", run.ID)
		}
		seen[run.ID] = struct{}{}
		runs = append(runs, run)
		return nil
	}, func(p struct {
		TotalCount   *int                  `json:"total_count"`
		WorkflowRuns []fallbackWorkflowRun `json:"workflow_runs"`
	}) (*int, []fallbackWorkflowRun) {
		return p.TotalCount, p.WorkflowRuns
	}); err != nil {
		return nil, err
	}
	return runs, nil
}

// validateFallbackPages centralizes the safety property shared by run/job
// discovery: a short, duplicate, or count-inconsistent listing is unavailable,
// because a missing failing item is the one way pagination could invent green.
func validateFallbackPages[P any, T any](pageLabel, itemLabel string, pages []P, visit func(int, T, map[int64]struct{}) error, unpack func(P) (*int, []T)) error {
	if len(pages) == 0 {
		return errors.New("discovery returned no pages")
	}
	total := -1
	seen := map[int64]struct{}{}
	for i, page := range pages {
		count, items := unpack(page)
		if count == nil || *count < 0 {
			return fmt.Errorf("%s page %d has no valid total_count", pageLabel, i+1)
		}
		if total == -1 {
			total = *count
		} else if total != *count {
			return fmt.Errorf("%s page %d total_count is %d, want %d", pageLabel, i+1, *count, total)
		}
		for _, item := range items {
			if err := visit(i+1, item, seen); err != nil {
				return err
			}
		}
	}
	if len(seen) != total {
		return fmt.Errorf("%s discovery returned %d unique %s, want %d", pageLabel, len(seen), itemLabel, total)
	}
	return nil
}

func (h *Host) listFallbackWorkflowJobs(ctx context.Context, runID int64) ([]fallbackWorkflowJob, error) {
	endpoint := fmt.Sprintf("repos/%s/actions/runs/%d/jobs", h.repoSlug(), runID)
	args := []string{"api"}
	if h.host != "" {
		args = append(args, "--hostname", h.host)
	}
	args = append(args, "--method", "GET", endpoint, "-f", "filter=latest", "-f", "per_page=100", "--paginate", "--slurp")
	out, err := h.cmd(ctx, "gh", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh api jobs for run %d: %s: %w", runID, strings.TrimSpace(string(out)), err)
	}
	var pages []struct {
		TotalCount *int                  `json:"total_count"`
		Jobs       []fallbackWorkflowJob `json:"jobs"`
	}
	if err := json.Unmarshal(out, &pages); err != nil {
		return nil, fmt.Errorf("parse jobs for run %d: %w", runID, err)
	}
	var jobs []fallbackWorkflowJob
	if err := validateFallbackPages("job", "jobs", pages, func(page int, job fallbackWorkflowJob, seen map[int64]struct{}) error {
		if job.ID == 0 {
			return fmt.Errorf("job page %d contains an id-less job", page)
		}
		if _, ok := seen[job.ID]; ok {
			return fmt.Errorf("job %d appears more than once", job.ID)
		}
		seen[job.ID] = struct{}{}
		jobs = append(jobs, job)
		return nil
	}, func(p struct {
		TotalCount *int                  `json:"total_count"`
		Jobs       []fallbackWorkflowJob `json:"jobs"`
	}) (*int, []fallbackWorkflowJob) {
		return p.TotalCount, p.Jobs
	}); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (h *Host) fallbackChecksForRun(run fallbackWorkflowRun, jobs []fallbackWorkflowJob, headSHA string) ([]scm.Check, error) {
	if len(jobs) == 0 {
		bucket := normalizeCheckBucket("", run.Conclusion)
		if !strings.EqualFold(run.Status, "completed") {
			bucket = scm.CheckBucketPending
		} else if bucket == "" || bucket == scm.CheckBucketPass {
			return nil, fmt.Errorf("successful workflow run %d has no readable job: %w", run.ID, ErrActionsEvidenceMissing)
		}
		return []scm.Check{{Name: fallbackRunName(run), Bucket: bucket, State: fallbackState(run.Conclusion, run.Status), CompletedAt: parseFallbackTime(run.UpdatedAt), Link: run.HTMLURL}}, nil
	}
	checks := make([]scm.Check, 0, len(jobs))
	for _, job := range jobs {
		if job.RunID != 0 && job.RunID != run.ID || job.RunAttempt != run.RunAttempt || strings.TrimSpace(job.HeadSHA) != "" && !sameCommit(job.HeadSHA, headSHA) {
			return nil, fmt.Errorf("job %d is not from exact run/head attempt: %w", job.ID, ErrActionsHeadMismatch)
		}
		if strings.TrimSpace(job.Name) == "" {
			return nil, fmt.Errorf("job %d has no check identity: %w", job.ID, ErrActionsEvidenceMissing)
		}
		bucket := normalizeCheckBucket("", job.Conclusion)
		if !strings.EqualFold(job.Status, "completed") || bucket == "" {
			bucket = scm.CheckBucketPending
		}
		checks = append(checks, scm.Check{Name: strings.TrimSpace(job.Name), Bucket: bucket, State: fallbackState(job.Conclusion, job.Status), CompletedAt: parseFallbackTime(job.CompletedAt), Link: strings.TrimSpace(job.HTMLURL)})
	}
	return checks, nil
}

type requiredCheckIdentity struct {
	Context string
	AppID   *int64
}

func (h *Host) assertRequiredActionsChecksMapped(ctx context.Context, pr *scm.PR, checks []scm.Check) error {
	base, err := h.getPRBaseBranch(ctx, pr)
	if err != nil {
		return fmt.Errorf("%w: %w", err, ErrActionsEvidenceMissing)
	}
	required, err := h.requiredCheckIdentities(ctx, base)
	if err != nil {
		return fmt.Errorf("%w: %w", err, ErrActionsEvidenceMissing)
	}
	if len(required) == 0 {
		return fmt.Errorf("branch %q has no readable required checks: %w", base, ErrActionsEvidenceMissing)
	}
	seen := map[string]int{}
	for _, check := range checks {
		seen[check.Name]++
	}
	for _, identity := range required {
		if identity.AppID != nil && *identity.AppID != -1 && *identity.AppID != githubActionsAppID {
			return fmt.Errorf("required check %q is bound to GitHub App %d, not Actions: %w", identity.Context, *identity.AppID, ErrActionsEvidenceMissing)
		}
		switch seen[identity.Context] {
		case 1:
		case 0:
			return fmt.Errorf("required check %q has no Actions mapping: %w", identity.Context, ErrActionsEvidenceMissing)
		default:
			return fmt.Errorf("required check %q has %d Actions mappings: %w", identity.Context, seen[identity.Context], ErrActionsEvidenceAmbiguous)
		}
	}
	return nil
}

func (h *Host) getPRBaseBranch(ctx context.Context, pr *scm.PR) (string, error) {
	selector, err := prSelector(pr)
	if err != nil {
		return "", err
	}
	args := append([]string{"pr", "view", selector}, h.repoArgs()...)
	args = append(args, "--json", "baseRefName", "--jq", ".baseRefName")
	out, err := h.cmd(ctx, "gh", args...).Output()
	if err != nil {
		return "", fmt.Errorf("gh pr view base branch: %w", err)
	}
	base := strings.TrimSpace(string(out))
	if base == "" {
		return "", errors.New("gh pr view returned an empty base branch")
	}
	return base, nil
}

func (h *Host) requiredCheckIdentities(ctx context.Context, branch string) ([]requiredCheckIdentity, error) {
	endpoint := fmt.Sprintf("repos/%s/branches/%s/protection/required_status_checks", h.repoSlug(), url.PathEscape(branch))
	args := []string{"api"}
	if h.host != "" {
		args = append(args, "--hostname", h.host)
	}
	args = append(args, "--method", "GET", endpoint)
	out, err := h.cmd(ctx, "gh", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh api required checks for branch %q: %s: %w", branch, strings.TrimSpace(string(out)), err)
	}
	var payload struct {
		Contexts []string `json:"contexts"`
		Checks   []struct {
			Context string `json:"context"`
			AppID   *int64 `json:"app_id"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, fmt.Errorf("parse required checks for branch %q: %w", branch, err)
	}
	unique := map[string]requiredCheckIdentity{}
	for _, context := range payload.Contexts {
		context = strings.TrimSpace(context)
		if context != "" {
			unique[context+"\x00*"] = requiredCheckIdentity{Context: context}
		}
	}
	for _, check := range payload.Checks {
		context := strings.TrimSpace(check.Context)
		if context == "" {
			continue
		}
		key := context + "\x00*"
		if check.AppID != nil {
			key = fmt.Sprintf("%s\x00%d", context, *check.AppID)
		}
		unique[key] = requiredCheckIdentity{Context: context, AppID: check.AppID}
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]requiredCheckIdentity, 0, len(keys))
	for _, key := range keys {
		result = append(result, unique[key])
	}
	return result, nil
}

func actionsEvidenceWouldCertify(checks []scm.Check) bool {
	if len(checks) == 0 {
		return false
	}
	for _, check := range checks {
		if check.Bucket != scm.CheckBucketPass && check.Bucket != scm.CheckBucketSkip {
			return false
		}
	}
	return true
}

func fallbackRunName(run fallbackWorkflowRun) string {
	if strings.TrimSpace(run.Name) != "" {
		return strings.TrimSpace(run.Name)
	}
	if strings.TrimSpace(run.DisplayName) != "" {
		return strings.TrimSpace(run.DisplayName)
	}
	return "GitHub Actions workflow"
}

func fallbackState(conclusion, status string) string {
	if strings.TrimSpace(conclusion) != "" {
		return strings.ToUpper(strings.TrimSpace(conclusion))
	}
	return strings.ToUpper(strings.TrimSpace(status))
}

func parseFallbackTime(raw string) time.Time {
	parsed, _ := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	return parsed
}

func sameCommit(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
