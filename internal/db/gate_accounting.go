package db

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// CancelAdjudication is signed by an independent coordinator. The payload bytes
// (not a reserialized interpretation) are verified against the store's public key.
// It authorizes one dispatch, never resets historical aborts.
type CancelAdjudication struct {
	RepoID        string   `json:"repo_id"`
	Branch        string   `json:"branch"`
	HeadSHA       string   `json:"head_sha"`
	AbortedRunIDs []string `json:"aborted_run_ids"`
	Reason        string   `json:"reason"`
	Issuer        string   `json:"issuer"`
}

type SignedCancelAdjudication struct {
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}

func (d *DB) RegisterCancelAdjudication(raw []byte, reason string, key ed25519.PublicKey) (string, error) {
	var envelope SignedCancelAdjudication
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", err
	}
	if len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, envelope.Payload, envelope.Signature) {
		return "", fmt.Errorf("cancel budget: independent adjudicator signature required")
	}
	var record CancelAdjudication
	if err := json.Unmarshal(envelope.Payload, &record); err != nil {
		return "", err
	}
	if strings.TrimSpace(reason) == "" || reason != record.Reason || strings.TrimSpace(record.Issuer) == "" || record.RepoID == "" || record.Branch == "" || record.HeadSHA == "" || len(record.AbortedRunIDs) < 2 {
		return "", fmt.Errorf("cancel budget: reason and exact lane/head/abort evidence required")
	}
	ids, err := json.Marshal(record.AbortedRunIDs)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(envelope.Payload)
	id := hex.EncodeToString(digest[:])
	_, err = d.sql.Exec(`INSERT INTO cancel_adjudications(id,repo_id,branch,head_sha,aborted_run_ids_json,reason,issuer,signed_record) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, id, record.RepoID, record.Branch, record.HeadSHA, string(ids), reason, record.Issuer, string(raw))
	return id, err
}

// installGateAccounting runs only after additive column migrations. Triggers
// make the admission check and one-use grant consumption part of the run INSERT
// transaction, including other connections, retries and daemon restarts.
func (d *DB) installGateAccounting() error {
	for _, stmt := range gateAccountingSchema {
		if _, err := d.sql.Exec(stmt); err != nil {
			return fmt.Errorf("gate accounting migration: %w", err)
		}
	}
	return nil
}

const abortIDsSQL = `(SELECT json_group_array(run_id) FROM (SELECT run_id FROM gate_aborts WHERE repo_id=NEW.repo_id AND branch=NEW.branch ORDER BY run_id))`
const matchingGrantSQL = `SELECT id FROM cancel_adjudications WHERE repo_id=NEW.repo_id AND branch=NEW.branch AND head_sha=NEW.head_sha AND used_run_id IS NULL AND aborted_run_ids_json=` + abortIDsSQL + ` ORDER BY id LIMIT 1`
const wasteTurnsSQL = `(SELECT json_group_array(json_object(
 'turn_id',id,'purpose',purpose,'step',step_name,'round',round,'provider',model_provider,'model',model,
 'input_tokens',delta_input_tokens,'output_tokens',delta_output_tokens,'cache_read_tokens',delta_cache_read_tokens,
 'cache_creation_tokens',CASE WHEN session_mode='resumed' THEN NULL ELSE cache_creation_tokens END,
 'reported_cache_creation_tokens',cache_creation_tokens,'estimated_cost_usd',estimated_cost_usd,
 'known_cost_usd',known_cost_usd,'cost_basis',cost_basis,'price_source',json(price_source_json),
 'exit_status',exit_status,'started_at',started_at,'completed_at',completed_at
 )) FROM (SELECT * FROM agent_invocations WHERE run_id=NEW.id ORDER BY started_at,id))`
const wastePayloadSQL = `json_object('schema',1,'run_id',NEW.id,'lane_id',NEW.repo_id||':'||NEW.branch,'repo_id',NEW.repo_id,'branch',NEW.branch,'status',NEW.status,'terminal_at_ms',NEW.terminal_at_ms,'turns',json(` + wasteTurnsSQL + `))`

var gateAccountingSchema = []string{
	`CREATE TABLE IF NOT EXISTS gate_aborts(run_id TEXT PRIMARY KEY,repo_id TEXT NOT NULL,branch TEXT NOT NULL,reason TEXT,aborted_at INTEGER NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS gate_aborts_lane ON gate_aborts(repo_id,branch)`,
	`CREATE TABLE IF NOT EXISTS cancel_adjudications(id TEXT PRIMARY KEY,repo_id TEXT NOT NULL,branch TEXT NOT NULL,head_sha TEXT NOT NULL,aborted_run_ids_json TEXT NOT NULL,reason TEXT NOT NULL,issuer TEXT NOT NULL,signed_record TEXT NOT NULL,used_run_id TEXT UNIQUE)`,
	`INSERT OR IGNORE INTO gate_aborts SELECT id,repo_id,branch,COALESCE(cancel_reason,error),updated_at FROM runs WHERE status='cancelled'`,
	`CREATE TRIGGER IF NOT EXISTS gate_abort_record AFTER UPDATE OF status ON runs WHEN NEW.status='cancelled' AND NOT EXISTS (SELECT 1 FROM gate_aborts WHERE run_id=NEW.id) BEGIN INSERT INTO gate_aborts VALUES(NEW.id,NEW.repo_id,NEW.branch,COALESCE(NEW.cancel_reason,NEW.error),NEW.updated_at); END`,
	`CREATE TRIGGER IF NOT EXISTS gate_cancel_budget BEFORE INSERT ON runs WHEN (SELECT count(*) FROM gate_aborts WHERE repo_id=NEW.repo_id AND branch=NEW.branch)>=2 AND NOT EXISTS (` + matchingGrantSQL + `) BEGIN SELECT RAISE(ABORT,'cancel budget exhausted: signed adjudication and dispatch reason required'); END`,
	`CREATE TRIGGER IF NOT EXISTS gate_cancel_grant AFTER INSERT ON runs WHEN (SELECT count(*) FROM gate_aborts WHERE repo_id=NEW.repo_id AND branch=NEW.branch)>=2 BEGIN
 UPDATE runs SET cancel_adjudication_id=(` + matchingGrantSQL + `),dispatch_reason=(SELECT reason FROM cancel_adjudications WHERE id=(` + matchingGrantSQL + `)) WHERE id=NEW.id;
 UPDATE cancel_adjudications SET used_run_id=NEW.id WHERE id=(SELECT cancel_adjudication_id FROM runs WHERE id=NEW.id); END`,
	`CREATE TRIGGER IF NOT EXISTS gate_waste_terminal AFTER UPDATE OF status,terminal_at_ms ON runs WHEN NEW.status IN ('failed','cancelled') BEGIN UPDATE runs SET waste_usage_json=` + wastePayloadSQL + ` WHERE id=NEW.id; END`,
	// Late completed attempts after cancellation update the same run row, never
	// append a second run or sum the old aggregate. The hourly reader dedupes ID.
	`CREATE TRIGGER IF NOT EXISTS gate_waste_late_attempt AFTER INSERT ON agent_invocations BEGIN UPDATE runs SET status=status WHERE id=NEW.run_id AND status IN ('failed','cancelled'); END`,
	`CREATE TRIGGER IF NOT EXISTS gate_waste_completed_attempt AFTER UPDATE ON agent_invocations BEGIN UPDATE runs SET status=status WHERE id=NEW.run_id AND status IN ('failed','cancelled'); END`,
	// Legacy terminal runs are rebuilt from measured attempts, preserving NULLs.
	`UPDATE runs SET status=status WHERE status IN ('failed','cancelled') AND waste_usage_json IS NULL`,
}

// RecordCancelReason is called before cancellation reaches the executor.
func (d *DB) RecordCancelReason(runID, reason string) error {
	result, err := d.sql.Exec(`UPDATE runs SET cancel_reason=COALESCE(cancel_reason,?) WHERE id=?`, reason, runID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("unknown exact run %s", runID)
	}
	return nil
}
