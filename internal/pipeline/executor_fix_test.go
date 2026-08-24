package pipeline

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestExecutor_FixEmitsFixReviewStatusWithoutStreamingTheDiff(t *testing.T) {
	database, p, run, repo := setupTest(t)

	// Create a real git repo as workDir so DiffHead works
	workDir := t.TempDir()
	initGitRepo(t, workDir)

	// Step that needs approval on first call and after fix
	callCount := 0
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			callCount++
			if sctx.Fixing {
				// Simulate agent making changes in the worktree
				writeTestFile(t, workDir, "fix.txt", "agent fix\n")
				execGit(t, workDir, "add", "fix.txt")
			}
			return &StepOutcome{NeedsApproval: true, Findings: `{"items":[]}`}, nil
		},
	}

	steps := []Step{step}
	exec := NewExecutor(database, p, nil, nil, steps, nil)
	events := collectEvents(exec)

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	// First: step reaches awaiting_approval (not fix_review)
	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)

	// Verify initial event has awaiting_approval status
	initialEvent := waitForStepEvent(t, events, ipc.EventStepCompleted, types.StepReview)
	if initialEvent.Status == nil || *initialEvent.Status != string(types.StepStatusAwaitingApproval) {
		t.Errorf("expected awaiting_approval status, got %v", initialEvent.Status)
	}

	// Send fix action
	exec.Respond(types.StepReview, types.ActionFix, nil)

	// The gate is announced by status alone. The working-tree diff is
	// derived state served on demand (ipc.MethodGetStepDiff); it is
	// deliberately not attached here, because it is unbounded and a single
	// oversized frame would break the whole subscription.
	fixEvent := waitForEvent(t, events, ipc.EventStepCompleted, string(types.StepStatusFixReview))
	if fixEvent.StepName == nil || *fixEvent.StepName != types.StepReview {
		t.Errorf("fix_review event step = %v, want review", fixEvent.StepName)
	}
	if encoded, err := json.Marshal(fixEvent); err != nil {
		t.Fatal(err)
	} else if len(encoded) > 4096 {
		t.Errorf("fix_review frame is %d bytes; the gate event must stay small enough that no worktree change can overflow it", len(encoded))
	}

	// Approve to end
	exec.Respond(types.StepReview, types.ActionApprove, nil)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor timed out")
	}
}

func TestExecutor_UnselectedReviewFindingSurvivesSilentRereview(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	initial := `{"findings":[{"id":"review-1","severity":"error","description":"unsafe loader","action":"ask-user"},{"id":"review-2","severity":"warning","description":"hardcoded timeout","action":"ask-user"}],"summary":"2 findings","tested":["initial review evidence"]}`
	empty := `{"findings":[],"summary":"no new findings","risk_level":"low","tested":["rereview evidence"]}`
	calls := 0
	step := &scopeLimitedAdaptiveCallStep{adaptiveCallStep: adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			calls++
			if calls == 1 {
				return &StepOutcome{NeedsApproval: true, Findings: initial}, nil
			}
			return &StepOutcome{Findings: empty}, nil
		},
	}}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)
	done, _ := startExecutor(t, exec, run, repo, workDir)

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)
	unsafeID := findingIDByDescription(t, database, run.ID, types.StepReview, "unsafe loader")
	if err := exec.Respond(types.StepReview, types.ActionFix, []string{unsafeID}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("run completed after a silent rereview dropped an unresolved finding: %v", err)
		default:
		}
		steps, err := database.GetStepsByRun(run.ID)
		if err == nil && len(steps) == 1 && steps[0].Status == types.StepStatusFixReview {
			if steps[0].FindingsJSON == nil {
				t.Fatal("fix-review gate has no findings")
			}
			parsed, err := types.ParseFindingsJSON(*steps[0].FindingsJSON)
			if err != nil {
				t.Fatal(err)
			}
			if len(parsed.Items) != 1 || parsed.Items[0].Description != "hardcoded timeout" {
				t.Fatalf("outstanding findings = %#v, want only hardcoded timeout", parsed.Items)
			}
			if len(parsed.Tested) != 2 || !slices.Contains(parsed.Tested, "initial review evidence") || !slices.Contains(parsed.Tested, "rereview evidence") {
				t.Fatalf("merged review evidence = %#v, want both rounds", parsed.Tested)
			}
			stats, err := database.StepFindingStats(steps[0])
			if err != nil {
				t.Fatal(err)
			}
			if stats.ReportedFindings != 2 || stats.FixedFindings != 1 {
				t.Fatalf("finding stats = reported %d, fixed %d; want 2 and 1", stats.ReportedFindings, stats.FixedFindings)
			}
			if err := exec.Respond(types.StepReview, types.ActionApprove, nil); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("review did not park again on the unresolved carried finding")
}

