package db

import (
	"fmt"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// FixAttempts includes a durable inherited budget plus this step's own spend.
//
// Own spend has two independent records and they overlap, so it is the MAXIMUM
// of the two, never their sum. The round record counts funded selections and
// executed fix rounds: counting selections too means a crash after funding but
// before the round insert cannot refund the attempt, and the trigger rows cover
// older stores that recorded no selection source. The durable record is
// step_results.ci_fix_attempts, which the CI step increments itself before it
// launches an internal repair (internal/pipeline/steps/ci.go).
//
// Those two views describe the same money. Every internal CI repair that
// actually commits returns a validation restart, and the executor writes that
// execution back as one "auto_fix" round on this same step result - so summing
// would charge the common case twice. The maximum charges it once and can never
// fall below what the durable counter already recorded, so internal CI spend is
// never refunded by a reattach, rebase or restart. The residual is an internal
// repair that produced no commit and therefore no round: it raises the durable
// counter without raising the round count, and it stays charged as long as the
// durable counter leads. Where it does not lead - a CI step that also ran gate
// fix rounds - that one no-commit attempt is absorbed rather than double
// charged, which is the deliberate direction: the CI step separately enforces
// its own internal ceiling against ci_fix_attempts, so this total exists to
// stop an OUTER gate budget being refunded, not to re-bound the inner loop.
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
	spent := funded
	if executed > spent {
		spent = executed
	}
	durable, err := d.durableInternalFixAttempts(stepID)
	if err != nil {
		return 0, err
	}
	if durable > spent {
		spent = durable
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
