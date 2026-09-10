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

func TestAxiStatusPrintsFailedReviewTimingStatus(t *testing.T) {
	repoDir, _, database, repo := setupAxiQueryRepo(t)
	chdir(t, repoDir)
	run, err := database.InsertRun(repo.ID, "feature/timing-failure", "head", "base")
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
	if _, err := database.InsertAgentInvocation(db.AgentInvocation{
		RunID: run.ID, StepName: string(types.StepReview), Purpose: "review", Round: 1,
		Agent: "mock", StartedAt: 100, CompletedAt: 101, DurationMS: 1000, ExitStatus: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.FailStep(step.ID, "invalid review report", 1000); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	if _, err := runAxiStatus(cmd, run.ID); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	if !strings.Contains(output, "review_timing:\n  started_at:") || !strings.Contains(output, "  status: failed\n") {
		t.Fatalf("failed review status missing from axi status: %s", output)
	}
}