func TestExecutor_LaterSelectedCarriedFindingClearsAfterVerification(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	initial := `{"findings":[{"id":"review-1","severity":"error","description":"unsafe loader","action":"ask-user"},{"id":"review-2","severity":"warning","description":"hardcoded timeout","action":"ask-user"}],"summary":"2 findings"}`
	empty := `{"findings":[],"summary":"no new findings","risk_level":"low"}`
	calls := 0
	step := &scopeLimitedAdaptiveCallStep{adaptiveCallStep: adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			calls++
			if calls == 1 {
				return &StepOutcome{NeedsApproval: true, Findings: initial}, nil
			}
			return &StepOutcome{Findings: empty}, nil
		},
	}}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)
	done, _ := startExecutor(t, exec, run, repo, workDir)

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)
	firstID := findingIDByDescription(t, database, run.ID, types.StepReview, "unsafe loader")
	if err := exec.Respond(types.StepReview, types.ActionFix, []string{firstID}); err != nil {
		t.Fatal(err)
	}
	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusFixReview)
	secondID := findingIDByDescription(t, database, run.ID, types.StepReview, "hardcoded timeout")
	if err := exec.Respond(types.StepReview, types.ActionFix, []string{secondID}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not complete after the later-selected finding passed verification")
	}

	steps, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	rounds, err := database.GetRoundsByStep(steps[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rounds) != 3 || rounds[1].SelectedFindingIDs == nil || !strings.Contains(*rounds[1].SelectedFindingIDs, secondID) {
		t.Fatalf("later selection was not durably attached to the carried gate: %#v", rounds)
	}
}

