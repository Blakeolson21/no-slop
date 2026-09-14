package db

import (
	"database/sql"
	"fmt"

	"github.com/Blakeolson21/no-slop/internal/types"
)

// scanStatusTarget returns a scan destination for a step_results.status (or
// runs.status, via scanRunStatusTarget) column that normalizes legacy stored
// literals to the renamed tokens. This is the single read boundary for the
// rename alias window: every GetStepsByRun/GetStepResult/GetRun* read flows
// through it, so a legacy fix_review row classifies identically to a
// parked_for_responder_after_fix row. Writers emit only the new names.
func scanStatusTarget(target *types.StepStatus) any {
	var raw sql.NullString
	return &rawValue{dst: &raw, apply: func() {
		if raw.Valid {
			*target = types.NormalizeStepStatus(raw.String)
		}
	}}
}

// scanRunStatusTarget is the runs.status analogue of scanStatusTarget. It
// applies NormalizeRunStatus only (runs.status legacy "pending" maps to
// run_starting; it must never touch step_results.status, whose "pending" is
// an unrelated token).
func scanRunStatusTarget(target *types.RunStatus) any {
	var raw sql.NullString
	return &rawValue{dst: &raw, apply: func() {
		if raw.Valid {
			*target = types.NormalizeRunStatus(raw.String)
		}
	}}
}

type rawValue struct {
	dst   *sql.NullString
	apply func()
}

func (v *rawValue) Scan(src any) error {
	if err := v.dst.Scan(src); err != nil {
		return fmt.Errorf("scan status: %w", err)
	}
	v.apply()
	return nil
}

// distinctColumnStatuses returns the DISTINCT values currently stored in
// table.column (runs.status or step_results.status). Used by the startup
// validator and the store linter.
func (d *DB) distinctColumnStatuses(table, column string) ([]string, error) {
	if column != "status" || (table != "runs" && table != "step_results") {
		return nil, fmt.Errorf("distinct statuses: unsupported table %q.%q", table, column)
	}
	// Table and column names are validated above, so interpolation here is safe.
	rows, err := d.sql.Query("SELECT DISTINCT " + column + " FROM " + table + " WHERE " + column + " IS NOT NULL AND " + column + " != ''")
	if err != nil {
		return nil, fmt.Errorf("read distinct %s.%s: %w", table, column, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan distinct %s.%s: %w", table, column, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate distinct %s.%s: %w", table, column, err)
	}
	return out, nil
}

// ValidateStatusNames checks every DISTINCT runs.status and
// step_results.status value against the declared legal sets and returns an
// error naming each offending value. The daemon refuses to start on failure;
// `axi lint-store` exposes the same check interactively.
func (d *DB) ValidateStatusNames() error {
	legalRun := toNameSet(types.LegalRunStatusNames())
	legalStep := toNameSet(types.LegalStepStatusNames())

	var bad []string
	for table, legal := range map[string]map[string]bool{"runs": legalRun, "step_results": legalStep} {
		values, err := d.distinctColumnStatuses(table, "status")
		if err != nil {
			return err
		}
		for _, v := range values {
			if !legal[v] {
				bad = append(bad, table+".status = "+v)
			}
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("store contains status values outside the declared legal sets: %v", bad)
	}
	return nil
}

// MigrateStatusNames rewrites persisted legacy status literals to the renamed
// tokens in one transaction (runs.status and step_results.status). It is
// idempotent, safe to run while the daemon is up (single UPDATE statements,
// busy-timeout guarded), and returns the per-table row counts it changed.
func (d *DB) MigrateStatusNames() (runsUpdated, stepsUpdated int64, err error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return 0, 0, fmt.Errorf("begin status name migration: %w", err)
	}
	defer tx.Rollback()

	stepRes, err := tx.Exec(
		`UPDATE step_results SET status = CASE status
			WHEN ? THEN ?
			WHEN ? THEN ?
			WHEN ? THEN ?
			ELSE status END
			WHERE status IN (?, ?, ?)`,
		types.LegacyStepStatusFixReview, types.StepStatusParkedAfterFix,
		types.LegacyStepStatusAwaitingApproval, types.StepStatusParkedForApproval,
		types.LegacyStepStatusFixing, types.StepStatusFixerRunning,
		types.LegacyStepStatusFixReview, types.LegacyStepStatusAwaitingApproval, types.LegacyStepStatusFixing,
	)
	if err != nil {
		return 0, 0, fmt.Errorf("migrate step_results.status names: %w", err)
	}
	stepsUpdated, err = stepRes.RowsAffected()
	if err != nil {
		return 0, 0, fmt.Errorf("migrate step_results.status names: %w", err)
	}

	runRes, err := tx.Exec(
		`UPDATE runs SET status = CASE status WHEN ? THEN ? ELSE status END WHERE status = ?`,
		types.LegacyRunPending, types.RunStarting, types.LegacyRunPending,
	)
	if err != nil {
		return 0, 0, fmt.Errorf("migrate runs.status names: %w", err)
	}
	runsUpdated, err = runRes.RowsAffected()
	if err != nil {
		return 0, 0, fmt.Errorf("migrate runs.status names: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit status name migration: %w", err)
	}
	return runsUpdated, stepsUpdated, nil
}

func toNameSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}
