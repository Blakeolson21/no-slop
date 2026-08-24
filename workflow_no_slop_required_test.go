package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/db"
	pipelinesteps "github.com/Blakeolson21/no-slop/internal/pipeline/steps"
	"github.com/Blakeolson21/no-slop/internal/shellenv"
	"github.com/Blakeolson21/no-slop/internal/types"
	"gopkg.in/yaml.v3"
)

const requiredWorkflowStepTimeout = 10 * time.Second
const requiredWorkflowTestHeadSHA = "0123456789abcdef0123456789abcdef01234567"

// TestNoSlopRequiredWorkflowExemptsReleaseAutomation pins the exemption
// logic so the release pipeline (release-please via GITHUB_TOKEN) and
// dependabot are never silently blocked by the gate.
func TestNoSlopRequiredWorkflowExemptsReleaseAutomation(t *testing.T) {
	workflow := loadRequiredWorkflow(t)
	exempt := []string{
		"github-actions[bot]",
		"dependabot[bot]",
		"release-please[bot]",
	}
	for _, login := range exempt {
		if requiredWorkflowJobRunsForAuthor(t, workflow, login) {
			t.Errorf("workflow check job runs for exempt author %q", login)
		}
	}
	if !requiredWorkflowJobRunsForAuthor(t, workflow, "human-contributor") {
		t.Fatal("workflow check job should run for a non-exempt contributor")
	}
}