func TestExecutor_CarriedFindingKeepsIdentityAndStricterAction(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	initial := `{"findings":[{"id":"review-1","severity":"error","file":"loader.go","line":8,"description":"unsafe loader","action":"ask-user"},{"id":"review-2","severity":"warning","description":"selected first","action":"ask-user"}],"summary":"2 findings"}`
	unsafeID := ""
	unsafeToken := ""
	calls := 0
	step := &scopeLimitedAdaptiveCallStep{adaptiveCallStep: adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			calls++
			if calls == 1 {
				return &StepOutcome{NeedsApproval: true, Findings: initial}, nil
			}
			rereview := `{"findings":[{"prior_id":"` + unsafeID + `","prior_continuity_token":"` + unsafeToken + `","severity":"error","file":"loader.go","line":9,"description":"unsafe loader","action":"no-op"},{"severity":"warning","description":"new concern","action":"ask-user"}],"summary":"2 findings"}`
			return &StepOutcome{NeedsApproval: true, Findings: rereview}, nil
		},
	}}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)
	done, _ := startExecutor(t, exec, run, repo, workDir)
	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)
	unsafeID = findingIDByDescription(t, database, run.ID, types.StepReview, "unsafe loader")
	parkedSteps, err := database.GetStepsByRun(run.ID)
	if err != nil || parkedSteps[0].FindingsJSON == nil {
		t.Fatalf("read initial findings: %v", err)
	}
	parkedFindings, err := types.ParseFindingsJSON(*parkedSteps[0].FindingsJSON)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range parkedFindings.Items {
		if finding.ID == unsafeID {
			unsafeToken = finding.ContinuityToken
		}
	}
	if unsafeToken == "" {
		t.Fatalf("finding %q has no continuity token", unsafeID)
	}
	selectedID := findingIDByDescription(t, database, run.ID, types.StepReview, "selected first")
	if err := exec.Respond(types.StepReview, types.ActionFix, []string{selectedID}); err != nil {
		t.Fatal(err)
	}
	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusFixReview)

	steps, err := database.GetStepsByRun(run.ID)
	if err != nil || steps[0].FindingsJSON == nil {
		t.Fatalf("read parked findings: %v", err)
	}
	findings, err := types.ParseFindingsJSON(*steps[0].FindingsJSON)
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]bool)
	for _, finding := range findings.Items {
		if ids[finding.ID] {
			t.Fatalf("duplicate published finding id %q: %#v", finding.ID, findings.Items)
		}
		ids[finding.ID] = true
		if finding.Description == "unsafe loader" && (finding.ID != unsafeID || finding.Action != "ask-user") {
			t.Fatalf("restated carried finding lost identity or was relaxed: %#v", finding)
		}
	}
	if len(findings.Items) != 2 || !ids[unsafeID] {
		t.Fatalf("effective findings = %#v, want two stable unique ids", findings.Items)
	}
	if err := exec.Respond(types.StepReview, types.ActionApprove, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestExecutor_NonActionableCarryDoesNotGateFreshNonblockingFinding(t *testing.T) {
	database, p, run, repo := setupTest(t)
	initial := `{"findings":[{"id":"review-1","severity":"error","description":"selected defect","action":"ask-user"},{"id":"review-2","severity":"info","description":"informational carry","action":"no-op"}],"summary":"two"}`
	rereview := `{"findings":[{"id":"review-3","severity":"info","description":"optional cleanup","action":"auto-fix"}],"summary":"one suggestion"}`
	calls := 0
	step := &scopeLimitedAdaptiveCallStep{adaptiveCallStep: adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			calls++
			if calls == 1 {
				return &StepOutcome{NeedsApproval: true, Findings: initial}, nil
			}
			return &StepOutcome{AutoFixable: true, Findings: rereview}, nil
		},
	}}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)
	done, _ := startExecutor(t, exec, run, repo, t.TempDir())
	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)
	selectedID := findingIDByDescription(t, database, run.ID, types.StepReview, "selected defect")
	if err := exec.Respond(types.StepReview, types.ActionFix, []string{selectedID}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("non-actionable carry created an approval gate")
	}
}

func TestExecutor_DoesNotDispatchCarriedFixWhenSelectionPersistenceFails(t *testing.T) {
	database, p, run, repo := setupTest(t)
	calls := 0
	step := &scopeLimitedAdaptiveCallStep{adaptiveCallStep: adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			calls++
			return &StepOutcome{NeedsApproval: true, Findings: `{"findings":[{"id":"review-1","severity":"error","description":"must persist","action":"ask-user"}]}`}, nil
		},
	}}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)
	done, _ := startExecutor(t, exec, run, repo, t.TempDir())
	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)
	selectedID := findingIDByDescription(t, database, run.ID, types.StepReview, "must persist")
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Respond(types.StepReview, types.ActionFix, []string{selectedID}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "record review user decision") {
			t.Fatalf("executor error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor did not fail after selection persistence failed")
	}
	if calls != 1 {
		t.Fatalf("review calls = %d, want no verification dispatch", calls)
	}
}

