//go:build unix

package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// Exercise the MO #677 wire contract through the real native adapters, their
// attempt callbacks, persistence, and the timing projection served to AXI.
func TestPerfRecording_FixBudgetRefusal(t *testing.T) {
	for _, name := range []types.AgentName{types.AgentClaude, types.AgentCodex} {
		for _, limit := range []int{0, 2} {
			t.Run(fmt.Sprintf("%s/%d", name, limit), func(t *testing.T) {
				database, _, run, _ := setupTest(t)
				step, err := database.InsertStepResult(run.ID, types.StepReview)
				if err != nil {
					t.Fatal(err)
				}
				if err := database.StartStep(step.ID); err != nil {
					t.Fatal(err)
				}
				reason := fmt.Sprintf("fix budget exhausted (MO_GATE_FIX_ROUNDS=%d)", limit)
				bin := filepath.Join(t.TempDir(), "budget-wrapper")
				script := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' 'GATE_RUNNER_ERROR refused: " + reason + "' >&2\nexit 3\n"
				if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
				native, err := agent.New(name, bin, nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = native.Close() })
				wrapped := &perfRecordingAgent{inner: native, db: database, runID: run.ID, stepName: types.StepReview, round: func() int { return 2 }}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if _, err := wrapped.Run(ctx, agent.RunOpts{Purpose: "review-fix", Prompt: "fix", CWD: filepath.Dir(bin)}); err == nil {
					t.Fatal("refusal must remain an invocation failure")
				}
				invs, err := database.GetAgentInvocationsByRun(run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(invs) != 1 || invs[0].ExitStatus != "refused" {
					t.Fatalf("exhausted fix invocation = %+v, want one refused turn", invs)
				}
				timing, err := database.GetReviewTimingAt(run.ID, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				data, err := json.Marshal(timing)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(data), `"exit_status":"refused"`) || !strings.Contains(string(data), `"reason":"`+reason+`"`) {
					t.Fatalf("timing must preserve refusal and reason: %s", data)
				}
			})
		}
	}
}