// TestNoSlopRequiredWorkflowChecksSignatureMarker executes the workflow check
// against the generated PR body emitted by the pipeline summary builder.
func TestNoSlopRequiredWorkflowChecksSignatureMarker(t *testing.T) {
	workflow := loadRequiredWorkflow(t)
	pipelineBody := generatedPipelineBody(t)
	legacyPipelineBody := strings.Replace(pipelineBody, "git push no-slop", "git push no-mistakes", 1)
	if legacyPipelineBody == pipelineBody {
		t.Fatal("generated pipeline body fixture did not contain canonical signature")
	}
	got := executeRequiredWorkflowFixture(t, workflow, []requiredWorkflowEvent{
		{Action: "opened", Body: pipelineBody, HeadSHA: requiredWorkflowTestHeadSHA, PRNumber: 1, RunID: 1, RunNumber: 1},
		{Action: "edited", Body: legacyPipelineBody, HeadSHA: requiredWorkflowTestHeadSHA, PRNumber: 1, RunID: 2, RunNumber: 2},
		{Action: "edited", Body: "body without a generated pipeline section", HeadSHA: requiredWorkflowTestHeadSHA, PRNumber: 1, RunID: 3, RunNumber: 3},
	})
	want := []requiredWorkflowResult{
		{RunID: 1, RunNumber: 1, Action: "opened", Executed: true, Conclusion: "success"},
		{RunID: 2, RunNumber: 2, Action: "edited", Executed: true, Conclusion: "success"},
		{RunID: 3, RunNumber: 3, Action: "edited", Executed: true, Conclusion: "failure"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("workflow check results =\n  %v\nwant\n  %v", got, want)
	}
}

// TestNoSlopRequiredWorkflowAcceptsHistoricalLegacyMarker executes the
// required check against a fully attested body written before the project and
// repository were renamed. Existing PRs can legitimately retain that marker
// while a current no-slop run refreshes their attestation.
func TestNoSlopRequiredWorkflowAcceptsHistoricalLegacyMarker(t *testing.T) {
	workflow := loadRequiredWorkflow(t)
	body := strings.Replace(
		generatedPipelineBody(t),
		"Updates from [git push no-slop](https://github.com/Blakeolson21/no-slop)",
		"Updates from [git push no-mistakes](https://github.com/kunchenguid/no-mistakes)",
		1,
	)
	got := executeRequiredWorkflowFixture(t, workflow, []requiredWorkflowEvent{{
		Action: "edited", Body: body, HeadSHA: requiredWorkflowTestHeadSHA, PRNumber: 4, RunID: 4, RunNumber: 4,
	}})
	if got[0].Conclusion != "success" {
		t.Fatalf("historical legacy body concluded %q, want success", got[0].Conclusion)
	}
}

// TestNoSlopRequiredWorkflowEnforcesCompletedPipelineAttestation executes the
// repository's required-check script as GitHub would. A signature proves only
// which tool wrote the body; merge authority additionally requires a v1
// attestation bound to this head with every required pre-publication gate done.
func TestNoSlopRequiredWorkflowEnforcesCompletedPipelineAttestation(t *testing.T) {
	workflow := loadRequiredWorkflow(t)
	signatureOnly := "## Pipeline\n\nUpdates from [git push no-slop](https://github.com/Blakeolson21/no-slop)\n"
	historicalSignatureOnly := "## Pipeline\n\nUpdates from [git push no-mistakes](https://github.com/kunchenguid/no-mistakes)\n"

	tests := []struct {
		name    string
		body    string
		headSHA string
		want    string
	}{
		{name: "signature only", body: signatureOnly, want: "failure"},
		{name: "historical signature only", body: historicalSignatureOnly, want: "failure"},
		{name: "review missing", body: generatedPipelineBodyWithStatuses(t, "", types.StepStatusCompleted, types.StepStatusCompleted), want: "failure"},
		{name: "test failed", body: generatedPipelineBodyWithStatuses(t, types.StepStatusCompleted, types.StepStatusFailed, types.StepStatusCompleted), want: "failure"},
		{name: "document skipped", body: generatedPipelineBodyWithStatuses(t, types.StepStatusCompleted, types.StepStatusCompleted, types.StepStatusSkipped), want: "failure"},
		{name: "stale head", body: generatedPipelineBody(t), headSHA: "ffffffffffffffffffffffffffffffffffffffff", want: "failure"},
		{name: "review certified stale head", body: generatedPipelineBodyWithStaleReviewCertification(t), want: "failure"},
		{name: "quoted malformed attestation before owned pipeline", body: insertAfterPublicationMarker(t, generatedPipelineBody(t), "## Intent\n\nQuoted legacy data: <!-- no-slop-pipeline-attestation:v1 { -->"), want: "success"},
		{name: "quoted semantically invalid owned tuple before owned pipeline", body: generatedPipelineBodyWithQuotedInvalidAttestation(t), want: "success"},
		{name: "pipeline heading in generated detail", body: generatedPipelineBody(t) + "\n\nFinding detail\n## Pipeline\n\nnot an owned attestation", want: "success"},
		{name: "all required steps completed", body: generatedPipelineBody(t), want: "success"},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			headSHA := tc.headSHA
			if headSHA == "" {
				headSHA = requiredWorkflowTestHeadSHA
			}
			got := executeRequiredWorkflowFixture(t, workflow, []requiredWorkflowEvent{{
				Action: "opened", Body: tc.body, HeadSHA: headSHA, PRNumber: 797, RunID: int64(100 + i), RunNumber: int64(100 + i),
			}})
			if got[0].Conclusion != tc.want {
				t.Fatalf("conclusion = %q, want %q", got[0].Conclusion, tc.want)
			}
		})
	}
}

// TestNoSlopRequiredWorkflowReadsPRBodyViaEnv pins the shell-injection-safe
// pattern: the PR body must be piped through an env var, not interpolated
// directly into the shell script body.
func TestNoSlopRequiredWorkflowReadsPRBodyViaEnv(t *testing.T) {
	workflow := loadRequiredWorkflow(t)
	step := requiredWorkflowCheckStep(t, workflow)
	if got := step.Env["PR_BODY"]; got != "${{ github.event.pull_request.body }}" {
		t.Fatalf("PR_BODY env expression = %q, want pull request body expression", got)
	}
	if got := step.Env["PR_HEAD_SHA"]; got != "${{ github.event.pull_request.head.sha }}" {
		t.Fatalf("PR_HEAD_SHA env expression = %q, want pull request head expression", got)
	}
	if strings.Contains(step.Run, "github.event.pull_request.body") {
		t.Fatalf("workflow must not interpolate the PR body expression directly into run script")
	}

	got := executeRequiredWorkflowFixture(t, workflow, []requiredWorkflowEvent{
		{Action: "opened", Body: generatedPipelineBody(t) + "\n$(exit 42)\n`exit 42`", HeadSHA: requiredWorkflowTestHeadSHA, PRNumber: 1, RunID: 10, RunNumber: 10},
	})
	if got[0].Conclusion != "success" {
		t.Fatalf("env-carried PR body with shell metacharacters concluded %q, want success", got[0].Conclusion)
	}
}

