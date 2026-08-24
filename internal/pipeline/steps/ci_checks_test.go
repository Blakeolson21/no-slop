package steps

import (
	"context"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/scm"
)

type attestationIdentityHost struct {
	recordingPRUpdateHost
	identities map[string]scm.CheckAttemptIdentity
}

func (h *attestationIdentityHost) GetCheckAttemptIdentity(_ context.Context, check scm.Check) (scm.CheckAttemptIdentity, error) {
	return h.identities[check.Link], nil
}

func TestFilterExpectedStaleAttestationChecksUsesAttemptOrder(t *testing.T) {
	dir, baseSHA, headSHA := setupGitRepo(t)
	sctx := newTestContextWithDBRecords(t, &mockAgent{name: "test"}, dir, baseSHA, headSHA, config.Commands{})
	stale := scm.Check{Name: requiredAttestationCheckName, Bucket: scm.CheckBucketFail, State: "FAILURE", Link: "stale"}
	compliantPending := scm.Check{Name: requiredAttestationCheckName, Bucket: scm.CheckBucketPending, State: "IN_PROGRESS", Link: "compliant"}
	compliantPass := scm.Check{Name: requiredAttestationCheckName, Bucket: scm.CheckBucketPass, State: "SUCCESS", Link: "compliant"}
	newFailure := scm.Check{Name: requiredAttestationCheckName, Bucket: scm.CheckBucketFail, State: "FAILURE", Link: "new-failure"}
	host := &attestationIdentityHost{identities: map[string]scm.CheckAttemptIdentity{
		"stale":       {RunID: 1000, RunNumber: 100, HeadSHA: headSHA},
		"compliant":   {RunID: 1001, RunNumber: 101, HeadSHA: headSHA},
		"new-failure": {RunID: 1002, RunNumber: 102, HeadSHA: headSHA},
	}}
	step := &CIStep{transientReruns: checkRerunBudget{expectedAttestationHeadSHA: headSHA}}

	filtered, err := step.filterExpectedStaleAttestationChecks(sctx, host, []scm.Check{stale})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].Bucket != scm.CheckBucketPending {
		t.Fatalf("stale-only checks = %#v, want synthetic pending", filtered)
	}

	filtered, err = step.filterExpectedStaleAttestationChecks(sctx, host, []scm.Check{stale, compliantPending})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].Link != "compliant" || filtered[0].Bucket != scm.CheckBucketPending {
		t.Fatalf("pending compliant checks = %#v", filtered)
	}

	filtered, err = step.filterExpectedStaleAttestationChecks(sctx, host, []scm.Check{stale, compliantPass})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].Link != "compliant" || filtered[0].Bucket != scm.CheckBucketPass {
		t.Fatalf("passing compliant checks = %#v", filtered)
	}
	encoded, err := sctx.DB.GetRunCIRerunState(sctx.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var persisted checkRerunBudget
	if err := persisted.unmarshal(encoded); err != nil {
		t.Fatal(err)
	}
	if persisted.expectedAttestationHeadSHA != headSHA || persisted.compliantAttestationRunNumber != 101 {
		t.Fatalf("persisted attestation state = %#v", persisted)
	}

	filtered, err = step.filterExpectedStaleAttestationChecks(sctx, host, []scm.Check{stale, compliantPass, newFailure})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 || filtered[0].Link != "compliant" || filtered[1].Link != "new-failure" || !filtered[1].Failing() {
		t.Fatalf("newer failure checks = %#v", filtered)
	}
}

