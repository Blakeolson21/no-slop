package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestApprovalNoteFlagAndTransport(t *testing.T) {
	cmd := newAxiRespondCmd()
	const note = "The compatibility caller validates this input."
	if err := cmd.ParseFlags([]string{"--action", "approve", "--note", note}); err != nil {
		t.Fatal(err)
	}
	got, err := cmd.Flags().GetString("note")
	if err != nil || got != note {
		t.Fatalf("note=%q: %v", got, err)
	}
	srv := ipc.NewServer()
	received := make(chan ipc.RespondParams, 1)
	srv.Handle(ipc.MethodRespond, func(_ context.Context, raw json.RawMessage) (interface{}, error) {
		var p ipc.RespondParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		received <- p
		return &ipc.RespondResult{OK: true}, nil
	})
	client, _ := startDriveTestServer(t, srv)
	if err := sendRespond(client, "run-note", types.StepReview, types.ActionApprove, nil, nil, nil, got); err != nil {
		t.Fatal(err)
	}
	p := <-received
	if p.Note != note || p.Action != types.ActionApprove || p.RunID != "run-note" {
		t.Fatalf("response=%+v", p)
	}
}
