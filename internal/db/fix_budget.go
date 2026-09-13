package db

import (
	"fmt"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// FixAttempts includes a durable inherited budget plus this step's own spend.
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
	durable, err := d.durableInternalFixAttempts(stepID)
	if err != nil {
		return 0, err
	}
	spent := funded
	if executed > spent {
		spent = executed
	}
	if durable > 0 && funded+durable > spent {
		spent = funded + durable
	}
	return inherited + spent, nil
}

// durableInternalFixAttempts reads the fix spend a step persisted for itself.
// A store predating the column reports zero rather than failing, matching how
// every other reader treats a missing ci_fix_attempts.
func (d *DB) durableInternalFixAttempts(stepID string) (int, error) {
	if !d.hasColumn("step_results", "ci_fix_attempts") {
		return 0, nil
	}
	var attempts int
	if err := d.sql.QueryRow(`SELECT COALESCE((SELECT ci_fix_attempts FROM step_results WHERE id=?),0)`, stepID).Scan(&attempts); err != nil {
		return 0, err
	}
	if attempts < 0 {
		return 0, nil
	}
	return attempts, nil
}

// InitStepFixBudget runs before binding review provenance. It records inherited
// expenditure once, so a reattach, rebase, or replacement run cannot buy a new
// budget for the same still-uncertified review work.
func (d *DB) InitStepFixBudget(stepID, repoID, branch, runID string, step types.StepName) error {
	sourceRunID := ""
	if step == types.StepReview {
		rng, err := d.GetUncertifiedPipelineRange(repoID, branch)
		if err != nil {
			return err
		}
		if rng != nil {
			sourceRunID = rng.SourceRunID
		}
	}
	return d.initStepFixBudget(stepID, runID, step, sourceRunID)
}

func (d *DB) InitStepFixBudgetForSource(stepID, runID string, step types.StepName, sourceRunID string) error {
	return d.initStepFixBudget(stepID, runID, step, sourceRunID)
}

func (d *DB) initStepFixBudget(stepID, runID string, step types.StepName, sourceRunID string) error {
	inherited := 0
	if step == types.StepReview && sourceRunID != "" && sourceRunID != runID {
		steps, err := d.GetStepsByRun(sourceRunID)
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
	_, err := d.sql.Exec(`INSERT INTO step_fix_budgets(step_result_id,inherited_attempts) VALUES (?,?) ON CONFLICT(step_result_id) DO NOTHING`, stepID, inherited)
	if err != nil {
		return fmt.Errorf("persist inherited fix budget: %w", err)
	}
	return nil
}
