package pipeline

import (
	"context"
	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/types"
	"slices"
	"testing"
)

func TestExplicitIntentAndRebaseTurnDuty(t *testing.T) {
	for _, step := range []types.StepName{types.StepIntent, types.StepRebase} {
		capture := &turnKindCaptureAgent{}
		wrapped := &perfRecordingAgent{inner: capture, stepName: step, round: func() int { return 1 }}
		_, err := wrapped.Run(context.Background(), agent.RunOpts{Purpose: string(step), Env: []string{"MO_GATE_STEP_KIND=forged", "MO_GATE_TURN_KIND=forged"}})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"MO_GATE_STEP_KIND=" + string(step), "MO_GATE_TURN_KIND=" + string(step)} {
			if !slices.Contains(capture.env, want) {
				t.Fatalf("missing %s in %v", want, capture.env)
			}
		}
	}
}
