package db

import (
	"database/sql"
	"fmt"
	"strings"
)

type ReviewRecoveryState string

const (
	ReviewRecoverySelectionRecoveredNoDelta   ReviewRecoveryState = "selection_recovered_no_delta"
	ReviewRecoverySelectionRecoveredWithDelta ReviewRecoveryState = "selection_recovered_with_delta"
	ReviewRecoverySelectionApplied            ReviewRecoveryState = "selection_applied"
)

func (s ReviewRecoveryState) Valid() bool {
	switch s {
	case ReviewRecoverySelectionRecoveredNoDelta, ReviewRecoverySelectionRecoveredWithDelta, ReviewRecoverySelectionApplied:
		return true
	default:
		return false
	}
}

// UncertifiedPipelineRange is the per-branch recovery boundary for review
// truth whose verification did not complete. RecoveryState records whether a
// selection was recovered before a fixer delta, recovered with that delta, or
// already applied by an ordinary promotion. The database boundary is
// authoritative. SelectionApplied is a compatibility view for older callers.
type UncertifiedPipelineRange struct {
	RepoID             string
	Branch             string
	FromSHA            string
	ToSHA              string
	SourceRunID        string
	RecoveryState      ReviewRecoveryState
	SelectionApplied   bool
	FindingsJSON       *string
	SelectedFindingIDs *string
	CreatedAt          int64
}

// UpsertUncertifiedPipelineRange records or replaces the uncertified recovery
// boundary for one repo+branch. A newer uncertified HEAD replaces an older one.
func (d *DB) UpsertUncertifiedPipelineRange(repoID, branch, fromSHA, toSHA, sourceRunID string) error {
	return d.UpsertUncertifiedPipelineRangeRecovery(repoID, branch, fromSHA, toSHA, sourceRunID, ReviewRecoverySelectionApplied)
}

func (d *DB) UpsertUncertifiedPipelineRangeState(repoID, branch, fromSHA, toSHA, sourceRunID string, selectionApplied bool) error {
	state := ReviewRecoverySelectionRecoveredNoDelta
	if selectionApplied {
		state = ReviewRecoverySelectionApplied
	}
	return d.UpsertUncertifiedPipelineRangeRecovery(repoID, branch, fromSHA, toSHA, sourceRunID, state)
}

func (d *DB) UpsertUncertifiedPipelineRangeRecovery(repoID, branch, fromSHA, toSHA, sourceRunID string, recoveryState ReviewRecoveryState) error {
	repoID = strings.TrimSpace(repoID)
	branch = strings.TrimSpace(branch)
	fromSHA = strings.TrimSpace(fromSHA)
	toSHA = strings.TrimSpace(toSHA)
	sourceRunID = strings.TrimSpace(sourceRunID)
	if repoID == "" || branch == "" || fromSHA == "" || toSHA == "" || sourceRunID == "" {
		return fmt.Errorf("uncertified pipeline range requires repo, branch, from_sha, to_sha, and source run")
	}
	if !recoveryState.Valid() {
		return fmt.Errorf("uncertified pipeline range has invalid recovery state %q", recoveryState)
	}
	selectionApplied := recoveryState != ReviewRecoverySelectionRecoveredNoDelta
	findingsJSON, selectedFindingIDs, err := d.reviewRecoverySnapshot(sourceRunID)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(
		`INSERT INTO uncertified_pipeline_ranges (repo_id, branch, from_sha, to_sha, source_run_id, selection_applied, recovery_state, findings_json, selected_finding_ids, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(repo_id, branch) DO UPDATE SET
		   from_sha = excluded.from_sha,
		   to_sha = excluded.to_sha,
		   source_run_id = excluded.source_run_id,
		   selection_applied = excluded.selection_applied,
		   recovery_state = excluded.recovery_state,
		   findings_json = COALESCE(excluded.findings_json, uncertified_pipeline_ranges.findings_json),
		   selected_finding_ids = COALESCE(excluded.selected_finding_ids, uncertified_pipeline_ranges.selected_finding_ids),
		   created_at = excluded.created_at`,
		repoID, branch, fromSHA, toSHA, sourceRunID, selectionApplied, recoveryState, findingsJSON, selectedFindingIDs, now(),
	)
	if err != nil {
		return fmt.Errorf("upsert uncertified pipeline range: %w", err)
	}
	return nil
}