func TestExecutor_FixEmitsFixingStatusImmediately(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	fixStarted := make(chan struct{})
	releaseFix := make(chan struct{})
	callCount := 0
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			callCount++
			if callCount == 1 {
				return &StepOutcome{NeedsApproval: true, Findings: `{"issues":["bug"]}`}, nil
			}
			close(fixStarted)
			<-releaseFix
			return &StepOutcome{ExitCode: 0}, nil
		},
	}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)
	events := collectEvents(exec)

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)
	if err := exec.Respond(types.StepReview, types.ActionFix, nil); err != nil {
		t.Fatal(err)
	}

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusFixing)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if event := events.findLast(ipc.EventStepCompleted, string(types.StepStatusFixing)); event != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if event := events.findLast(ipc.EventStepCompleted, string(types.StepStatusFixing)); event == nil {
		close(releaseFix)
		<-done
		t.Fatal("expected step_completed event with fixing status after fix was accepted")
	}

	<-fixStarted
	close(releaseFix)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor timed out")
	}
}

func TestExecutor_FixingEventIncludesFindingStats(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()
	releaseFix := make(chan struct{})
	callCount := 0
	findings := `{"findings":[{"id":"r1","severity":"warning","description":"one","action":"auto-fix"},{"id":"r2","severity":"warning","description":"two","action":"auto-fix"}],"summary":"two"}`
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			callCount++
			if callCount == 1 {
				return &StepOutcome{NeedsApproval: true, Findings: findings}, nil
			}
			<-releaseFix
			return &StepOutcome{}, nil
		},
	}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)
	events := collectEvents(exec)
	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)
	if err := exec.Respond(types.StepReview, types.ActionFix, []string{"r1"}); err != nil {
		t.Fatal(err)
	}
	fixingEvent := waitForEvent(t, events, ipc.EventStepCompleted, string(types.StepStatusFixing))
	if fixingEvent.FixedFindings == nil || *fixingEvent.FixedFindings != 0 {
		close(releaseFix)
		<-done
		t.Fatalf("fixed findings = %v, want 0", fixingEvent.FixedFindings)
	}
	if fixingEvent.ReportedFindings == nil || *fixingEvent.ReportedFindings != 2 {
		close(releaseFix)
		<-done
		t.Fatalf("reported findings = %v, want 2", fixingEvent.ReportedFindings)
	}

	close(releaseFix)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor timed out")
	}
}

func TestExecutor_FixReviewNoChanges(t *testing.T) {
	database, p, run, repo := setupTest(t)

	// Create a real git repo as workDir
	workDir := t.TempDir()
	initGitRepo(t, workDir)

	// Step that needs approval both times but agent makes no changes on fix
	callCount := 0
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			callCount++
			return &StepOutcome{NeedsApproval: true, Findings: `{"items":[]}`}, nil
		},
	}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)
	events := collectEvents(exec)

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)
	exec.Respond(types.StepReview, types.ActionFix, nil)

	fixEvent := waitForEvent(t, events, ipc.EventStepCompleted, string(types.StepStatusFixReview))
	if fixEvent.Status == nil || *fixEvent.Status != string(types.StepStatusFixReview) {
		t.Errorf("expected fix_review status, got %v", fixEvent.Status)
	}

	exec.Respond(types.StepReview, types.ActionApprove, nil)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor timed out")
	}
}

func TestExecutor_FixSetsPreviousFindings(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	findings := `{"findings":[{"severity":"error","file":"main.go","line":42,"description":"nil pointer dereference","action":"auto-fix"}],"summary":"1 error found"}`
	var capturedFindings string

	callCount := 0
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			callCount++
			if callCount == 1 {
				// First call: return findings that need approval
				return &StepOutcome{NeedsApproval: true, Findings: findings}, nil
			}
			// Second call (fix): capture PreviousFindings and pass
			capturedFindings = sctx.PreviousFindings
			return &StepOutcome{}, nil
		},
	}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)
	exec.Respond(types.StepReview, types.ActionFix, nil)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor timed out")
	}

	payload, err := types.ParseFindingsJSON(capturedFindings)
	if err != nil {
		t.Fatalf("parse PreviousFindings: %v", err)
	}
	if len(payload.Items) != 0 {
		t.Fatalf("expected 0 findings, got %d", len(payload.Items))
	}
	if payload.Summary != "0 selected findings" {
		t.Fatalf("summary = %q, want %q", payload.Summary, "0 selected findings")
	}
}

