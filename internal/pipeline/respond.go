package pipeline

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func responseHash(params ipc.RespondParams) (string, error) {
	if err := ipc.ValidateResponseKey(params.IdempotencyKey); err != nil {
		return "", err
	}
	switch params.Action {
	case types.ActionApprove, types.ActionFix, types.ActionSkip, types.ActionAbort:
	default:
		return "", fmt.Errorf("unknown response action %q", params.Action)
	}
	data, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("encode response: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// ReplayResponse returns a durable acceptance even after an executor exits.
// Reusing a key with a different ruling fails rather than silently losing edits.
func ReplayResponse(database *db.DB, params ipc.RespondParams) (*ipc.RespondResult, error) {
	hash, err := responseHash(params)
	if err != nil {
		return nil, err
	}
	if params.IdempotencyKey == "" {
		return nil, nil
	}
	receipt, err := database.GetResponseReceipt(params.RunID, params.IdempotencyKey)
	if err != nil || receipt == nil {
		return nil, err
	}
	if receipt.RequestHash != hash {
		return nil, fmt.Errorf("idempotency key already accepted a different response")
	}
	return &ipc.RespondResult{OK: true, RunID: receipt.RunID, Step: receipt.Step, Round: receipt.Round, IdempotencyKey: receipt.IdempotencyKey, Replayed: true}, nil
}

// AcceptResponse serializes receipt persistence and queue admission with gate
// resolution. The buffered handoff never waits for a fix agent. A receipt proves
// acceptance only; cancellation or daemon failure can still interrupt execution.
func (e *Executor) AcceptResponse(params ipc.RespondParams) (*ipc.RespondResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if receipt, err := ReplayResponse(e.db, params); receipt != nil || err != nil {
		return receipt, err
	}
	if !e.waiting {
		return nil, fmt.Errorf("no step awaiting approval")
	}
	if params.RunID != e.waitingRunID {
		return nil, fmt.Errorf("run mismatch: response does not target the waiting run")
	}
	if params.Step != e.waitingStep {
		return nil, fmt.Errorf("step mismatch: responding to %q but %q is awaiting approval", params.Step, e.waitingStep)
	}
	round, err := e.db.LatestStepRound(e.waitingStepResultID)
	if err != nil {
		return nil, err
	}
	result := &ipc.RespondResult{OK: true, RunID: params.RunID, Step: params.Step, Round: round, IdempotencyKey: params.IdempotencyKey}
	if params.IdempotencyKey != "" {
		hash, err := responseHash(params)
		if err != nil {
			return nil, err
		}
		if err := e.db.InsertResponseReceipt(db.ResponseReceipt{RunID: params.RunID, IdempotencyKey: params.IdempotencyKey, Step: params.Step, Round: result.Round, RequestHash: hash}); err != nil {
			return nil, err
		}
	}
	e.enqueueResponse(approvalResponse{action: params.Action, findingIDs: params.FindingIDs, instructions: params.Instructions, addedFindings: params.AddedFindings})
	return result, nil
}