func TestAllChecksPassedFailsClosed(t *testing.T) {
	tests := []struct {
		name  string
		check scm.Check
		ready bool
	}{
		{name: "pass", check: scm.Check{Bucket: scm.CheckBucketPass}, ready: true},
		{name: "skip", check: scm.Check{Bucket: scm.CheckBucketSkip}, ready: true},
		{name: "pending", check: scm.Check{Bucket: scm.CheckBucketPending}},
		{name: "failure", check: scm.Check{Bucket: scm.CheckBucketFail}},
		{name: "cancel", check: scm.Check{Bucket: scm.CheckBucketCancel}},
		{name: "unknown", check: scm.Check{}, ready: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checks := []scm.Check{tt.check}
			if got := allChecksPassed(checks); got != tt.ready {
				t.Fatalf("allChecksPassed() = %v, want %v", got, tt.ready)
			}
			if !tt.ready && !hasUnresolvedChecks(checks) && tt.check.Bucket != scm.CheckBucketFail {
				t.Fatal("non-ready check must be unresolved or failing")
			}
		})
	}
	if allChecksPassed(nil) {
		t.Fatal("empty checks must not pass")
	}
}

func TestPendingCheckMatchesLastFixed_SpecialCheckNames(t *testing.T) {
	t.Parallel()

	lastFixedChecks := encodeLastFixedChecks([]string{"lint,unit", "deploy+conflict"}, true)
	checks := []scm.Check{
		{Name: "lint,unit", Bucket: "pending"},
	}

	if !pendingCheckMatchesLastFixed(checks, lastFixedChecks) {
		t.Fatalf("expected pending check with special characters to match encoded last fixed checks %q", lastFixedChecks)
	}

	checks = []scm.Check{
		{Name: "lint", Bucket: "pending"},
	}
	if pendingCheckMatchesLastFixed(checks, lastFixedChecks) {
		t.Fatalf("expected unrelated pending check not to match encoded last fixed checks %q", lastFixedChecks)
	}
}

// A cancelled check can be a fix target, so the completion snapshot that lets
// the step notice its own CI re-run has to cover it. Keyed on the fail bucket
// alone, a cancelled-only fix round records nothing and the step can only log
// "fix already attempted" until its idle timeout.
func TestTerminalFailureCompletionTimesCoverCancelledChecks(t *testing.T) {
	t.Parallel()

	completed := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	cancelled := scm.Check{Name: "build", Bucket: scm.CheckBucketCancel, State: "CANCELLED", CompletedAt: completed}

	before := terminalFailureCompletionTimes([]scm.Check{cancelled})
	if got, ok := before["build"]; !ok || !got.Equal(completed) {
		t.Fatalf("completion times = %v, want the cancelled check recorded at %v", before, completed)
	}

	if terminalFailureCompletedAfter([]scm.Check{cancelled}, before) {
		t.Fatal("the same observation must not read as a re-run")
	}

	rerun := cancelled
	rerun.CompletedAt = completed.Add(2 * time.Minute)
	if !terminalFailureCompletedAfter([]scm.Check{rerun}, before) {
		t.Fatal("a cancelled check that completed again after the fix push must read as a re-run")
	}
}

// The fail bucket keeps the behavior it always had.
func TestTerminalFailureCompletionTimesStillCoverFailingChecks(t *testing.T) {
	t.Parallel()

	completed := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	failing := scm.Check{Name: "lint", Bucket: scm.CheckBucketFail, State: "FAILURE", CompletedAt: completed}

	before := terminalFailureCompletionTimes([]scm.Check{failing})
	if got, ok := before["lint"]; !ok || !got.Equal(completed) {
		t.Fatalf("completion times = %v, want the failing check recorded at %v", before, completed)
	}

	rerun := failing
	rerun.CompletedAt = completed.Add(time.Minute)
	if !terminalFailureCompletedAfter([]scm.Check{rerun}, before) {
		t.Fatal("a failing check that completed again after the fix push must read as a re-run")
	}

	// Passing and skipped checks are not failures and must stay out of the
	// snapshot, or an unrelated green check would reset the fix bookkeeping.
	quiet := terminalFailureCompletionTimes([]scm.Check{
		{Name: "docs", Bucket: scm.CheckBucketPass, State: "SUCCESS", CompletedAt: completed},
		{Name: "flaky", Bucket: scm.CheckBucketSkip, State: "SKIPPED", CompletedAt: completed},
	})
	if quiet != nil {
		t.Fatalf("completion times = %v, want nothing recorded for non-failures", quiet)
	}
}
