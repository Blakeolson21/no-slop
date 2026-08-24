package steps

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/scm"
)

type attestationIdentityHost struct {
	recordingPRUpdateHost
	identities  map[string]scm.CheckAttemptIdentity
	publication scm.CheckAttemptIdentity
}

func TestCIStepFailsClosedWhenAttestationStateCannotBeRestored(t *testing.T) {
	for _, encoded := range []string{
		`{`,
		`{"head_sha":"head-without-boundary"}`,
	} {
		t.Run(encoded, func(t *testing.T) {
			dir, baseSHA, headSHA := setupGitRepo(t)
			sctx := newTestContextWithDBRecords(t, &mockAgent{name: "test"}, dir, baseSHA, headSHA, config.Commands{})
			if err := sctx.DB.SetRunCIAttestationState(sctx.Run.ID, encoded); err != nil {
				t.Fatal(err)
			}
			outcome, err := (&CIStep{}).Execute(sctx)
			if err == nil || !strings.Contains(err.Error(), "persisted CI attestation state") {
				t.Fatalf("Execute() = (%#v, %v), want restoration error", outcome, err)
			}
		})
	}
}

func TestCIStepFailsClosedWhenAttestationStateCannotBeRead(t *testing.T) {
	dir, baseSHA, headSHA := setupGitRepo(t)
	sctx := newTestContextWithDBRecords(t, &mockAgent{name: "test"}, dir, baseSHA, headSHA, config.Commands{})
	if err := sctx.DB.Close(); err != nil {
		t.Fatal(err)
	}
	outcome, err := (&CIStep{}).Execute(sctx)
	if err == nil || !strings.Contains(err.Error(), "read persisted CI attestation state") {
		t.Fatalf("Execute() = (%#v, %v), want read error", outcome, err)
	}
}

