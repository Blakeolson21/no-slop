package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/Blakeolson21/no-slop/internal/db"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestSendResponseUncertainTransportNamesReceiptCheck(t *testing.T) {
	srv := ipc.NewServer()
	received := make(chan struct{})
	release := make(chan struct{})
	srv.Handle(ipc.MethodRespond, func(context.Context, json.RawMessage) (interface{}, error) {
		close(received)
		<-release
		return &ipc.RespondResult{OK: true}, nil
	})
	client, _ := startDriveTestServer(t, srv)
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := sendResponse(ctx, client, ipc.RespondParams{RunID: "run-8", Step: types.StepReview, Action: types.ActionFix, IdempotencyKey: "ruling-8"})
		done <- err
	}()
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("request never reached server")
	}
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	for _, want := range []string{"may have been accepted", "no-slop axi respond --receipt --run run-8 --idempotency-key ruling-8", "same --run, --step and --idempotency-key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestSendResponseRejectsLegacyAcknowledgement(t *testing.T) {
	srv := ipc.NewServer()
	srv.Handle(ipc.MethodRespond, func(context.Context, json.RawMessage) (interface{}, error) { return &ipc.RespondResult{OK: true}, nil })
	client, _ := startDriveTestServer(t, srv)
	_, err := sendResponse(context.Background(), client, ipc.RespondParams{RunID: "run-8", Step: types.StepReview, Action: types.ActionFix, IdempotencyKey: "ruling-8"})
	if err == nil || !strings.Contains(err.Error(), "Do not resend") {
		t.Fatalf("legacy acknowledgement = %v", err)
	}
}

func TestAxiReceiptReadsAcceptanceWithoutDaemonOrWorktree(t *testing.T) {
	_, p, database, repo := setupAxiQueryRepo(t)
	run, err := database.InsertRun(repo.ID, "feature", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.InsertResponseReceipt(db.ResponseReceipt{RunID: run.ID, IdempotencyKey: "offline-ruling", Step: types.StepReview, Round: 4, RequestHash: "digest"}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	cmd := newAxiRespondCmd()
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runAxiRespond(cmd, respondArgs{runID: run.ID, idempotencyKey: "offline-ruling", receipt: true}); err != nil {
		t.Fatalf("offline receipt: %v\n%s", err, out.String())
	}
	for _, want := range []string{"accepted: true", "round: 4", "step: review", "idempotency_key: offline-ruling"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("receipt missing %q: %s", want, out.String())
		}
	}
	if _, err := os.Stat(p.Socket()); !os.IsNotExist(err) {
		t.Fatalf("receipt lookup started a daemon: %v", err)
	}
	out.Reset()
	if err := runAxiRespond(cmd, respondArgs{runID: run.ID, idempotencyKey: "unseen", receipt: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "accepted: false") || !strings.Contains(out.String(), "in-flight request") {
		t.Fatalf("missing receipt should preserve uncertainty: %s", out.String())
	}
}
