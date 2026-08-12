package steps

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/slop/lenses"
)

func TestReviewStepFeedsEverySlopLensToReviewer(t *testing.T) {
	t.Parallel()

	dir, baseSHA, headSHA := setupGitRepo(t)
	ag := &mockAgent{
		name: "test",
		runFn: func(_ context.Context, _ agent.RunOpts) (*agent.Result, error) {
			findings, _ := json.Marshal(Findings{Summary: "clean"})
			return &agent.Result{Output: findings}, nil
		},
	}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})

	if _, err := (&ReviewStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	if len(ag.calls) != 1 {
		t.Fatalf("agent calls = %d, want 1", len(ag.calls))
	}
	for _, lens := range lenses.Catalog() {
		if !strings.Contains(ag.calls[0].Prompt, "["+lens.Name+"]") {
			t.Errorf("review prompt does not include lens %q", lens.Name)
		}
	}
}
