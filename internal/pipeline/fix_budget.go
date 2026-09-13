package pipeline

import (
	"errors"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/types"
)

var ErrFixBudgetExhausted = errors.New("fix budget exhausted")

const explicitFixBudget = 3

func (e *Executor) autoFixLimitForStep(step types.StepName, recorded *int) int {
	limit := 0
	configured := e.config != nil
	if configured {
		limit = e.config.AutoFixLimit(step)
	}
	if recorded == nil {
		return limit
	}
	if *recorded <= 0 {
		return 0
	}
	if configured && limit == 0 {
		return 0
	}
	if limit == 0 || *recorded < limit {
		return *recorded
	}
	return limit
}

func (e *Executor) fixBudgetExhausted(stepID string, autoFixLimit int) (bool, error) {
	budget := autoFixLimit
	if budget <= 0 {
		budget = explicitFixBudget
	}
	used, err := e.db.FixAttempts(stepID)
	return used >= budget, err
}
func (e *Executor) failFixBudget(stepID string, run *db.Run, repo *db.Repo, step types.StepName, duration int64) error {
	if err := e.db.FailStep(stepID, ErrFixBudgetExhausted.Error(), duration); err != nil {
		return err
	}
	if err := e.db.CompleteRunAwaitingAgent(run.ID, 0); err != nil {
		return err
	}
	e.emitStepEventWithFindingsAndError(ipc.EventStepCompleted, run, repo, step, string(types.StepStatusFailed), "", ErrFixBudgetExhausted.Error(), &duration)
	return ErrFixBudgetExhausted
}