func TestExecutor_AssignsFindingIDsBeforePersistingAndEmitting(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			return &StepOutcome{
				NeedsApproval: true,
				Findings:      `{"findings":[{"severity":"error","description":"first","action":"auto-fix"},{"severity":"warning","description":"second","action":"auto-fix"}],"summary":"2 findings"}`,
			}, nil
		},
	}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)
	events := collectEvents(exec)

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)

	paused := waitForStepEvent(t, events, ipc.EventStepCompleted, types.StepReview)
	if paused.Findings == nil {
		t.Fatal("expected paused step event with findings")
	}

	items := mustParseFindingItems(t, *paused.Findings)
	if len(items) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(items))
	}
	if items[0].ID == "" || items[1].ID == "" || items[0].ID == items[1].ID || !items[0].IDGenerated || !items[1].IDGenerated {
		t.Fatalf("unexpected finding IDs: %#v", items)
	}

	steps, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if steps[0].FindingsJSON == nil {
		t.Fatal("expected findings stored in DB")
	}
	storedItems := mustParseFindingItems(t, *steps[0].FindingsJSON)
	if len(storedItems) != 2 {
		t.Fatalf("expected 2 stored findings, got %d", len(storedItems))
	}
	if storedItems[0].ID != items[0].ID || storedItems[1].ID != items[1].ID {
		t.Fatalf("unexpected stored finding IDs: %#v", storedItems)
	}

	if err := exec.Respond(types.StepReview, types.ActionAbort, nil); err != nil {
		t.Fatal(err)
	}
	<-done
}