// TestNoSlopRequiredWorkflowTriggersOnRelevantPREvents ensures the check
// re-runs when the PR body is edited so a contributor cannot bypass by opening
// clean then editing the body.
func TestNoSlopRequiredWorkflowTriggersOnRelevantPREvents(t *testing.T) {
	workflow := loadRequiredWorkflow(t)
	got := requiredWorkflowPullRequestTypes(t, workflow)
	for _, typ := range []string{"opened", "edited", "synchronize", "reopened"} {
		if !got[typ] {
			t.Errorf("workflow must trigger on pull_request type %q", typ)
		}
	}
	if len(got) != 4 {
		t.Fatalf("pull_request types = %v, want exactly opened/edited/synchronize/reopened", got)
	}
}

// TestNoSlopRequiredWorkflowExecutesEveryBodyEvent reproduces the
// first-time-fork incident in which an opened event and two same-head body
// edits became actionable together. The scheduler fixture implements GitHub's
// documented one-running/one-pending concurrency limit, including pending-run
// replacement even when cancel-in-progress is false, and the exact
// cancel-in-progress ordering observed in runs 29962844999, 29962943078, and
// 29965243268. It then executes the workflow's real shell step for every job
// that survives scheduling.
func TestNoSlopRequiredWorkflowExecutesEveryBodyEvent(t *testing.T) {
	workflow := loadRequiredWorkflow(t)
	pipelineBody := generatedPipelineBody(t)
	events := []requiredWorkflowEvent{
		{Action: "opened", Body: pipelineBody, HeadSHA: requiredWorkflowTestHeadSHA, PRNumber: 549, RunID: 29962844999, RunNumber: 586},
		{Action: "edited", Body: "signature removed", HeadSHA: requiredWorkflowTestHeadSHA, PRNumber: 549, RunID: 29962943078, RunNumber: 587},
		{Action: "edited", Body: pipelineBody, HeadSHA: requiredWorkflowTestHeadSHA, PRNumber: 549, RunID: 29965243268, RunNumber: 588},
	}

	got := executeRequiredWorkflowFixture(t, workflow, events)
	want := []requiredWorkflowResult{
		{RunID: 29962844999, RunNumber: 586, Action: "opened", Executed: true, Conclusion: "success"},
		{RunID: 29962943078, RunNumber: 587, Action: "edited", Executed: true, Conclusion: "failure"},
		{RunID: 29965243268, RunNumber: 588, Action: "edited", Executed: true, Conclusion: "success"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("same-head body-event results =\n  %v\nwant every event executed to its own terminal result:\n  %v", got, want)
	}
}

func TestNoSlopRequiredWorkflowPreservesHeadEventCoalescing(t *testing.T) {
	workflow := loadRequiredWorkflow(t)
	events := []requiredWorkflowEvent{
		{Action: "opened", PRNumber: 549, RunID: 1001},
		{Action: "edited", PRNumber: 549, RunID: 1002},
		{Action: "edited", PRNumber: 549, RunID: 1003},
		{Action: "synchronize", PRNumber: 549, RunID: 1004},
		{Action: "reopened", PRNumber: 549, RunID: 1005},
	}
	groups := make([]string, len(events))
	for i, event := range events {
		groups[i] = renderRequiredWorkflowTemplate(t, workflow.Concurrency.Group, event)
	}
	if groups[0] == groups[1] || groups[0] == groups[2] || groups[1] == groups[2] {
		t.Fatalf("body-bearing event groups must be unique: %v", groups[:3])
	}
	if groups[3] != groups[4] {
		t.Fatalf("synchronize/reopened groups = %q and %q, want preserved coalescing", groups[3], groups[4])
	}
	for _, bodyGroup := range groups[:3] {
		if bodyGroup == groups[3] {
			t.Fatalf("body event group %q can be canceled by a head event", bodyGroup)
		}
	}
}

func TestNoSlopRequiredWorkflowPublishesStableEventIdentity(t *testing.T) {
	workflow := loadRequiredWorkflow(t)
	if workflow.Jobs["check"].Name != "PR must be raised via no-slop" {
		t.Fatalf("required check name changed to %q", workflow.Jobs["check"].Name)
	}

	firstNonce := "00112233445566778899aabbccddeeff"
	latestNonce := "ffeeddccbbaa99887766554433221100"
	first := requiredWorkflowEvent{Action: "edited", Body: "<!-- no-slop-publication:v1 " + firstNonce + " -->\n\nfirst body quoting <!-- no-slop-publication:v1 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa -->", PRNumber: 549, RunID: 29962943078, RunNumber: 587}
	latest := requiredWorkflowEvent{Action: "edited", Body: "<!-- no-slop-publication:v1 " + latestNonce + " -->\n\nlatest body", PRNumber: 549, RunID: 29965243268, RunNumber: 588}
	firstName := renderRequiredWorkflowTemplate(t, workflow.RunName, first)
	latestName := renderRequiredWorkflowTemplate(t, workflow.RunName, latest)
	if !strings.HasPrefix(firstName, "no-slop-required|edited|PR #549 event 587 (run 29962943078)|<!-- no-slop-publication:v1 "+firstNonce+" -->") {
		t.Fatalf("first event run name = %q, want publication identity prefix", firstName)
	}
	if !strings.HasPrefix(latestName, "no-slop-required|edited|PR #549 event 588 (run 29965243268)|<!-- no-slop-publication:v1 "+latestNonce+" -->") {
		t.Fatalf("latest event run name = %q, want publication identity prefix", latestName)
	}
	for label, rendered := range map[string]string{"first": firstName, "latest": latestName} {
		for field, want := range map[string]string{
			"PR number":    "PR #549",
			"event action": "edited",
			"run number":   "event " + strconv.FormatInt(map[string]int64{"first": first.RunNumber, "latest": latest.RunNumber}[label], 10),
			"run ID":       "run " + strconv.FormatInt(map[string]int64{"first": first.RunID, "latest": latest.RunID}[label], 10),
		} {
			if !strings.Contains(rendered, want) {
				t.Fatalf("%s event run name = %q, want %s %q", label, rendered, field, want)
			}
		}
	}
	if firstName == latestName {
		t.Fatalf("distinct body events have ambiguous run name %q", firstName)
	}
	if first.RunNumber >= latest.RunNumber {
		t.Fatalf("fixture event ordering is not monotonic: %d then %d", first.RunNumber, latest.RunNumber)
	}
}

func TestNoSlopRequiredWorkflowKeepsForkBoundaryReadOnly(t *testing.T) {
	workflow := loadRequiredWorkflow(t)
	if _, ok := workflow.On["pull_request"]; !ok {
		t.Fatal("required workflow must retain the safe pull_request boundary")
	}
	if _, ok := workflow.On["pull_request_target"]; ok {
		t.Fatal("required workflow must not gain pull_request_target write authority")
	}
	if got := workflow.Permissions["contents"]; got != "read" {
		t.Fatalf("contents permission = %q, want read", got)
	}
	for permission, access := range workflow.Permissions {
		if access == "write" {
			t.Fatalf("permission %q unexpectedly grants write authority", permission)
		}
	}

	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if requiredWorkflowStepReferences(step, "secrets.") {
				t.Fatal("required workflow must not expose secrets to fork runs")
			}
			if strings.Contains(strings.ToLower(step.Uses), "actions/checkout") {
				t.Fatal("required workflow must not check out fork code")
			}
		}
	}
}

