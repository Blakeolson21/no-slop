package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestExecutor_CIRepairMustPassReviewBeforeAnotherPush(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	const (
		originalHead = "1111111111111111111111111111111111111111"
		repairedHead = "2222222222222222222222222222222222222222"
	)
	run.HeadSHA = originalHead

	var mu sync.Mutex
	var order []types.StepName
	record := func(name types.StepName) {
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
	}

	reviewCalls := 0
	review := &adaptiveCallStep{name: types.StepReview, fn: func(sctx *StepContext) (*StepOutcome, error) {
		record(types.StepReview)
		reviewCalls++
		if reviewCalls == 1 {
			return &StepOutcome{ReviewApprovedHeadSHA: originalHead}, nil
		}
		return &StepOutcome{
			NeedsApproval:         true,
			Findings:              `{"findings":[{"id":"repaired-head-review","severity":"error","description":"repair needs review","action":"ask-user"}]}`,
			ReviewApprovedHeadSHA: repairedHead,
		}, nil
	}}

	pushCalls := 0
	push := &adaptiveCallStep{name: types.StepPush, fn: func(sctx *StepContext) (*StepOutcome, error) {
		record(types.StepPush)
		pushCalls++
		return &StepOutcome{}, nil
	}}

	ciCalls := 0
	ci := &adaptiveCallStep{name: types.StepCI, fn: func(sctx *StepContext) (*StepOutcome, error) {
		record(types.StepCI)
		ciCalls++
		if ciCalls != 1 {
			return &StepOutcome{}, nil
		}
		if err := sctx.DB.UpdateRunHeadSHAForRevalidation(sctx.Run.ID, repairedHead); err != nil {
			return nil, err
		}
		sctx.Run.HeadSHA = repairedHead
		sctx.Run.ReviewApprovedHeadSHA = nil
		return &StepOutcome{RestartFrom: types.StepReview}, nil
	}}

	testStep := &adaptiveCallStep{name: types.StepTest, fn: func(*StepContext) (*StepOutcome, error) {
		record(types.StepTest)
		return &StepOutcome{}, nil
	}}
	exec := NewExecutor(database, p, nil, nil, []Step{review, testStep, push, ci}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- exec.Execute(ctx, run, repo, workDir) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("executor did not stop after cancellation")
		}
	})

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)

	mu.Lock()
	gotOrder := append([]types.StepName(nil), order...)
	mu.Unlock()
	wantOrder := []types.StepName{
		types.StepReview, types.StepTest, types.StepPush, types.StepCI,
		types.StepReview,
	}
	if !equalStepNames(gotOrder, wantOrder) {
		t.Fatalf("execution order = %v, want %v", gotOrder, wantOrder)
	}
	if pushCalls != 1 {
		t.Fatalf("push executed %d times; repaired head reached push before re-review", pushCalls)
	}
	gotRun, err := database.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotRun.HeadSHA != repairedHead {
		t.Fatalf("run head = %s, want repaired head %s", gotRun.HeadSHA, repairedHead)
	}
	if gotRun.ReviewApprovedHeadSHA != nil {
		t.Fatalf("stale review authority survived CI repair: %#v", gotRun.ReviewApprovedHeadSHA)
	}
	results, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		rounds, err := database.GetRoundsByStep(result.ID)
		if err != nil {
			t.Fatal(err)
		}
		if result.StepName == types.StepReview {
			if got, want := roundNumbersForRevalidation(rounds), []int{1, 2}; !equalInts(got, want) {
				t.Fatalf("review rounds = %v, want %v", got, want)
			}
		}
		if result.StepName == types.StepCI {
			if len(rounds) != 1 || rounds[0].Trigger != "auto_fix" {
				t.Fatalf("CI repair rounds = %#v, want one auto_fix-triggered round", rounds)
			}
		}
	}
}

func equalStepNames(got, want []types.StepName) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestExecutor_RecoveredRevalidationPreservesSkippedStepsAndRoundNumbers(t *testing.T) {
	database, p, run, repo := setupTest(t)
	const repairedHead = "2222222222222222222222222222222222222222"
	findings := `{"findings":[{"id":"repaired-head-review","severity":"error","description":"repair needs review","action":"ask-user"}]}`

	if err := database.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateRunHeadSHAForRevalidation(run.ID, repairedHead); err != nil {
		t.Fatal(err)
	}
	reviewResult, err := database.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	testResult, err := database.InsertStepResult(run.ID, types.StepTest)
	if err != nil {
		t.Fatal(err)
	}
	pushResult, err := database.InsertStepResult(run.ID, types.StepPush)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.StartStep(reviewResult.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.InsertReviewStepRound(reviewResult.ID, 2, "initial", &findings, nil, repairedHead, 1); err != nil {
		t.Fatal(err)
	}
	if err := database.ParkStepForApproval(run.ID, reviewResult.ID, types.StepStatusAwaitingApproval, 1, &findings); err != nil {
		t.Fatal(err)
	}
	if _, err := database.InsertStepRound(testResult.ID, 1, "initial", nil, nil, 1); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteStepWithStatus(pushResult.ID, types.StepStatusSkipped, 0, 0, ""); err != nil {
		t.Fatal(err)
	}

	review := newPassStep(types.StepReview)
	testStep := newPassStep(types.StepTest)
	push := newPassStep(types.StepPush)
	exec := NewExecutor(database, p, nil, nil, []Step{review, testStep, push}, nil)
	parkedRun, err := database.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumeWorkDir := t.TempDir()
	done := make(chan error, 1)
	go func() { done <- exec.Resume(context.Background(), parkedRun, repo, resumeWorkDir) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		err = exec.Respond(types.StepReview, types.ActionApprove, nil)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovered executor never accepted approval: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Resume() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recovered executor did not complete")
	}

	if got := review.callCount(); got != 0 {
		t.Fatalf("recovered parked review executed %d times, want approval of durable round", got)
	}
	if got := testStep.callCount(); got != 1 {
		t.Fatalf("test executed %d times, want 1", got)
	}
	if got := push.callCount(); got != 0 {
		t.Fatalf("explicitly skipped push executed %d times, want 0", got)
	}
	testRounds, err := database.GetRoundsByStep(testResult.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := roundNumbersForRevalidation(testRounds), []int{1, 2}; !equalInts(got, want) {
		t.Fatalf("recovered test rounds = %v, want %v", got, want)
	}
	gotPush, err := database.GetStepResult(pushResult.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotPush.Status != types.StepStatusSkipped {
		t.Fatalf("push status = %s, want %s", gotPush.Status, types.StepStatusSkipped)
	}
}

func roundNumbersForRevalidation(rounds []*db.StepRound) []int {
	numbers := make([]int, len(rounds))
	for i, round := range rounds {
		numbers[i] = round.Round
	}
	return numbers
}

func equalInts(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
