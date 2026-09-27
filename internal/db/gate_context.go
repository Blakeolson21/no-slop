package db

import (
	"context"
	"fmt"

	"github.com/Blakeolson21/no-slop/internal/types"
)

// ActiveGateStep is the minimal state needed to authenticate gate ancestry.
type ActiveGateStep struct {
	RunID    string
	RepoID   string
	Phase    types.StepName
	AgentPID int
}

// ActiveGateSteps reads the estate in one query without loading findings,
// round histories, or probing schema columns for each active run.
// Include the read-side status aliases: local preflight can inspect an older
// daemon's store before the status backfill has run.
func (d *DB) ActiveGateSteps(ctx context.Context) ([]ActiveGateStep, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT r.id, r.repo_id, s.step_name, COALESCE(s.agent_pid, 0)
 FROM runs r JOIN step_results s ON s.run_id = r.id
 WHERE r.status IN (?, ?, ?) AND s.status IN (?, ?, ?, ?, ?, ?, ?)`,
		types.RunStarting, types.LegacyRunPending, types.RunRunning,
		types.StepStatusRunning, types.StepStatusFixerRunning, types.StepStatusParkedForApproval, types.StepStatusParkedAfterFix,
		types.LegacyStepStatusFixing, types.LegacyStepStatusAwaitingApproval, types.LegacyStepStatusFixReview)
	if err != nil {
		return nil, fmt.Errorf("list active gate steps: %w", err)
	}
	defer rows.Close()
	var steps []ActiveGateStep
	for rows.Next() {
		var step ActiveGateStep
		if err := rows.Scan(&step.RunID, &step.RepoID, &step.Phase, &step.AgentPID); err != nil {
			return nil, fmt.Errorf("read active gate step: %w", err)
		}
		steps = append(steps, step)
	}
	return steps, rows.Err()
}

// GateContextRepoExists verifies registered gate identity within the same
// deadline as the rest of classification, including waiting for a DB connection.
func (d *DB) GateContextRepoExists(ctx context.Context, id string) (bool, error) {
	var exists bool
	err := d.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM repos WHERE id = ?)`, id).Scan(&exists)
	return exists, err
}

// HasActiveRuns provides the estate-wide check used by terminal recovery to
// skip active-agent lookup. Authenticated daemon ancestry is still checked.
func (d *DB) HasActiveRuns(ctx context.Context) (bool, error) {
	var active bool
	err := d.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE status IN (?, ?, ?))`,
		types.RunStarting, types.LegacyRunPending, types.RunRunning).Scan(&active)
	return active, err
}