type requiredWorkflow struct {
	RunName     string                         `yaml:"run-name"`
	On          map[string]any                 `yaml:"on"`
	Permissions map[string]string              `yaml:"permissions"`
	Concurrency requiredWorkflowConcurrency    `yaml:"concurrency"`
	Jobs        map[string]requiredWorkflowJob `yaml:"jobs"`
}

type requiredWorkflowConcurrency struct {
	Group            string `yaml:"group"`
	CancelInProgress bool   `yaml:"cancel-in-progress"`
}

type requiredWorkflowJob struct {
	Name  string                 `yaml:"name"`
	If    string                 `yaml:"if"`
	Steps []requiredWorkflowStep `yaml:"steps"`
}

type requiredWorkflowStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
	With map[string]string `yaml:"with"`
}

type requiredWorkflowEvent struct {
	Action    string
	Body      string
	HeadSHA   string
	UpdatedAt string
	PRNumber  int64
	RunID     int64
	RunNumber int64
}

type requiredWorkflowResult struct {
	RunID      int64
	RunNumber  int64
	Action     string
	Executed   bool
	Conclusion string
}

func loadRequiredWorkflow(t *testing.T) requiredWorkflow {
	t.Helper()
	data, err := os.ReadFile(".github/workflows/no-slop-required.yml")
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}
	var workflow requiredWorkflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse workflow: %v", err)
	}
	return workflow
}