func TestExecutor_FixAppliesUserInstructionsAndAddedFindings(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	var capturedFindings string
	var capturedDurableFindings string
	callCount := 0
	step := &scopeLimitedAdaptiveCallStep{adaptiveCallStep: adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			callCount++
			if callCount == 1 {
				return &StepOutcome{
					NeedsApproval: true,
					Findings:      `{"findings":[{"id":"review-1","severity":"error","description":"first","action":"auto-fix"}],"summary":"1 finding"}`,
				}, nil
			}
			capturedFindings = sctx.PreviousFindings
			stored, err := sctx.DB.GetStepResult(sctx.StepResultID)
			if err != nil {
				t.Fatal(err)
			}
			if stored == nil || stored.FindingsJSON == nil {
				t.Fatalf("durable review lineages = %#v", stored)
			}
			capturedDurableFindings = *stored.FindingsJSON
			return &StepOutcome{}, nil
		},
	}}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)
	selectedID := findingIDByDescription(t, database, run.ID, types.StepReview, "first")
	instructions := map[string]string{selectedID: "only touch parser.go, skip helpers"}
	added := []types.Finding{{Severity: "warning", Description: "also audit logger init", Action: types.ActionAutoFix}}
	if err := exec.RespondWithOverrides(types.StepReview, types.ActionFix, []string{selectedID}, instructions, added); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor timed out")
	}

	parsedFindings, err := types.ParseFindingsJSON(capturedFindings)
	if err != nil {
		t.Fatal(err)
	}
	items := parsedFindings.Items
	if len(items) != 2 {
		t.Fatalf("expected 2 findings (selected + user-added), got %d: %s", len(items), capturedFindings)
	}
	if items[0].ID != selectedID {
		t.Errorf("expected selected agent finding first, got %q", items[0].ID)
	}
	if items[0].UserInstructions != "only touch parser.go, skip helpers" {
		t.Errorf("expected instruction attached to review-1, got %q", items[0].UserInstructions)
	}
	if items[1].ID != "user-1" {
		t.Errorf("expected user-added finding to get ID user-1, got %q", items[1].ID)
	}
	if items[1].Source != types.FindingSourceUser {
		t.Errorf("expected user-added finding to be tagged source=user, got %q", items[1].Source)
	}
	if !items[1].HasLineage() || len(items[1].ContinuityToken) != 32 {
		t.Fatalf("user-added finding has no durable lineage: %#v", items[1])
	}
	parsedDurable, err := types.ParseFindingsJSON(capturedDurableFindings)
	if err != nil {
		t.Fatal(err)
	}
	durableItems := parsedDurable.Items
	var durableUser *types.Finding
	for i := range durableItems {
		if durableItems[i].ID == items[1].ID {
			durableUser = &durableItems[i]
			break
		}
	}
	if durableUser == nil || durableUser.ContinuityToken != items[1].ContinuityToken || durableUser.Source != types.FindingSourceUser {
		t.Fatalf("durable user lineage = %#v, rereview finding = %#v", durableUser, items[1])
	}

	rounds, err := database.GetRoundsByStep(firstStepID(t, database, run.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(rounds) == 0 {
		t.Fatal("expected at least one round")
	}
	round := rounds[0]
	if round.UserFindingsJSON == nil {
		t.Fatal("expected user_findings_json to be persisted on the selection round")
	}
	if !strings.Contains(*round.UserFindingsJSON, "audit logger init") {
		t.Errorf("expected user findings payload to include user-added description, got %s", *round.UserFindingsJSON)
	}
	if round.SelectedFindingIDs == nil {
		t.Fatal("expected selected_finding_ids to be set")
	}
	if !strings.Contains(*round.SelectedFindingIDs, "user-1") {
		t.Errorf("expected user-added finding id in selected list, got %s", *round.SelectedFindingIDs)
	}
}

func firstStepID(t *testing.T, database *db.DB, runID string) string {
	t.Helper()
	steps, err := database.GetStepsByRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) == 0 {
		t.Fatal("no steps persisted")
	}
	return steps[0].ID
}

func TestExecutor_FixUsesSelectedFindingIDsOnly(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	var capturedFindings string
	callCount := 0
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			callCount++
			if callCount == 1 {
				return &StepOutcome{
					NeedsApproval: true,
					Findings:      `{"findings":[{"id":"review-1","severity":"error","description":"first","action":"auto-fix"},{"id":"review-2","severity":"warning","description":"second","action":"auto-fix"}],"summary":"2 findings"}`,
				}, nil
			}
			capturedFindings = sctx.PreviousFindings
			return &StepOutcome{}, nil
		},
	}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)
	selectedID := findingIDByDescription(t, database, run.ID, types.StepReview, "second")
	if err := exec.Respond(types.StepReview, types.ActionFix, []string{selectedID}); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor timed out")
	}

	items := mustParseFindingItems(t, capturedFindings)
	if len(items) != 1 {
		t.Fatalf("expected 1 selected finding, got %d", len(items))
	}
	if items[0].ID != selectedID || items[0].Description != "second" {
		t.Fatalf("unexpected selected finding: %#v", items[0])
	}
}

func TestExecutor_FixClearsStoredFindingsAfterSuccessfulReRun(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	callCount := 0
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			callCount++
			if callCount == 1 {
				return &StepOutcome{
					NeedsApproval: true,
					Findings:      `{"findings":[{"severity":"error","description":"first pass issue","action":"auto-fix"}],"summary":"1 issue"}`,
				}, nil
			}
			return &StepOutcome{}, nil
		},
	}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)
	if err := exec.Respond(types.StepReview, types.ActionFix, nil); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor timed out")
	}

	dbSteps, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dbSteps[0].FindingsJSON != nil {
		t.Fatalf("expected findings to be cleared, got %q", *dbSteps[0].FindingsJSON)
	}
}