func TestCIStepKeepsLegacyRerunRestorationBestEffort(t *testing.T) {
	dir, baseSHA, headSHA := setupGitRepo(t)
	sctx := newTestContextWithDBRecords(t, &mockAgent{name: "test"}, dir, baseSHA, headSHA, config.Commands{})
	if err := sctx.DB.SetRunCIRerunState(sctx.Run.ID, `{"spent":[]}`); err != nil {
		t.Fatal(err)
	}
	var logs []string
	sctx.Log = func(line string) { logs = append(logs, line) }
	sctx.Run.PRURL = nil
	outcome, err := (&CIStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if outcome == nil || !outcome.Skipped {
		t.Fatalf("Execute() = %#v, want no-PR skip", outcome)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "could not restore the persisted rerun budget") {
		t.Fatalf("logs = %q", logs)
	}
}

func TestCIStepFailsClosedOnMalformedLegacyAttestationState(t *testing.T) {
	dir, baseSHA, headSHA := setupGitRepo(t)
	sctx := newTestContextWithDBRecords(t, &mockAgent{name: "test"}, dir, baseSHA, headSHA, config.Commands{})
	if err := sctx.DB.SetRunCIRerunState(sctx.Run.ID, `{`); err != nil {
		t.Fatal(err)
	}
	sctx.Run.PRURL = nil
	outcome, err := (&CIStep{}).Execute(sctx)
	if err == nil || !strings.Contains(err.Error(), "restore legacy persisted CI attestation state") {
		t.Fatalf("Execute() = (%#v, %v), want legacy restoration error", outcome, err)
	}
}

func TestCIStepRejectsLegacyTimestampAttestationState(t *testing.T) {
	dir, baseSHA, headSHA := setupGitRepo(t)
	sctx := newTestContextWithDBRecords(t, &mockAgent{name: "test"}, dir, baseSHA, headSHA, config.Commands{})
	boundary := time.Date(2026, 8, 23, 18, 42, 31, 0, time.UTC)
	legacy := `{"spent":{"build":1},"expected_attestation_head_sha":"` + headSHA + `","expected_attestation_updated_at":"` + boundary.Format(time.RFC3339Nano) + `"}`
	if err := sctx.DB.SetRunCIRerunState(sctx.Run.ID, legacy); err != nil {
		t.Fatal(err)
	}
	step := &CIStep{}
	step.loadRerunBudget(sctx)
	err := step.loadExpectedAttestationState(sctx)
	if err == nil || !strings.Contains(err.Error(), "requires PR attestation republication") {
		t.Fatalf("loadExpectedAttestationState() error = %v, want republication", err)
	}
	if step.transientReruns.used("build") != 1 {
		t.Fatalf("restored state = budget %#v, attestation %#v", step.transientReruns, step.expectedAttestation)
	}
}

func (h *attestationIdentityHost) GetCheckAttemptIdentity(_ context.Context, check scm.Check) (scm.CheckAttemptIdentity, error) {
	return h.identities[check.Link], nil
}

func (h *attestationIdentityHost) FindAttestationPublicationIdentity(_ context.Context, headSHA, nonce string) (scm.CheckAttemptIdentity, bool, error) {
	if h.publication.HeadSHA != headSHA || h.publication.PublicationNonce != nonce {
		return scm.CheckAttemptIdentity{}, false, nil
	}
	return h.publication, true, nil
}

func TestFilterExpectedStaleAttestationChecksUsesPublicationNonce(t *testing.T) {
	dir, baseSHA, headSHA := setupGitRepo(t)
	sctx := newTestContextWithDBRecords(t, &mockAgent{name: "test"}, dir, baseSHA, headSHA, config.Commands{})
	currentNonce := "00112233445566778899aabbccddeeff"
	staleNonce := "ffeeddccbbaa99887766554433221100"
	olderPass := scm.Check{Name: requiredAttestationCheckName, Bucket: scm.CheckBucketPass, State: "SUCCESS", Link: "older-pass"}
	stale := scm.Check{Name: requiredAttestationCheckName, Bucket: scm.CheckBucketFail, State: "FAILURE", Link: "stale"}
	publicationCancelled := scm.Check{Name: requiredAttestationCheckName, Bucket: scm.CheckBucketCancel, State: "CANCELLED", Link: "publication-cancelled"}
	laterFailure := scm.Check{Name: requiredAttestationCheckName, Bucket: scm.CheckBucketFail, State: "FAILURE", Link: "later-failure"}
	laterSameBody := scm.Check{Name: requiredAttestationCheckName, Bucket: scm.CheckBucketPass, State: "SUCCESS", Link: "later-same-body"}
	host := &attestationIdentityHost{identities: map[string]scm.CheckAttemptIdentity{
		"older-pass":            {RunID: 999, RunNumber: 99, RunAttempt: 1, HeadSHA: headSHA, PublicationNonce: staleNonce},
		"stale":                 {RunID: 1000, RunNumber: 101, RunAttempt: 1, HeadSHA: headSHA, PublicationNonce: staleNonce},
		"publication-cancelled": {RunID: 1002, RunNumber: 102, RunAttempt: 1, HeadSHA: headSHA, PublicationNonce: currentNonce},
		"later-failure":         {RunID: 1001, RunNumber: 103, RunAttempt: 1, HeadSHA: headSHA, PublicationNonce: staleNonce},
		"later-same-body":       {RunID: 1004, RunNumber: 104, RunAttempt: 1, HeadSHA: headSHA, PublicationNonce: currentNonce},
	}, publication: scm.CheckAttemptIdentity{RunID: 1002, RunNumber: 102, RunAttempt: 1, HeadSHA: headSHA, PublicationNonce: currentNonce}}
	state := expectedAttestationState{HeadSHA: headSHA, PublicationNonce: currentNonce}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := sctx.DB.SetRunCIAttestationState(sctx.Run.ID, string(encoded)); err != nil {
		t.Fatal(err)
	}
	step := &CIStep{}
	step.loadRerunBudget(sctx)
	if err := step.loadExpectedAttestationState(sctx); err != nil {
		t.Fatal(err)
	}

	filtered, err := step.filterExpectedStaleAttestationChecks(sctx, host, []scm.Check{olderPass, stale})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].Bucket != scm.CheckBucketPending {
		t.Fatalf("pre-update terminal checks = %#v, want synthetic pending", filtered)
	}

	filtered, err = step.filterExpectedStaleAttestationChecks(sctx, host, []scm.Check{olderPass, stale, publicationCancelled})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].Link != "publication-cancelled" || filtered[0].Bucket != scm.CheckBucketCancel {
		t.Fatalf("publication attempt ordering = %#v", filtered)
	}
	if step.expectedAttestation.PublicationRunID != 1002 {
		t.Fatalf("publication run ID = %d, want 1002", step.expectedAttestation.PublicationRunID)
	}
	if step.expectedAttestation.PublicationRunNumber != 102 {
		t.Fatalf("publication run number = %d, want 102", step.expectedAttestation.PublicationRunNumber)
	}

	filtered, err = step.filterExpectedStaleAttestationChecks(sctx, host, []scm.Check{olderPass, stale, publicationCancelled, laterFailure, laterSameBody})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 3 || filtered[0].Link != "publication-cancelled" || filtered[1].Link != "later-failure" || filtered[2].Link != "later-same-body" {
		t.Fatalf("later authoritative attempts were suppressed: %#v", filtered)
	}

	recovered := &CIStep{}
	if err := recovered.loadExpectedAttestationState(sctx); err != nil {
		t.Fatal(err)
	}
	filtered, err = recovered.filterExpectedStaleAttestationChecks(sctx, host, []scm.Check{stale, publicationCancelled, laterFailure})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 || filtered[0].Link != "publication-cancelled" || filtered[1].Link != "later-failure" {
		t.Fatalf("recovered attempt ordering = %#v", filtered)
	}
	if recovered.expectedAttestation.PublicationRunID != 1002 || recovered.expectedAttestation.PublicationRunNumber != 102 {
		t.Fatalf("recovered attestation state = %#v", recovered.expectedAttestation)
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