func generatedPipelineBody(t *testing.T) string {
	return generatedPipelineBodyWithStatuses(t, types.StepStatusCompleted, types.StepStatusCompleted, types.StepStatusCompleted)
}

func generatedPipelineBodyWithStatuses(t *testing.T, review, testStep, document types.StepStatus) string {
	t.Helper()
	results := []*db.StepResult{
		{ID: "review", StepName: types.StepReview, Status: review, CertifiedHeadSHA: testCertifiedWorkflowHead(review)},
		{ID: "test", StepName: types.StepTest, Status: testStep, CertifiedHeadSHA: testCertifiedWorkflowHead(testStep)},
		{ID: "document", StepName: types.StepDocument, Status: document, CertifiedHeadSHA: testCertifiedWorkflowHead(document)},
		{ID: "pr", StepName: types.StepPR, Status: types.StepStatusRunning},
		{ID: "ci", StepName: types.StepCI, Status: types.StepStatusPending},
	}
	if review == "" {
		results = results[1:]
	}
	rounds := make(map[string][]*db.StepRound, len(results))
	for _, result := range results {
		rounds[result.ID] = []*db.StepRound{{Round: 1, Trigger: "initial"}}
	}
	body, _ := pipelinesteps.BuildPipelineSummary(results, rounds, requiredWorkflowTestHeadSHA)
	if strings.TrimSpace(body) == "" {
		t.Fatal("pipeline summary builder returned an empty PR body")
	}
	return publicationMarkerForPipelineBody(t, body) + "\n\n" + body
}

func publicationMarkerForPipelineBody(t *testing.T, body string) string {
	t.Helper()
	const prefix = "<!-- no-slop-pipeline-attestation:v1 "
	const closing = " -->"
	start := strings.Index(body, prefix)
	if start < 0 {
		t.Fatal("generated body has no pipeline attestation")
	}
	start += len(prefix)
	end := strings.Index(body[start:], closing)
	if end < 0 {
		t.Fatal("generated body has malformed pipeline attestation")
	}
	var attestation struct {
		PublicationNonce string `json:"publication_nonce"`
	}
	if err := json.Unmarshal([]byte(body[start:start+end]), &attestation); err != nil {
		t.Fatal(err)
	}
	if attestation.PublicationNonce == "" {
		t.Fatal("generated body has no publication nonce")
	}
	return "<!-- no-slop-publication:v1 " + attestation.PublicationNonce + " -->"
}

func generatedPipelineBodyWithQuotedInvalidAttestation(t *testing.T) string {
	t.Helper()
	body := generatedPipelineBody(t)
	const prefix = "<!-- no-slop-pipeline-attestation:v1 "
	const closing = " -->"
	start := strings.Index(body, prefix)
	if start < 0 {
		t.Fatal("generated body has no pipeline attestation")
	}
	start += len(prefix)
	end := strings.Index(body[start:], closing)
	if end < 0 {
		t.Fatal("generated body has malformed pipeline attestation")
	}
	var attestation map[string]any
	if err := json.Unmarshal([]byte(body[start:start+end]), &attestation); err != nil {
		t.Fatal(err)
	}
	attestation["steps"] = []any{}
	invalid, err := json.Marshal(attestation)
	if err != nil {
		t.Fatal(err)
	}
	quoted := "## Intent\n\nQuoted historical data:\n\n## Pipeline\n\nUpdates from [git push no-slop](https://github.com/Blakeolson21/no-slop)\n\n" + prefix + string(invalid) + closing
	return insertAfterPublicationMarker(t, body, quoted)
}