func TestExecutor_FixPersistsFollowUpRoundAsAutoFix(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	callCount := 0
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			callCount++
			if callCount == 1 {
				return &StepOutcome{
					NeedsApproval: true,
					Findings:      `{"findings":[{"severity":"error","description":"first pass issue","action":"auto-fix"}],"summary":"1 issue"}`,
				}, nil
			}
			return &StepOutcome{}, nil
		},
	}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)
	if err := exec.Respond(types.StepReview, types.ActionFix, nil); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor timed out")
	}

	dbSteps, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dbSteps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(dbSteps))
	}

	rounds, err := database.GetRoundsByStep(dbSteps[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rounds) != 2 {
		t.Fatalf("expected 2 rounds, got %d", len(rounds))
	}
	if rounds[0].Trigger != "initial" {
		t.Fatalf("round 1 trigger = %q, want %q", rounds[0].Trigger, "initial")
	}
	if rounds[1].Trigger != "auto_fix" {
		t.Fatalf("round 2 trigger = %q, want %q", rounds[1].Trigger, "auto_fix")
	}
}

func TestExecutor_FixSelectedFindingsRewritesSummary(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	var capturedFindings string
	callCount := 0
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			callCount++
			if callCount == 1 {
				return &StepOutcome{
					NeedsApproval: true,
					Findings:      `{"findings":[{"id":"review-1","severity":"error","description":"first","action":"auto-fix"},{"id":"review-2","severity":"warning","description":"second","action":"auto-fix"}],"summary":"2 findings"}`,
				}, nil
			}
			capturedFindings = sctx.PreviousFindings
			return &StepOutcome{}, nil
		},
	}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)
	selectedID := findingIDByDescription(t, database, run.ID, types.StepReview, "second")
	if err := exec.Respond(types.StepReview, types.ActionFix, []string{selectedID}); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor timed out")
	}

	var payload struct {
		Findings []findingJSON `json:"findings"`
		Summary  string        `json:"summary"`
	}
	if err := json.Unmarshal([]byte(capturedFindings), &payload); err != nil {
		t.Fatalf("parse findings JSON: %v", err)
	}
	if len(payload.Findings) != 1 || payload.Findings[0].ID != selectedID {
		t.Fatalf("unexpected selected findings payload: %#v", payload.Findings)
	}
	if payload.Summary != "1 selected finding" {
		t.Fatalf("summary = %q, want %q", payload.Summary, "1 selected finding")
	}
}

func TestExecutor_UserFixRecordsSelectedFindingIDsAndFixSummary(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	callCount := 0
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			callCount++
			if callCount == 1 {
				return &StepOutcome{
					NeedsApproval: true,
					Findings:      `{"findings":[{"id":"review-1","severity":"error","description":"first","action":"auto-fix"},{"id":"review-2","severity":"warning","description":"second","action":"auto-fix"}],"summary":"2 findings"}`,
				}, nil
			}
			return &StepOutcome{FixSummary: "fix the warning"}, nil
		},
	}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)
	selectedID := findingIDByDescription(t, database, run.ID, types.StepReview, "second")
	if err := exec.Respond(types.StepReview, types.ActionFix, []string{selectedID}); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor timed out")
	}

	dbSteps, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dbSteps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(dbSteps))
	}
	rounds, err := database.GetRoundsByStep(dbSteps[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rounds) != 2 {
		t.Fatalf("expected 2 rounds, got %d", len(rounds))
	}

	if rounds[0].SelectedFindingIDs == nil {
		t.Fatal("expected selected_finding_ids set on round 1")
	}
	var ids []string
	if err := json.Unmarshal([]byte(*rounds[0].SelectedFindingIDs), &ids); err != nil {
		t.Fatalf("parse selected_finding_ids: %v", err)
	}
	if len(ids) != 1 || ids[0] != selectedID {
		t.Fatalf("unexpected selected ids: %v", ids)
	}

	if rounds[1].FixSummary == nil || *rounds[1].FixSummary != "fix the warning" {
		t.Fatalf("expected fix_summary %q on round 2, got %v", "fix the warning", rounds[1].FixSummary)
	}
}

