package db

import (
	"path/filepath"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/types"
)

// Seed a store with raw legacy literals (the pre-backfill state: rows written
// by a binary that still emitted the old names) and prove that reads,
// the lint, and the backfill handle them exactly like the new names.
func seedLegacyStatusRows(t *testing.T, d *DB) {
	t.Helper()
	seedRepoRow(t, d)
	_, err := d.sql.Exec(
		`INSERT INTO runs (id, repo_id, branch, head_sha, base_sha, no_mistakes_version, no_mistakes_build_sha, status, pr_state, created_at, updated_at)
		 VALUES ('run-legacy', 'repo', 'b', 'h', 'base', 'v', 'sha', 'pending', 'none', 1, 1)`)
	if err != nil {
		t.Fatalf("seed legacy run: %v", err)
	}
	legacySteps := map[string]string{
		"review": "fix_review",
		"test":   "awaiting_approval",
		"lint":   "fixing",
	}
	order := 0
	for name, status := range legacySteps {
		order++
		_, err := d.sql.Exec(
			`INSERT INTO step_results (id, run_id, step_name, step_order, status)
			 VALUES (?, 'run-legacy', ?, ?, ?)`, "step-"+name, name, order, status)
		if err != nil {
			t.Fatalf("seed legacy step %s: %v", name, err)
		}
	}
}

// TestLegacyStatusRowsReadAsNewNames proves read-side equivalence: a run and
// its steps stored with the legacy literals classify identically to the
// renamed tokens at the db read boundary (SPEC item 2).
func TestLegacyStatusRowsReadAsNewNames(t *testing.T) {
	d := openTestDB(t)
	seedLegacyStatusRows(t, d)

	run, err := d.GetRun("run-legacy")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != types.RunStarting {
		t.Errorf("legacy runs.status pending read as %q, want run_starting", run.Status)
	}

	steps, err := d.GetStepsByRun("run-legacy")
	if err != nil {
		t.Fatalf("GetStepsByRun: %v", err)
	}
	want := map[types.StepName]types.StepStatus{
		"review": types.StepStatusParkedAfterFix,
		"test":   types.StepStatusParkedForApproval,
		"lint":   types.StepStatusFixerRunning,
	}
	if len(steps) != len(want) {
		t.Fatalf("got %d steps, want %d", len(steps), len(want))
	}
	for _, st := range steps {
		if want[st.StepName] == "" {
			t.Fatalf("unexpected step %q", st.StepName)
		}
		if st.Status != want[st.StepName] {
			t.Errorf("step %q legacy status read as %q, want %q", st.StepName, st.Status, want[st.StepName])
		}
	}
}

// TestValidateStatusNamesRejectsUnknowns pins the startup validator: unknown
// status values (like the orphan 'superseded' seen on linux3) are named and
// refuse; legal values, including the legacy alias set, pass.
func TestValidateStatusNamesRejectsUnknowns(t *testing.T) {
	d := openTestDB(t)

	if err := d.ValidateStatusNames(); err != nil {
		t.Fatalf("empty store should validate: %v", err)
	}

	seedLegacyStatusRows(t, d)
	if err := d.ValidateStatusNames(); err != nil {
		t.Fatalf("legacy alias values are legal during the alias window: %v", err)
	}

	if _, err := d.sql.Exec(
		`INSERT INTO step_results (id, run_id, step_name, step_order, status)
		 VALUES ('step-bad', 'run-legacy', 'push', 9, 'superseded')`); err != nil {
		t.Fatalf("seed offending step: %v", err)
	}
	err := d.ValidateStatusNames()
	if err == nil {
		t.Fatal("unknown status value must be refused")
	}
	if !contains(err.Error(), "superseded") {
		t.Errorf("validator must name the offending value, got: %v", err)
	}
}

