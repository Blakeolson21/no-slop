package db

import (
	"fmt"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// FixAttempts includes a durable inherited budget plus this step's funded
// rounds. Count selections too: a crash after funding but before round insert
// must not refund the attempt. Trigger rows cover older stores without them.
func (d *DB) FixAttempts(stepID string) (int, error) {
	var inherited int
	if err := d.sql.QueryRow(`SELECT COALESCE((SELECT inherited_attempts FROM step_fix_budgets WHERE step_result_id=?),0)`, stepID).Scan(&inherited); err != nil {
		return 0, err
	}
	rounds, err := d.GetRoundsByStep(stepID)
	if err != nil {
		return 0, err
	}
	funded, executed := 0, 0
	for _, r := range rounds {
		if r.SelectionSource != nil && (*r.SelectionSource == RoundSelectionSourceAutoFix || *r.SelectionSource == RoundSelectionSourceUser) {
			funded++
		}
		if r.IsFixRound() {
			executed++
		}
	}
	if executed > funded {
		funded = executed
	}
	return inherited + funded, nil
}

// InitStepFixBudget runs before binding review provenance. It records inherited
// expenditure once, so a reattach, rebase, or replacement run cannot buy a new
// budget for the same still-uncertified review work.
func (d *DB) InitStepFixBudget(stepID, repoID, branch, runID string, step types.StepName) error {
	inherited := 0
	if step == types.StepReview {
		rng, err := d.GetUncertifiedPipelineRange(repoID, branch)
		if err != nil {
			return err
		}
		if rng != nil && rng.SourceRunID != runID {
			steps, err := d.GetStepsByRun(rng.SourceRunID)
			if err != nil {
				return err
			}
			for _, s := range steps {
				if s.StepName == step {
					inherited, err = d.FixAttempts(s.ID)
					if err != nil {
						return err
					}
					break
				}
			}
		}
	}
	_, err := d.sql.Exec(`INSERT INTO step_fix_budgets(step_result_id,inherited_attempts) VALUES (?,?) ON CONFLICT(step_result_id) DO NOTHING`, stepID, inherited)
	if err != nil {
		return fmt.Errorf("persist inherited fix budget: %w", err)
	}
	return nil
}
