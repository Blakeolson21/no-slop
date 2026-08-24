package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Blakeolson21/no-slop/internal/pipeline"
	"github.com/Blakeolson21/no-slop/internal/scm"
	"github.com/Blakeolson21/no-slop/internal/types"
)

const requiredAttestationCheckName = "PR must be raised via no-slop"

func (s *CIStep) filterExpectedStaleAttestationChecks(sctx *pipeline.StepContext, host scm.Host, checks []scm.Check) ([]scm.Check, error) {
	state := &s.expectedAttestation
	if state.HeadSHA == "" || state.HeadSHA != sctx.Run.HeadSHA {
		return checks, nil
	}
	if !validPublicationNonce(state.PublicationNonce) {
		return nil, fmt.Errorf("expected attestation boundary is missing")
	}
	reader, ok := host.(scm.CheckAttemptIdentityReader)
	if !ok {
		return nil, fmt.Errorf("provider cannot identify expected stale attestation check attempts")
	}
	identities := make(map[string]scm.CheckAttemptIdentity)
	publicationRunID := state.PublicationRunID
	for _, check := range checks {
		if check.Name != requiredAttestationCheckName {
			continue
		}
		identity, err := readCheckAttemptIdentity(sctx.Ctx, reader, check, identities)
		if err != nil {
			return nil, err
		}
		if identity.RunID <= 0 {
			return nil, fmt.Errorf("attestation check attempt has no immutable run identity")
		}
		if identity.HeadSHA == sctx.Run.HeadSHA && identity.PublicationNonce == state.PublicationNonce {
			if publicationRunID == 0 || identity.RunID < publicationRunID {
				publicationRunID = identity.RunID
			}
		}
	}
	if publicationRunID != 0 && state.PublicationRunID != publicationRunID {
		state.PublicationRunID = publicationRunID
		if err := persistExpectedAttestationState(sctx, *state); err != nil {
			return nil, fmt.Errorf("persist attestation publication run identity: %w", err)
		}
	}

	filtered := make([]scm.Check, 0, len(checks)+1)
	if publicationRunID == 0 {
		for _, check := range checks {
			if check.Name != requiredAttestationCheckName {
				filtered = append(filtered, check)
			}
		}
		filtered = append(filtered, scm.Check{Name: requiredAttestationCheckName, Bucket: scm.CheckBucketPending, State: "EXPECTED_ATTESTATION"})
		return filtered, nil
	}
	currentAttemptPresent := false
	for _, check := range checks {
		if check.Name != requiredAttestationCheckName {
			filtered = append(filtered, check)
			continue
		}
		identity, err := readCheckAttemptIdentity(sctx.Ctx, reader, check, identities)
		if err != nil {
			return nil, err
		}
		if identity.HeadSHA != sctx.Run.HeadSHA {
			continue
		}
		if identity.RunID < publicationRunID {
			continue
		}
		filtered = append(filtered, check)
		currentAttemptPresent = true
	}
	if !currentAttemptPresent {
		filtered = append(filtered, scm.Check{Name: requiredAttestationCheckName, Bucket: scm.CheckBucketPending, State: "EXPECTED_ATTESTATION"})
	}
	return filtered, nil
}

func readCheckAttemptIdentity(ctx context.Context, reader scm.CheckAttemptIdentityReader, check scm.Check, cache map[string]scm.CheckAttemptIdentity) (scm.CheckAttemptIdentity, error) {
	if identity, ok := cache[check.Link]; ok {
		return identity, nil
	}
	identity, err := reader.GetCheckAttemptIdentity(ctx, check)
	if err != nil {
		return scm.CheckAttemptIdentity{}, fmt.Errorf("identify attestation check attempt: %w", err)
	}
	cache[check.Link] = identity
	return identity, nil
}

type lastFixedIssues struct {
	Checks        []string `json:"checks,omitempty"`
	MergeConflict bool     `json:"mergeConflict,omitempty"`
}

// pollInterval returns the polling interval based on elapsed time since CI monitoring started.
// 30s for first 5min, 60s for 5-15min, 120s after.
func pollInterval(elapsed time.Duration) time.Duration {
	switch {
	case elapsed < 5*time.Minute:
		return 30 * time.Second
	case elapsed < 15*time.Minute:
		return 60 * time.Second
	default:
		return 120 * time.Second
	}
}

// hasFailingChecks returns true if any CI check is in the fail bucket.
func hasFailingChecks(checks []scm.Check) bool {
	for _, c := range checks {
		if c.Failing() {
			return true
		}
	}
	return false
}

// hasPendingChecks returns true if any CI check is still running or queued.
func hasPendingChecks(checks []scm.Check) bool {
	for _, c := range checks {
		if c.Pending() {
			return true
		}
	}
	return false
}

func hasUnresolvedChecks(checks []scm.Check) bool {
	for _, c := range checks {
		switch c.Bucket {
		case scm.CheckBucketPass, scm.CheckBucketFail, scm.CheckBucketSkip:
		default:
			return true
		}
	}
	return false
}

