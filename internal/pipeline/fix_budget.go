package pipeline

import (
	"errors"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/types"
)

var ErrFixBudgetExhausted = errors.New("fix budget exhausted")

func (e *Executor) fixBudgetExhausted(stepID string, limit int) (bool, error) {
	// The explicit/--yes fix ceiling when auto-fix is disabled.
	if limit <= 0 {
		limit = 3
	}
	used, err := e.db.FixAttempts(stepID)
	return used >= limit, err
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