func TestExecutor_AutoFixRecordsSelectedFindingIDs(t *testing.T) {
	database, p, run, repo := setupTest(t)
	cfg := &config.Config{AutoFix: config.AutoFix{Review: 1}}
	workDir := t.TempDir()

	callCount := 0
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			callCount++
			if callCount == 1 {
				return &StepOutcome{
					AutoFixable: true,
					Findings:    `{"findings":[{"id":"review-1","severity":"warning","description":"a","action":"auto-fix"},{"id":"review-2","severity":"warning","description":"b","action":"ask-user"}],"summary":"2"}`,
				}, nil
			}
			return &StepOutcome{FixSummary: "apply cheap fix"}, nil
		},
	}

	exec := NewExecutor(database, p, cfg, nil, []Step{step}, nil)
	if err := exec.Execute(context.Background(), run, repo, workDir); err != nil {
		t.Fatalf("execute: %v", err)
	}

	dbSteps, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	rounds, err := database.GetRoundsByStep(dbSteps[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rounds) != 2 {
		t.Fatalf("expected 2 rounds, got %d", len(rounds))
	}
	if rounds[0].SelectedFindingIDs == nil {
		t.Fatal("expected selected_finding_ids set on round 1 after auto-fix")
	}
	var ids []string
	if err := json.Unmarshal([]byte(*rounds[0].SelectedFindingIDs), &ids); err != nil {
		t.Fatalf("parse selected_finding_ids: %v", err)
	}
	roundFindings, err := types.ParseFindingsJSON(*rounds[0].FindingsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || len(roundFindings.Items) != 2 || ids[0] != roundFindings.Items[0].ID || roundFindings.Items[0].Description != "a" {
		t.Fatalf("expected only auto-fixable lineage to be recorded, got %v from %#v", ids, roundFindings.Items)
	}
	if rounds[1].FixSummary == nil || *rounds[1].FixSummary != "apply cheap fix" {
		t.Fatalf("expected fix_summary persisted on round 2, got %v", rounds[1].FixSummary)
	}
}

func TestRoundInsertIDClearsOnInsertFailure(t *testing.T) {
	round := &db.StepRound{ID: "round-2"}
	if got := roundInsertID("round-1", round, nil); got != "round-2" {
		t.Fatalf("roundInsertID success = %q, want %q", got, "round-2")
	}
	if got := roundInsertID("round-1", nil, context.Canceled); got != "" {
		t.Fatalf("roundInsertID failure = %q, want empty", got)
	}
}

func TestExecutor_StepResultIDIsExposedToSteps(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	var capturedStepResultID string
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			capturedStepResultID = sctx.StepResultID
			return &StepOutcome{}, nil
		},
	}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)
	if err := exec.Execute(context.Background(), run, repo, workDir); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if capturedStepResultID == "" {
		t.Fatal("expected StepContext.StepResultID to be populated")
	}
	dbSteps, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dbSteps) != 1 || dbSteps[0].ID != capturedStepResultID {
		t.Fatalf("StepResultID did not match the step's DB row (got %q, want %q)", capturedStepResultID, dbSteps[0].ID)
	}
}

func TestExecutor_PreviousFindingsEmptyOnFirstExecution(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	var capturedFindings string
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			capturedFindings = sctx.PreviousFindings
			return &StepOutcome{}, nil
		},
	}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)
	err := exec.Execute(context.Background(), run, repo, workDir)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if capturedFindings != "" {
		t.Errorf("PreviousFindings should be empty on first execution, got: %s", capturedFindings)
	}
}
