package db

import (
	"path/filepath"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestSetStepConvergenceRoundTrip(t *testing.T) {
	d := openTestDB(t)
	repo, err := d.InsertRepo("/tmp/repo", "https://github.com/test/repo", "main")
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	run, err := d.InsertRun(repo.ID, "b", "h", "b")
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	step, err := d.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatalf("insert step: %v", err)
	}

	got, err := d.GetStepResult(step.ID)
	if err != nil {
		t.Fatalf("get step: %v", err)
	}
	if got.ConvergenceJSON != nil {
		t.Fatalf("new step should have no convergence report, got %q", *got.ConvergenceJSON)
	}

	report := `{"round_findings":[1,1,2],"review_ms":5000,"warning":"review loop is not converging: test"}`
	if err := d.SetStepConvergence(step.ID, report); err != nil {
		t.Fatalf("set convergence: %v", err)
	}
	got, err = d.GetStepResult(step.ID)
	if err != nil {
		t.Fatalf("get step: %v", err)
	}
	if got.ConvergenceJSON == nil || *got.ConvergenceJSON != report {
		t.Fatalf("convergence json = %v, want %q", got.ConvergenceJSON, report)
	}

	steps, err := d.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatalf("get steps by run: %v", err)
	}
	if len(steps) != 1 || steps[0].ConvergenceJSON == nil || *steps[0].ConvergenceJSON != report {
		t.Fatalf("GetStepsByRun did not return the convergence report: %+v", steps)
	}
}

// TestOpenMigratesStepConvergenceColumn proves a database whose step_results
// table predates the convergence column gains it on reopen, with legacy rows
// reading back as absent rather than a fabricated report.
func TestOpenMigratesStepConvergenceColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	repo, err := d.InsertRepo("/tmp/repo", "https://github.com/test/repo", "main")
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	run, err := d.InsertRun(repo.ID, "b", "h", "b")
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if _, err := d.sql.Exec(`ALTER TABLE step_results DROP COLUMN convergence_json`); err != nil {
		t.Fatalf("drop convergence_json: %v", err)
	}
	if _, err := d.sql.Exec(
		`INSERT INTO step_results (id, run_id, step_name, step_order, status) VALUES ('legacy1', ?, 'review', 1, 'completed')`,
		run.ID,
	); err != nil {
		t.Fatalf("insert legacy step: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	d, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer d.Close()

	got, err := d.GetStepResult("legacy1")
	if err != nil {
		t.Fatalf("get legacy step after migration: %v", err)
	}
	if got == nil || got.ConvergenceJSON != nil {
		t.Fatalf("legacy step should read back with no convergence report, got %+v", got)
	}
	if err := d.SetStepConvergence("legacy1", `{"round_findings":[1]}`); err != nil {
		t.Fatalf("set convergence after migration: %v", err)
	}
}
