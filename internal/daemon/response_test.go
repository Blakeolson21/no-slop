package daemon

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/pipeline"
	"github.com/Blakeolson21/no-slop/internal/types"
)

type receiptBlockedFixStep struct {
	started chan struct{}
	release chan struct{}
	fixes   atomic.Int32
}

func (s *receiptBlockedFixStep) Name() types.StepName { return types.StepReview }
func (s *receiptBlockedFixStep) Execute(ctx *pipeline.StepContext) (*pipeline.StepOutcome, error) {
	if ctx.Fixing {
		if s.fixes.Add(1) == 1 {
			close(s.started)
		}
		select {
		case <-s.release:
		case <-ctx.Ctx.Done():
			return nil, ctx.Ctx.Err()
		}
	}
	return &pipeline.StepOutcome{NeedsApproval: true, Findings: `{"findings":[],"summary":"needs review"}`}, nil
}

func TestRespondLostAcknowledgementHasDurableReceiptAndNoDuplicateFix(t *testing.T) {
	step := &receiptBlockedFixStep{started: make(chan struct{}), release: make(chan struct{})}
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step { return []pipeline.Step{step} })
	_, head := setupTestGitRepo(t, p, d, "receipt-repo")
	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var pushed ipc.PushReceivedResult
	if err := client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{Gate: p.RepoDir("receipt-repo"), Ref: "refs/heads/main", New: head}, &pushed); err != nil {
		t.Fatal(err)
	}
	waitRounds := func(want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			steps, err := d.GetStepsByRun(pushed.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if len(steps) == 1 {
				rounds, err := d.GetRoundsByStep(steps[0].ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(rounds) > want {
					t.Fatalf("duplicate response funded a round: got %d, want %d", len(rounds), want)
				}
				if len(rounds) == want && (steps[0].Status == types.StepStatusAwaitingApproval || steps[0].Status == types.StepStatusFixReview) {
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("run did not park with %d rounds", want)
	}
	waitRounds(1)
	params := ipc.RespondParams{RunID: pushed.RunID, Step: types.StepReview, Action: types.ActionFix, IdempotencyKey: "lost-ack"}
	// The write reaches the real daemon, but the read deadline expires before
	// the client can receive its acknowledgement. Discard this connection.
	lost, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	var result ipc.RespondResult
	err = lost.CallWithTimeout(ipc.MethodRespond, &params, &result, time.Nanosecond)
	lost.Close()
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected lost acknowledgement timeout, got %v", err)
	}
	select {
	case <-step.started:
	case <-time.After(5 * time.Second):
		t.Fatal("accepted fix did not start")
	}
	// The receipt is queryable while the fix is deliberately blocked.
	if err := client.Call(ipc.MethodGetResponseReceipt, &ipc.ResponseReceiptParams{RunID: pushed.RunID, IdempotencyKey: params.IdempotencyKey}, &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.RunID != pushed.RunID || result.Step != types.StepReview || result.Round != 1 {
		t.Fatalf("receipt = %+v", result)
	}
	close(step.release)
	waitRounds(2)
	for range 3 {
		if err := client.Call(ipc.MethodRespond, &params, &result); err != nil {
			t.Fatal(err)
		}
		if !result.OK || !result.Replayed || result.Round != 1 {
			t.Fatalf("replay = %+v", result)
		}
	}
	waitRounds(2)
	// Cancellation removes the executor; receipts remain queryable and replayable.
	if err := client.Call(ipc.MethodCancelRun, &ipc.CancelRunParams{RunID: pushed.RunID}, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		run, err := d.GetRun(pushed.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status == types.RunCancelled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run status = %s, want cancelled", run.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := client.Call(ipc.MethodRespond, &params, &result); err != nil || !result.Replayed {
		t.Fatalf("terminal replay = %+v, %v", result, err)
	}
}
