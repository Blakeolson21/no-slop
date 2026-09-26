package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestNoFixFlagPushOptionAndRerun(t *testing.T) {
	cmd := newAxiRunCmd()
	if err := cmd.ParseFlags([]string{"--yes", "--no-fix"}); err != nil {
		t.Fatal(err)
	}
	noFix, err := cmd.Flags().GetBool("no-fix")
	if err != nil || !noFix {
		t.Fatalf("no-fix flag: %v", err)
	}
	if !hasNoFixPushOption([]string{formatIntentPushOption("check behavior"), noFixPushOption}) {
		t.Fatal("hook lost no-fix")
	}
	if hasNoFixPushOption(nil) {
		t.Fatal("ordinary push disables fixes")
	}
	if !rerunParams("repo", "feature", nil, "intent", noFix).NoFix {
		t.Fatal("no-op push fallback lost no-fix")
	}
}

func TestNoFixDriveApprovesWithoutFundingRepair(t *testing.T) {
	var approved atomic.Bool
	events := make(chan ipc.Event, 1)
	findings := `{"findings":[{"id":"r1","severity":"warning","action":"ask-user","description":"requires consent"}]}`
	source := &gateRoundStateSource{events: events, run: func() *ipc.RunInfo {
		run := &ipc.RunInfo{ID: "no-fix", NoFix: true, Status: types.RunRunning, Steps: []ipc.StepResultInfo{{StepName: types.StepReview, Status: types.StepStatusParkedForApproval, FindingsJSON: &findings}}}
		if approved.Load() {
			run.Status = types.RunCompleted
			run.Steps = nil
		}
		return run
	}}
	srv := ipc.NewServer()
	srv.Handle(ipc.MethodRespond, func(_ context.Context, raw json.RawMessage) (interface{}, error) {
		var p ipc.RespondParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if p.Action != types.ActionApprove || len(p.FindingIDs) != 0 {
			return nil, fmt.Errorf("no-fix funded repair: %+v", p)
		}
		approved.Store(true)
		events <- ipc.Event{Type: ipc.EventRunUpdated, RunID: "no-fix"}
		return &ipc.RespondResult{OK: true, RunID: p.RunID, Step: p.Step, IdempotencyKey: p.IdempotencyKey}, nil
	})
	client, _ := startDriveTestServer(t, srv)
	reconciler := newRunReconciler(source, "no-fix")
	defer reconciler.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	run, _, err := driveRunWithReconciler(ctx, io.Discard, client, reconciler, "no-fix", true)
	if err != nil || run.Status != types.RunCompleted || !approved.Load() {
		t.Fatalf("no-fix drive: %+v %v", run, err)
	}
}