func insertAfterPublicationMarker(t *testing.T, body, text string) string {
	t.Helper()
	markerEnd := strings.Index(body, "\n\n")
	if markerEnd < 0 {
		t.Fatal("generated body has no publication marker separator")
	}
	return body[:markerEnd+2] + text + "\n\n" + body[markerEnd+2:]
}

func testCertifiedWorkflowHead(status types.StepStatus) *string {
	if status != types.StepStatusCompleted {
		return nil
	}
	head := requiredWorkflowTestHeadSHA
	return &head
}

func generatedPipelineBodyWithStaleReviewCertification(t *testing.T) string {
	t.Helper()
	body := generatedPipelineBody(t)
	const prefix = "<!-- no-slop-pipeline-attestation:v1 "
	const closing = " -->"
	start := strings.Index(body, prefix)
	if start < 0 {
		t.Fatal("generated body has no pipeline attestation")
	}
	start += len(prefix)
	end := strings.Index(body[start:], closing)
	if end < 0 {
		t.Fatal("generated body has malformed pipeline attestation")
	}
	var attestation struct {
		HeadSHA          string `json:"head_sha"`
		PublicationNonce string `json:"publication_nonce"`
		Steps            []struct {
			Step    types.StepName   `json:"step"`
			Status  types.StepStatus `json:"status"`
			HeadSHA string           `json:"head_sha"`
		} `json:"steps"`
	}
	if err := json.Unmarshal([]byte(body[start:start+end]), &attestation); err != nil {
		t.Fatal(err)
	}
	for i := range attestation.Steps {
		if attestation.Steps[i].Step == types.StepReview {
			attestation.Steps[i].HeadSHA = "ffffffffffffffffffffffffffffffffffffffff"
		}
	}
	payload, err := json.Marshal(attestation)
	if err != nil {
		t.Fatal(err)
	}
	return body[:start] + string(payload) + body[start+end:]
}

func requiredWorkflowCheckStep(t *testing.T, workflow requiredWorkflow) requiredWorkflowStep {
	t.Helper()
	job, ok := workflow.Jobs["check"]
	if !ok {
		t.Fatal("workflow has no check job")
	}
	if len(job.Steps) != 1 {
		t.Fatalf("check job has %d steps, want 1", len(job.Steps))
	}
	return job.Steps[0]
}

func requiredWorkflowJobRunsForAuthor(t *testing.T, workflow requiredWorkflow, login string) bool {
	t.Helper()
	job, ok := workflow.Jobs["check"]
	if !ok {
		t.Fatal("workflow has no check job")
	}
	for _, term := range strings.Split(job.If, "&&") {
		term = strings.TrimSpace(term)
		const prefix = "github.event.pull_request.user.login != '"
		exempt, ok := strings.CutPrefix(term, prefix)
		if !ok {
			t.Fatalf("fixture cannot evaluate job condition term %q", term)
		}
		exempt, ok = strings.CutSuffix(exempt, "'")
		if !ok {
			t.Fatalf("fixture cannot evaluate job condition term %q", term)
		}
		if login == exempt {
			return false
		}
	}
	return true
}

func requiredWorkflowPullRequestTypes(t *testing.T, workflow requiredWorkflow) map[string]bool {
	t.Helper()
	raw, ok := workflow.On["pull_request"]
	if !ok {
		t.Fatal("workflow has no pull_request trigger")
	}
	trigger, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("pull_request trigger has type %T, want map", raw)
	}
	rawTypes, ok := trigger["types"].([]any)
	if !ok {
		t.Fatalf("pull_request trigger types has type %T, want list", trigger["types"])
	}
	out := make(map[string]bool, len(rawTypes))
	for _, rawType := range rawTypes {
		typ, ok := rawType.(string)
		if !ok {
			t.Fatalf("pull_request trigger type has type %T, want string", rawType)
		}
		out[typ] = true
	}
	return out
}

