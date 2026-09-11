//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestAxiResponseReceiptRetryJourney(t *testing.T) {
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: branchSyncScenario(t)})
	h.CommitChange("init-receipt", "seed.txt", "seed\n", "seed receipt init")
	initWorktree := h.AddWorktree("init-receipt")
	if out, err := h.RunInDir(initWorktree, "init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	h.CommitChange("feature/receipt", "feature.txt", "unsafe\n", "add feature")
	operator := h.AddWorktree("feature/receipt")
	gateOut, err := h.RunInDir(operator, "axi", "run", "--intent", "guard unsafe value")
	if err != nil {
		t.Fatalf("gate: %v\n%s", err, gateOut)
	}
	findingID := axiGateFindingID(t, gateOut)
	gated := waitForStepStatus(t, h, "feature/receipt", types.StepReview, types.StepStatusAwaitingApproval, 60*time.Second)
	if gated == nil {
		t.Fatal("review did not park")
	}
	args := []string{"axi", "respond", "--run", gated.ID, "--step", "review", "--action", "fix", "--findings", findingID, "--idempotency-key", "receipt-ruling", "--no-wait"}
	out, err := h.RunInDir(operator, args...)
	if err != nil {
		t.Fatalf("accept response: %v\n%s", err, out)
	}
	for _, want := range []string{"accepted: true", "round: 1", "replayed: false", "idempotency_key: receipt-ruling"} {
		if !strings.Contains(out, want) {
			t.Fatalf("receipt missing %q:\n%s", want, out)
		}
	}
	next := waitForStepStatus(t, h, "feature/receipt", types.StepReview, types.StepStatusFixReview, 60*time.Second)
	if next == nil {
		t.Fatal("fixed review did not park")
	}
	// Resolve from outside the operator worktree and after the round advanced.
	out, err = h.RunInDir(t.TempDir(), args...)
	if err != nil || !strings.Contains(out, "replayed: true") || !strings.Contains(out, "round: 1") {
		t.Fatalf("replay: %v\n%s", err, out)
	}
	out, err = h.RunInDir(t.TempDir(), "axi", "respond", "--receipt", "--run", gated.ID, "--idempotency-key", "receipt-ruling")
	if err != nil || !strings.Contains(out, "accepted: true") || !strings.Contains(out, "round: 1") {
		t.Fatalf("lookup: %v\n%s", err, out)
	}
	out, err = h.RunInDir(operator, append(args, "--instructions", "a different ruling")...)
	if err == nil || !strings.Contains(out, "different response") {
		t.Fatalf("conflict: %v\n%s", err, out)
	}
	after := waitForStepStatus(t, h, "feature/receipt", types.StepReview, types.StepStatusFixReview, 60*time.Second)
	if after == nil {
		t.Fatal("replay disturbed the waiting gate")
	}
	for _, step := range after.Steps {
		if step.StepName == types.StepReview && step.FixRoundCount != 1 {
			t.Fatalf("retry funded %d fix rounds, want 1", step.FixRoundCount)
		}
	}
}
