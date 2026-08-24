package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// UncertifiedPipelineRange is the per-branch recovery boundary for review
// truth whose verification did not complete. SelectionApplied records whether
// the selected fix reached the branch. The database boundary is authoritative.
type UncertifiedPipelineRange struct {
	RepoID           string
	Branch           string
	FromSHA          string
	ToSHA            string
	SourceRunID      string
	SelectionApplied bool
	CreatedAt        int64
}

// UpsertUncertifiedPipelineRange records or replaces the uncertified recovery
// boundary for one repo+branch. A newer uncertified HEAD replaces an older one.
func (d *DB) UpsertUncertifiedPipelineRange(repoID, branch, fromSHA, toSHA, sourceRunID string) error {
	return d.UpsertUncertifiedPipelineRangeState(repoID, branch, fromSHA, toSHA, sourceRunID, true)
}

func (d *DB) UpsertUncertifiedPipelineRangeState(repoID, branch, fromSHA, toSHA, sourceRunID string, selectionApplied bool) error {
	repoID = strings.TrimSpace(repoID)
	branch = strings.TrimSpace(branch)
	fromSHA = strings.TrimSpace(fromSHA)
	toSHA = strings.TrimSpace(toSHA)
	sourceRunID = strings.TrimSpace(sourceRunID)
	if repoID == "" || branch == "" || fromSHA == "" || toSHA == "" || sourceRunID == "" {
		return fmt.Errorf("uncertified pipeline range requires repo, branch, from_sha, to_sha, and source run")
	}
	_, err := d.sql.Exec(
		`INSERT INTO uncertified_pipeline_ranges (repo_id, branch, from_sha, to_sha, source_run_id, selection_applied, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(repo_id, branch) DO UPDATE SET
		   from_sha = excluded.from_sha,
		   to_sha = excluded.to_sha,
		   source_run_id = excluded.source_run_id,
		   selection_applied = excluded.selection_applied,
		   created_at = excluded.created_at`,
		repoID, branch, fromSHA, toSHA, sourceRunID, selectionApplied, now(),
	)
	if err != nil {
		return fmt.Errorf("upsert uncertified pipeline range: %w", err)
	}
	return nil
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
		`SELECT repo_id, branch, from_sha, to_sha, source_run_id, selection_applied, created_at
		 FROM uncertified_pipeline_ranges WHERE repo_id = ? AND branch = ?`,
		repoID, branch,
	)
	var r UncertifiedPipelineRange
	if err := row.Scan(&r.RepoID, &r.Branch, &r.FromSHA, &r.ToSHA, &r.SourceRunID, &r.SelectionApplied, &r.CreatedAt); err != nil {
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
			 WHERE repo_id = ? AND branch = ? AND from_sha = ? AND to_sha = ? AND source_run_id = ? AND selection_applied = ?`,
			current.RepoID, current.Branch, current.FromSHA, current.ToSHA, current.SourceRunID, current.SelectionApplied,
		)
	} else {
		result, err = d.sql.Exec(
			`UPDATE uncertified_pipeline_ranges
			 SET from_sha = ?, to_sha = ?, source_run_id = ?, selection_applied = ?, created_at = ?
			 WHERE repo_id = ? AND branch = ? AND from_sha = ? AND to_sha = ? AND source_run_id = ? AND selection_applied = ?`,
			previous.FromSHA, previous.ToSHA, previous.SourceRunID, previous.SelectionApplied, previous.CreatedAt,
			current.RepoID, current.Branch, current.FromSHA, current.ToSHA, current.SourceRunID, current.SelectionApplied,
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