func requiredWorkflowStepReferences(step requiredWorkflowStep, needle string) bool {
	needle = strings.ToLower(needle)
	fields := []string{step.Name, step.Uses, step.Run}
	for key, value := range step.Env {
		fields = append(fields, key, value)
	}
	for key, value := range step.With {
		fields = append(fields, key, value)
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
}

func executeRequiredWorkflowFixture(t *testing.T, workflow requiredWorkflow, events []requiredWorkflowEvent) []requiredWorkflowResult {
	t.Helper()
	groups := make(map[string][]int)
	for i, event := range events {
		group := renderRequiredWorkflowTemplate(t, workflow.Concurrency.Group, event)
		groups[group] = append(groups[group], i)
	}

	execute := make([]bool, len(events))
	for _, indexes := range groups {
		switch {
		case len(indexes) == 1:
			execute[indexes[0]] = true
		case workflow.Concurrency.CancelInProgress:
			// This is the ordering the real first-time-fork approval incident
			// produced: the opened run executed and both waiting edits were
			// canceled. GitHub does not guarantee concurrency-group ordering.
			execute[indexes[0]] = true
		default:
			// GitHub permits one running and one pending run per group. A newer
			// pending run replaces an older pending run even when in-progress
			// cancellation is disabled.
			execute[indexes[0]] = true
			execute[indexes[len(indexes)-1]] = true
		}
	}

	step := workflow.Jobs["check"].Steps[0]
	results := make([]requiredWorkflowResult, len(events))
	for i, event := range events {
		result := requiredWorkflowResult{RunID: event.RunID, RunNumber: event.RunNumber, Action: event.Action}
		if !execute[i] {
			result.Conclusion = "cancelled"
			results[i] = result
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), requiredWorkflowStepTimeout)
		cmd := exec.CommandContext(ctx, "bash", "-c", step.Run)
		cmd.WaitDelay = 2 * time.Second
		shellenv.ConfigureShellCommand(cmd)
		cmd.Env = append(os.Environ(),
			"PR_BODY="+event.Body,
			"PR_HEAD_SHA="+event.HeadSHA,
			"PR_AUTHOR=first-time-fork-contributor",
			"PR_NUMBER="+strconv.FormatInt(event.PRNumber, 10),
		)
		var output bytes.Buffer
		cmd.Stdout = &output
		cmd.Stderr = &output
		err := shellenv.RunShellCommand(cmd)
		cancel()
		if ctx.Err() == context.DeadlineExceeded {
			t.Fatalf("execute compliance step for run %d timed out after %s\n%s", event.RunID, requiredWorkflowStepTimeout, output.String())
		}
		result.Executed = true
		if err == nil {
			result.Conclusion = "success"
		} else if _, ok := err.(*exec.ExitError); ok {
			result.Conclusion = "failure"
		} else {
			t.Fatalf("execute compliance step for run %d: %v\n%s", event.RunID, err, output.String())
		}
		results[i] = result
	}
	return results
}

func renderRequiredWorkflowTemplate(t *testing.T, template string, event requiredWorkflowEvent) string {
	t.Helper()
	const bodyEventGroupExpression = "(github.event.action == 'opened' || github.event.action == 'edited') && github.run_id || 'head-change'"
	bodyEventGroup := "head-change"
	if event.Action == "opened" || event.Action == "edited" {
		bodyEventGroup = strconv.FormatInt(event.RunID, 10)
	}
	template = strings.ReplaceAll(template, "${{ "+bodyEventGroupExpression+" }}", bodyEventGroup)

	replacements := []struct {
		expression string
		value      string
	}{
		{expression: "github.event.action", value: event.Action},
		{expression: "github.event.pull_request.body", value: event.Body},
		{expression: "github.event.pull_request.number", value: strconv.FormatInt(event.PRNumber, 10)},
		{expression: "github.event.pull_request.head.sha", value: event.HeadSHA},
		{expression: "github.event.pull_request.updated_at", value: event.UpdatedAt},
		{expression: "github.run_id", value: strconv.FormatInt(event.RunID, 10)},
		{expression: "github.run_number", value: strconv.FormatInt(event.RunNumber, 10)},
	}
	for _, replacement := range replacements {
		template = strings.ReplaceAll(template, "${{ "+replacement.expression+" }}", replacement.value)
	}
	if strings.Contains(template, "${{") {
		t.Fatalf("fixture cannot evaluate workflow expression in %q", template)
	}
	return strings.Join(strings.Fields(template), " ")
}
