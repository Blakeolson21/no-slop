package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/types"
	"github.com/spf13/cobra"
)

func TestAxiStatusPrintsReviewTimingFromStore(t *testing.T) {
	repoDir, _, database, repo := setupAxiQueryRepo(t)
	chdir(t, repoDir)
	run, err := database.InsertRun(repo.ID, "feature/timing", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	step, err := database.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.StartStep(step.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.InsertAgentInvocation(db.AgentInvocation{RunID: run.ID, StepName: "review", Purpose: "review", Round: 1, Agent: "mock", StartedAt: 100, CompletedAt: 102, DurationMS: 2000, ExitStatus: "ok"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	if _, err := runAxiStatus(cmd, run.ID); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"review_timing:", "review_ms: 2000", "fix_ms: 0", "round_count: 1", "latency_ms", "review,2000"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
}
