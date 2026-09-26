package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestAxiCancelTargetsRunAndRequiresTerminalTruth(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "unconfirmed", true: "cancelled"}[terminal], func(t *testing.T) {
			newAbortQuiescenceFixture(t, func(context.Context, int) (*ipc.RunInfo, error) {
				status := types.RunRunning
				if terminal {
					status = types.RunCancelled
				}
				return &ipc.RunInfo{ID: "run-quiesce", Branch: "feature/abort", Status: status}, nil
			})
			out, err := executeCmd("axi", "cancel", "run-quiesce")
			if terminal {
				if err != nil || !strings.Contains(out, "run_status: cancelled") {
					t.Fatalf("cancel: %v\n%s", err, out)
				}
			} else {
				if err == nil {
					t.Fatal("cancel succeeded before terminal state")
				}
				assertUnconfirmedAbortOutput(t, "cancel", out)
			}
		})
	}
}

func TestAxiCancelIsGuardedMutation(t *testing.T) {
	cmd, _, err := newRootCmd().Find([]string{"axi", "cancel"})
	if err != nil || cmd.Name() != "cancel" || !mutatesPipelineControl(cmd) {
		t.Fatalf("cancel must be a guarded mutation: %v", err)
	}
}
