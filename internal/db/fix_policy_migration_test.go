package db

import (
	"path/filepath"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestFixPolicyAndApprovalNoteMigrateLegacyRows(t *testing.T) {
	file := filepath.Join(t.TempDir(), "legacy.db")
	d, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo, err := d.InsertRepo("/repo", "https://example.com/repo.git", "main")
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.InsertRun(repo.ID, "feature", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	step, err := d.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	round, err := d.InsertStepRound(step.ID, 1, "initial", nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"ALTER TABLE runs DROP COLUMN no_fix", "ALTER TABLE step_rounds DROP COLUMN approval_note"} {
		if _, err := d.sql.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	restored, err := migrated.GetRun(run.ID)
	if err != nil || restored.NoFix {
		t.Fatalf("legacy run must keep its existing fix policy: %+v %v", restored, err)
	}
	rounds, err := migrated.GetRoundsByStep(step.ID)
	if err != nil || len(rounds) != 1 || rounds[0].ApprovalNote != nil {
		t.Fatalf("legacy approval note must remain unknown: %+v %v", rounds, err)
	}
	if err := migrated.SetStepRoundApprovalNote(round.ID, "Accepted by adjudicator"); err != nil {
		t.Fatal(err)
	}
	rounds, err = migrated.GetRoundsByStep(step.ID)
	if err != nil || rounds[0].ApprovalNote == nil || *rounds[0].ApprovalNote != "Accepted by adjudicator" {
		t.Fatalf("new approval note missing: %+v %v", rounds, err)
	}
}