func allChecksPassed(checks []scm.Check) bool {
	if len(checks) == 0 {
		return false
	}
	for _, c := range checks {
		if c.Bucket != scm.CheckBucketPass && c.Bucket != scm.CheckBucketSkip {
			return false
		}
	}
	return true
}

// failingCheckNames returns the names of failing checks.
func failingCheckNames(checks []scm.Check) []string {
	var names []string
	for _, c := range checks {
		if c.Failing() {
			names = append(names, c.Name)
		}
	}
	return names
}

// terminalFailureCompletionTimes snapshots when each terminally failed check
// finished, so a later poll can tell that CI has re-run since the fix push.
//
// It covers the whole terminal-failure set rather than just the fail bucket
// because a cancelled check can be a fix target too (see the CI step's
// fixTargets). Keying the snapshot on the fail bucket alone would leave a
// cancelled-only fix round with no completion evidence at all, and the step
// would then have no way to notice its own re-run.
func terminalFailureCompletionTimes(checks []scm.Check) map[string]time.Time {
	completedAt := make(map[string]time.Time)
	for _, c := range checks {
		if !checkFailedTerminally(c) {
			continue
		}
		if c.CompletedAt.IsZero() {
			continue
		}
		previous := completedAt[c.Name]
		if previous.IsZero() || c.CompletedAt.After(previous) {
			completedAt[c.Name] = c.CompletedAt
		}
	}
	if len(completedAt) == 0 {
		return nil
	}
	return completedAt
}

func terminalFailureCompletedAfter(checks []scm.Check, after map[string]time.Time) bool {
	if len(after) == 0 {
		return false
	}
	for _, c := range checks {
		if !checkFailedTerminally(c) || c.CompletedAt.IsZero() {
			continue
		}
		previous, ok := after[c.Name]
		if ok && c.CompletedAt.After(previous) {
			return true
		}
	}
	return false
}

func pendingCheckMatchesLastFixed(checks []scm.Check, lastFixedChecks string) bool {
	issues, ok := decodeLastFixedChecks(lastFixedChecks)
	if !ok {
		return false
	}

	failedNames := map[string]struct{}{}
	for _, name := range issues.Checks {
		if name == "" {
			continue
		}
		failedNames[name] = struct{}{}
	}
	if len(failedNames) == 0 {
		return issues.MergeConflict && hasPendingChecks(checks)
	}

	for _, c := range checks {
		if !c.Pending() {
			continue
		}
		if _, ok := failedNames[c.Name]; ok {
			return true
		}
	}

	return false
}

func encodeLastFixedChecks(failing []string, mergeConflict bool) string {
	if len(failing) == 0 && !mergeConflict {
		return ""
	}
	encoded, err := json.Marshal(lastFixedIssues{Checks: failing, MergeConflict: mergeConflict})
	if err != nil {
		return ""
	}
	return string(encoded)
}

func decodeLastFixedChecks(raw string) (lastFixedIssues, bool) {
	if raw == "" {
		return lastFixedIssues{}, false
	}
	var issues lastFixedIssues
	if err := json.Unmarshal([]byte(raw), &issues); err != nil {
		return lastFixedIssues{}, false
	}
	if len(issues.Checks) == 0 && !issues.MergeConflict {
		return lastFixedIssues{}, false
	}
	return issues, true
}

func ciFailureOutcome(failing []string, mergeConflict bool, summary string) *pipeline.StepOutcome {
	findings := Findings{Summary: summary}
	for _, name := range failing {
		findings.Items = append(findings.Items, Finding{
			Severity:    "warning",
			Description: fmt.Sprintf("CI check failing: %s", name),
		})
	}
	if mergeConflict {
		findings.Items = append(findings.Items, Finding{
			Severity:    "warning",
			Description: "PR has merge conflicts with the base branch",
		})
	}
	findingsJSON, _ := json.Marshal(findings)
	return &pipeline.StepOutcome{
		NeedsApproval: true,
		Findings:      string(findingsJSON),
	}
}

func ciMergeabilityOutcome(summary, description string) *pipeline.StepOutcome {
	findings := Findings{
		Summary: summary,
		Items: []Finding{{
			Severity:    "warning",
			Description: description,
			Action:      types.ActionAskUser,
		}},
	}
	findingsJSON, _ := json.Marshal(findings)
	return &pipeline.StepOutcome{
		NeedsApproval: true,
		Findings:      string(findingsJSON),
	}
}

func ciMonitoringTimeoutOutcome() *pipeline.StepOutcome {
	findings := Findings{
		Summary: "CI monitoring timed out before PR was merged or closed",
		Items: []Finding{{
			Severity:    "warning",
			Description: "PR was still open when CI monitoring timed out",
			Action:      types.ActionAskUser,
		}},
	}
	findingsJSON, _ := json.Marshal(findings)
	return &pipeline.StepOutcome{
		NeedsApproval: true,
		Findings:      string(findingsJSON),
	}
}
