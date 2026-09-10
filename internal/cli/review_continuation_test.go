package cli

import (
	"context"
	"encoding/json"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestReviewCompletionEventContinuesBeforeFallback(t *testing.T) {
	events := make(chan ipc.Event, 1)
	var complete atomic.Bool
	read := make(chan struct{}, 8)
	srv := ipc.NewServer()
	srv.Handle(ipc.MethodGetRun, func(context.Context, json.RawMessage) (interface{}, error) {
		status := types.StepStatusRunning
		if complete.Load() {
			status = types.StepStatusAwaitingApproval
		}
		select {
		case read <- struct{}{}:
		default:
		}
		return &ipc.GetRunResult{Run: &ipc.RunInfo{ID: "review", Status: types.RunRunning,
			Steps: []ipc.StepResultInfo{{StepName: types.StepReview, Status: status}}}}, nil
	})
	srv.HandleStream(ipc.MethodSubscribe, func(ctx context.Context, _ json.RawMessage) (ipc.StreamFunc, error) {
		return func(send func(interface{}) error) error {
			select {
			case event := <-events:
				if err := send(event); err != nil {
					return err
				}
			case <-ctx.Done():
				return nil
			}
			<-ctx.Done()
			return nil
		}, nil
	})
	client, socket := startDriveTestServer(t, srv)
	r := newRunReconciler(&ipcRunStateSource{socketPath: socket}, "review")
	r.heartbeatInterval = time.Hour
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		run, _, err := driveRunWithReconciler(ctx, io.Discard, client, r, "review", false)
		if err == nil && run.Steps[0].Status != types.StepStatusAwaitingApproval {
			t.Error("completion did not return the review decision")
		}
		done <- err
	}()
	select {
	case <-read:
	case <-ctx.Done():
		t.Fatal("initial reconciliation missing")
	}
	complete.Store(true)
	events <- ipc.Event{Type: ipc.EventStepCompleted, RunID: "review"}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		t.Log("observed review completion across IPC and continued with a one-hour fallback")
	case <-ctx.Done():
		t.Fatal("completion event did not wake driver")
	}
}

func TestReviewFallbackAndDuplicateEventsCannotContinueGateTwice(t *testing.T) {
	events := make(chan ipc.Event, 8)
	reads := make(chan struct{}, 100)
	var terminal atomic.Bool
	var responds atomic.Int32
	source := &gateRoundStateSource{events: events, run: func() *ipc.RunInfo {
		status := types.RunRunning
		if terminal.Load() {
			status = types.RunCompleted
		}
		select {
		case reads <- struct{}{}:
		default:
		}
		return &ipc.RunInfo{ID: "review", Status: status,
			Steps: []ipc.StepResultInfo{{StepName: types.StepReview, Status: types.StepStatusAwaitingApproval}}}
	}}
	srv := ipc.NewServer()
	srv.Handle(ipc.MethodRespond, func(context.Context, json.RawMessage) (interface{}, error) {
		responds.Add(1)
		return &ipc.RespondResult{OK: true}, nil
	})
	client, _ := startDriveTestServer(t, srv)
	r := newRunReconciler(source, "review")
	r.heartbeatInterval = 10 * time.Millisecond
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, err := driveRunWithReconciler(ctx, io.Discard, client, r, "review", true); done <- err }()
	// Three reconciliations with no events prove the fallback actually fired.
	for range 3 {
		select {
		case <-reads:
		case <-ctx.Done():
			t.Fatal("fallback did not fire")
		}
	}
	for range 3 {
		events <- ipc.Event{Type: ipc.EventStepCompleted, RunID: "review"}
	}
	select {
	case <-reads:
	case <-ctx.Done():
		t.Fatal("duplicate events not reconciled")
	}
	terminal.Store(true)
	events <- ipc.Event{Type: ipc.EventRunUpdated, RunID: "review"}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("driver did not finish")
	}
	if got := responds.Load(); got != 1 {
		t.Fatalf("continued same gate %d times, want one", got)
	}
	t.Log("fallback fired and duplicate completion events left exactly one continuation")
}
