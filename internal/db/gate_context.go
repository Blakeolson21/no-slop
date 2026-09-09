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
func (d *DB) ActiveGateSteps(ctx context.Context) ([]ActiveGateStep, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT r.id, r.repo_id, s.step_name, COALESCE(s.agent_pid, 0)
 FROM runs r JOIN step_results s ON s.run_id = r.id
 WHERE r.status IN (?, ?) AND s.status IN (?, ?, ?, ?)`,
		types.RunPending, types.RunRunning, types.StepStatusRunning, types.StepStatusFixing, types.StepStatusAwaitingApproval, types.StepStatusFixReview)
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
