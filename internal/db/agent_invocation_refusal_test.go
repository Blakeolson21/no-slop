package db

import (
	"path/filepath"
	"testing"
)

func TestOpenMigratesFixBudgetLimitWithoutReclassifyingLegacyTurns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	repo, err := d.InsertRepo(t.TempDir(), "https://github.com/test/repo", "main")
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.InsertRun(repo.ID, "feature", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.InsertAgentInvocation(AgentInvocation{RunID: run.ID, StepName: "review", Round: 1, Purpose: "review-fix", Agent: "claude", ExitStatus: "error"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec(`ALTER TABLE agent_invocations DROP COLUMN fix_budget_limit`); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{0, 2} {
		if _, err := d.InsertAgentInvocation(AgentInvocation{RunID: run.ID, StepName: "review", Round: 2, Purpose: "review-fix", Agent: "claude", ExitStatus: "refused", FixBudgetLimit: &limit}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	// Opening again proves both migration idempotence and durable readback.
	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	invs, err := d.GetAgentInvocationsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(invs) != 3 || invs[0].ExitStatus != "error" || invs[0].FixBudgetLimit != nil {
		t.Fatalf("legacy outcome must remain unchanged: %+v", invs)
	}
	for i, limit := range []int{0, 2} {
		if invs[i+1].ExitStatus != "refused" || invs[i+1].FixBudgetLimit == nil || *invs[i+1].FixBudgetLimit != limit {
			t.Fatalf("refusal budget %d did not survive reopen: %+v", limit, invs[i+1])
		}
	}
}