// TestMigrateStatusNamesBackfillsAndIsIdempotent covers the backfill command
// path: legacy literals are rewritten to the new tokens, row counts are
// reported, and a second pass is a no-op (SPEC item 3).
func TestMigrateStatusNamesBackfillsAndIsIdempotent(t *testing.T) {
	d := openTestDB(t)
	seedLegacyStatusRows(t, d)

	runsUpdated, stepsUpdated, err := d.MigrateStatusNames()
	if err != nil {
		t.Fatalf("MigrateStatusNames: %v", err)
	}
	if runsUpdated != 1 {
		t.Errorf("runs updated = %d, want 1", runsUpdated)
	}
	if stepsUpdated != 3 {
		t.Errorf("steps updated = %d, want 3", stepsUpdated)
	}

	// Post-backfill reads emit only the new names.
	run, err := d.GetRun("run-legacy")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != types.RunStarting {
		t.Errorf("post-backfill run status = %q, want run_starting", run.Status)
	}
	steps, err := d.GetStepsByRun("run-legacy")
	if err != nil {
		t.Fatalf("GetStepsByRun: %v", err)
	}
	for _, st := range steps {
		switch st.StepName {
		case "review":
			if st.Status != types.StepStatusParkedAfterFix {
				t.Errorf("review post-backfill = %q, want parked_for_responder_after_fix", st.Status)
			}
		case "test":
			if st.Status != types.StepStatusParkedForApproval {
				t.Errorf("test post-backfill = %q, want parked_for_responder_approval", st.Status)
			}
		case "lint":
			if st.Status != types.StepStatusFixerRunning {
				t.Errorf("lint post-backfill = %q, want fixer_running", st.Status)
			}
		}
	}
	if err := d.ValidateStatusNames(); err != nil {
		t.Fatalf("post-backfill store must validate: %v", err)
	}

	// Idempotent: nothing left to change.
	runsUpdated2, stepsUpdated2, err := d.MigrateStatusNames()
	if err != nil {
		t.Fatalf("second MigrateStatusNames: %v", err)
	}
	if runsUpdated2 != 0 || stepsUpdated2 != 0 {
		t.Errorf("second pass changed runs=%d steps=%d, want 0/0", runsUpdated2, stepsUpdated2)
	}
}

// TestBackfillPreservesStepLevelPending guards the ruling that the
// step_results "pending" token is unrelated and must never be rewritten.
func TestBackfillPreservesStepLevelPending(t *testing.T) {
	d := openTestDB(t)
	seedRepoRow(t, d)
	if _, err := d.sql.Exec(
		`INSERT INTO runs (id, repo_id, branch, head_sha, base_sha, no_mistakes_version, no_mistakes_build_sha, status, pr_state, created_at, updated_at)
		 VALUES ('run-p', 'repo', 'b', 'h', 'base', 'v', 'sha', 'run_starting', 'none', 1, 1)`); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if _, err := d.sql.Exec(
		`INSERT INTO step_results (id, run_id, step_name, step_order, status)
		 VALUES ('step-p', 'run-p', 'review', 1, 'pending')`); err != nil {
		t.Fatalf("seed pending step: %v", err)
	}
	if _, _, err := d.MigrateStatusNames(); err != nil {
		t.Fatalf("MigrateStatusNames: %v", err)
	}
	var status string
	if err := d.sql.QueryRow(`SELECT status FROM step_results WHERE id = 'step-p'`).Scan(&status); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "pending" {
		t.Errorf("step_results pending was rewritten to %q, must stay pending", status)
	}
}

// seedRepoRow creates the repos row the runs table references.
func seedRepoRow(t *testing.T, d *DB) {
	t.Helper()
	if _, err := d.sql.Exec(
		`INSERT INTO repos (id, working_path, upstream_url, default_branch, created_at)
		 VALUES ('repo', '/tmp/legacy-status-test', 'https://example.com/repo.git', 'main', 1)`); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// keep filepath import meaningful if future tests need it
var _ = filepath.Join
