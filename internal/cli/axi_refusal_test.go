package cli

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/types"
	toon "github.com/toon-format/toon-go"
)

func TestAxiStatusReportsRefusedFixTurnAndBudgetReason(t *testing.T) {
	repoDir, _, database, repo := setupAxiQueryRepo(t)
	chdir(t, repoDir)
	run, err := database.InsertRun(repo.ID, "feature/refusal", "head", "base")
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
	for _, limit := range []int{0, 2} {
		if _, err := database.InsertAgentInvocation(db.AgentInvocation{
			RunID: run.ID, StepName: "review", Purpose: "review-fix", Round: limit + 1,
			Agent: "claude", StartedAt: 100, CompletedAt: 101, DurationMS: 1000,
			ExitStatus: "refused", FailureCategory: "fix_budget_exhausted", FixBudgetLimit: &limit,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, status := range []string{"ok", "error", "cancelled"} {
		if _, err := database.InsertAgentInvocation(db.AgentInvocation{
			RunID: run.ID, StepName: "review", Purpose: "review-fix", Round: 4,
			Agent: "claude", StartedAt: 102, CompletedAt: 103, DurationMS: 1000, ExitStatus: status,
		}); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	cmd := newAxiStatusCmd()
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--run", run.ID})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	var document struct {
		ReviewTiming struct {
			Turns []db.ReviewTurnTiming `toon:"turns"`
		} `toon:"review_timing"`
	}
	if err := toon.UnmarshalString(output, &document); err != nil {
		t.Fatalf("decode axi status: %v\n%s", err, output)
	}
	turns := document.ReviewTiming.Turns
	if len(turns) != 5 {
		t.Fatalf("expected five turns:\n%s", output)
	}
	for i, limit := range []int{0, 2} {
		if turns[i].ExitStatus != "refused" || turns[i].Reason != fmt.Sprintf("fix budget exhausted (MO_GATE_FIX_ROUNDS=%d)", limit) {
			t.Fatalf("incorrect refused turn: %+v", turns[i])
		}
	}
	for i, status := range []string{"ok", "error", "cancelled"} {
		if turns[i+2].ExitStatus != status || turns[i+2].Reason != "" {
			t.Fatalf("ordinary outcome changed: %+v", turns[i+2])
		}
	}
}
