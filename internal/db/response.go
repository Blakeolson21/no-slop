package db

import (
	"database/sql"
	"fmt"

	"github.com/Blakeolson21/no-slop/internal/types"
)

// ResponseReceipt records acceptance, not completion of the requested work.
// Only a digest of the request is stored; instructions can contain private data.
type ResponseReceipt struct {
	RunID          string
	IdempotencyKey string
	Step           types.StepName
	Round          int
	RequestHash    string
}

func (d *DB) GetResponseReceipt(runID, key string) (*ResponseReceipt, error) {
	receipt := &ResponseReceipt{RunID: runID, IdempotencyKey: key}
	err := d.sql.QueryRow(`SELECT step_name, round, request_hash FROM response_receipts WHERE run_id = ? AND idempotency_key = ?`, runID, key).Scan(&receipt.Step, &receipt.Round, &receipt.RequestHash)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get response receipt: %w", err)
	}
	return receipt, nil
}

func (d *DB) InsertResponseReceipt(receipt ResponseReceipt) error {
	_, err := d.sql.Exec(`INSERT INTO response_receipts (run_id, idempotency_key, step_name, round, request_hash, accepted_at) VALUES (?, ?, ?, ?, ?, ?)`, receipt.RunID, receipt.IdempotencyKey, receipt.Step, receipt.Round, receipt.RequestHash, now())
	if err != nil {
		return fmt.Errorf("persist response acceptance: %w", err)
	}
	return nil
}

// LatestStepRound reads only the round number needed by an acceptance receipt.
func (d *DB) LatestStepRound(stepID string) (int, error) {
	var round int
	err := d.sql.QueryRow(`SELECT COALESCE(MAX(round), 0) FROM step_rounds WHERE step_result_id = ?`, stepID).Scan(&round)
	if err != nil {
		return 0, fmt.Errorf("read response round: %w", err)
	}
	return round, nil
}