func (d *DB) reviewRecoverySnapshot(sourceRunID string) (*string, *string, error) {
	var stepID string
	var findings sql.NullString
	err := d.sql.QueryRow(
		`SELECT id, findings_json FROM step_results WHERE run_id = ? AND step_name = ? LIMIT 1`,
		sourceRunID, "review",
	).Scan(&stepID, &findings)
	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read review recovery snapshot: %w", err)
	}
	var selected sql.NullString
	if err := d.sql.QueryRow(
		`SELECT selected_finding_ids FROM step_rounds WHERE step_result_id = ? ORDER BY round DESC LIMIT 1`,
		stepID,
	).Scan(&selected); err != nil && err != sql.ErrNoRows {
		return nil, nil, fmt.Errorf("read review recovery selection snapshot: %w", err)
	}
	var findingsPtr, selectedPtr *string
	if findings.Valid && findings.String != "" {
		findingsPtr = &findings.String
	}
	if selected.Valid && selected.String != "" {
		selectedPtr = &selected.String
	}
	return findingsPtr, selectedPtr, nil
}

// GetUncertifiedPipelineRange returns the uncertified range for a branch, or
// nil when none is recorded.
func (d *DB) GetUncertifiedPipelineRange(repoID, branch string) (*UncertifiedPipelineRange, error) {
	repoID = strings.TrimSpace(repoID)
	branch = strings.TrimSpace(branch)
	if repoID == "" || branch == "" {
		return nil, nil
	}
	row := d.sql.QueryRow(
		`SELECT repo_id, branch, from_sha, to_sha, source_run_id, recovery_state, selection_applied, findings_json, selected_finding_ids, created_at
		 FROM uncertified_pipeline_ranges WHERE repo_id = ? AND branch = ?`,
		repoID, branch,
	)
	var r UncertifiedPipelineRange
	if err := row.Scan(&r.RepoID, &r.Branch, &r.FromSHA, &r.ToSHA, &r.SourceRunID, &r.RecoveryState, &r.SelectionApplied, &r.FindingsJSON, &r.SelectedFindingIDs, &r.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get uncertified pipeline range: %w", err)
	}
	return &r, nil
}

// DeleteUncertifiedPipelineRange removes the uncertified marker for a branch.
// It is a no-op when no row exists.
func (d *DB) DeleteUncertifiedPipelineRange(repoID, branch string) error {
	repoID = strings.TrimSpace(repoID)
	branch = strings.TrimSpace(branch)
	if repoID == "" || branch == "" {
		return nil
	}
	if _, err := d.sql.Exec(
		`DELETE FROM uncertified_pipeline_ranges WHERE repo_id = ? AND branch = ?`,
		repoID, branch,
	); err != nil {
		return fmt.Errorf("delete uncertified pipeline range: %w", err)
	}
	return nil
}

func (d *DB) RestoreUncertifiedPipelineRangeIfCurrent(current UncertifiedPipelineRange, previous *UncertifiedPipelineRange) (bool, error) {
	if strings.TrimSpace(current.RepoID) == "" || strings.TrimSpace(current.Branch) == "" {
		return false, fmt.Errorf("restore uncertified pipeline range requires current repo and branch")
	}
	var (
		result sql.Result
		err    error
	)
	if previous == nil {
		result, err = d.sql.Exec(
			`DELETE FROM uncertified_pipeline_ranges
			 WHERE repo_id = ? AND branch = ? AND from_sha = ? AND to_sha = ? AND source_run_id = ? AND recovery_state = ?`,
			current.RepoID, current.Branch, current.FromSHA, current.ToSHA, current.SourceRunID, current.RecoveryState,
		)
	} else {
		result, err = d.sql.Exec(
			`UPDATE uncertified_pipeline_ranges
			 SET from_sha = ?, to_sha = ?, source_run_id = ?, selection_applied = ?, recovery_state = ?, findings_json = ?, selected_finding_ids = ?, created_at = ?
			 WHERE repo_id = ? AND branch = ? AND from_sha = ? AND to_sha = ? AND source_run_id = ? AND recovery_state = ?`,
			previous.FromSHA, previous.ToSHA, previous.SourceRunID, previous.SelectionApplied, previous.RecoveryState, previous.FindingsJSON, previous.SelectedFindingIDs, previous.CreatedAt,
			current.RepoID, current.Branch, current.FromSHA, current.ToSHA, current.SourceRunID, current.RecoveryState,
		)
	}
	if err != nil {
		return false, fmt.Errorf("restore uncertified pipeline range: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read restored uncertified pipeline range count: %w", err)
	}
	return changed == 1, nil
}
