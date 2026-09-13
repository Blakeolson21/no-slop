package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"time"
)

const PhantomReason = "phantom: no agent"
const PhantomIdleSeconds int64 = 6 * 60 * 60

type PhantomRun struct {
	CreatedAt    int64  `json:"created_at"`
	ID           string `json:"id"`
	RepoID       string `json:"repo_id"`
	Branch       string `json:"branch"`
	Head         string `json:"head"`
	LastActivity int64  `json:"last_activity"`
	AgentPIDs    int    `json:"agent_pids"`
	Protection   string `json:"protection,omitempty"`
}
type PhantomRepairOptions struct {
	Apply      bool   `json:"apply"`
	BackupPath string `json:"backup_path,omitempty"`
	Now        int64  `json:"-"` // supplied by the owning process, never an IPC caller
}
type PhantomRepairReceipt struct {
	StoreIdentity string       `json:"store_identity"`
	ReceiptPath   string       `json:"receipt_path"`
	Host          string       `json:"host"`
	Time          int64        `json:"time"`
	Store         string       `json:"store"`
	Cutoff        int64        `json:"cutoff"`
	Predicate     string       `json:"predicate"`
	Apply         bool         `json:"apply"`
	Before        int          `json:"before_running"`
	After         int          `json:"after_running"`
	Selected      []PhantomRun `json:"selected"`
	Protected     []PhantomRun `json:"protected"`
	Affected      int          `json:"affected"`
	BackupPath    string       `json:"backup_path,omitempty"`
	BackupSHA256  string       `json:"backup_sha256,omitempty"`
}

// RepairPhantoms requires the caller to fence executor ownership for the entire
// operation. It re-reads rows and calls the live ownership checker while holding
// SQLite's write reservation; an old dry-run selection grants no authority.
// The fixed idle threshold is strictly greater than six hours, measured using
// every durable run/step/round/invocation activity source, not NULL PID alone.
func (d *DB) RepairPhantoms(ctx context.Context, opts PhantomRepairOptions, owners func([]PhantomRun) (map[string]string, error)) (*PhantomRepairReceipt, error) {
	if owners == nil {
		return nil, fmt.Errorf("phantom repair requires a live ownership check")
	}
	if opts.Now == 0 {
		opts.Now = time.Now().Unix()
	}
	out := &PhantomRepairReceipt{Time: opts.Now, Cutoff: opts.Now - PhantomIdleSeconds, Apply: opts.Apply, Selected: []PhantomRun{}, Protected: []PhantomRun{}, Predicate: "status=running AND every step agent_pid IS NULL AND latest durable activity < cutoff AND no live executor, worker or lease"}
	if opts.Apply {
		if opts.BackupPath == "" {
			return nil, fmt.Errorf("apply requires a new backup path")
		}
		// SQLite owns the snapshot; copying a live DB file loses WAL transactions.
		// VACUUM INTO accepts an existing empty file. Reserve it exclusively
		// with private permissions and only clean up a reservation we own.
		f, err := os.OpenFile(opts.BackupPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, fmt.Errorf("reserve backup: %w", err)
		}
		backupReady := false
		defer func() {
			if !backupReady {
				_ = os.Remove(opts.BackupPath)
			}
		}()
		if err = f.Close(); err != nil {
			return nil, err
		}
		if _, err = d.sql.ExecContext(ctx, "VACUUM INTO ?", opts.BackupPath); err != nil {
			return nil, fmt.Errorf("snapshot store: %w", err)
		}
		f, err = os.Open(opts.BackupPath)
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		closeErr := f.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		out.BackupPath = opts.BackupPath
		out.BackupSHA256 = hex.EncodeToString(h.Sum(nil))
		backupReady = true
	}
	terminalMS := d.hasColumn("runs", "terminal_at_ms")
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if opts.Apply {
		if _, err = tx.ExecContext(ctx, "UPDATE runs SET status=status WHERE 0"); err != nil {
			return nil, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.id,r.repo_id,r.branch,r.head_sha,r.created_at,
 MAX(r.created_at,r.updated_at,COALESCE(r.awaiting_agent_since,0),
 COALESCE((SELECT MAX(MAX(COALESCE(s.last_activity_at,0),COALESCE(s.started_at,0),COALESCE(s.completed_at,0))) FROM step_results s WHERE s.run_id=r.id),0),
 COALESCE((SELECT MAX(q.created_at) FROM step_rounds q JOIN step_results s ON s.id=q.step_result_id WHERE s.run_id=r.id),0),
 COALESCE((SELECT MAX(MAX(COALESCE(a.started_at,0),COALESCE(a.completed_at,0))) FROM agent_invocations a WHERE a.run_id=r.id),0)),
 (SELECT COUNT(*) FROM step_results s WHERE s.run_id=r.id AND s.agent_pid IS NOT NULL)
 FROM runs r WHERE r.status='running' ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	var candidates []PhantomRun
	for rows.Next() {
		var r PhantomRun
		if err = rows.Scan(&r.ID, &r.RepoID, &r.Branch, &r.Head, &r.CreatedAt, &r.LastActivity, &r.AgentPIDs); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out.Before = len(candidates)
	out.After = out.Before
	protected, err := owners(candidates)
	if err != nil {
		return nil, fmt.Errorf("recheck live ownership: %w", err)
	}
	for _, r := range candidates {
		switch {
		case r.LastActivity >= out.Cutoff:
			r.Protection = "recent activity (idle <= 6h)"
		case r.AgentPIDs > 0:
			r.Protection = "recorded agent PID"
		case protected[r.ID] != "":
			r.Protection = protected[r.ID]
		}
		if r.Protection != "" {
			out.Protected = append(out.Protected, r)
			continue
		}
		out.Selected = append(out.Selected, r)
		if !opts.Apply {
			continue
		}
		result, err := tx.ExecContext(ctx, `UPDATE runs SET status='failed',error=?,awaiting_agent_since=NULL,push_active=0,ci_ready_at=NULL,ci_ready_no_ci=0,review_approved_head_sha=NULL,terminal_head_verified_at=NULL,updated_at=? WHERE id=? AND status='running'`, PhantomReason, opts.Now, r.ID)
		if err != nil {
			return nil, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, fmt.Errorf("run %s changed during repair", r.ID)
		}
		if terminalMS {
			if _, err := tx.ExecContext(ctx, "UPDATE runs SET terminal_at_ms=? WHERE id=?", opts.Now*1000, r.ID); err != nil {
				return nil, err
			}
		}
		if err := failPhantomSteps(ctx, tx, r.ID, opts.Now); err != nil {
			return nil, err
		}
		out.Affected++
	}
	if opts.Apply {
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		out.After -= out.Affected
	}
	return out, nil
}
func failPhantomSteps(ctx context.Context, tx *sql.Tx, runID string, now int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE step_results SET status='failed',error=?,agent_pid=NULL,completed_at=?,last_activity_at=?,last_activity=? WHERE run_id=? AND status IN ('running','fixing','awaiting_approval','fix_review')`, PhantomReason, now, now, PhantomReason, runID)
	return err
}

// OpenRepair never creates or migrates a production store. The repair SQL uses
// the shared legacy columns; newer terminal timestamp columns are optional.
func OpenRepair(path string, apply bool) (*DB, error) {
	if !apply {
		return OpenReadOnly(path)
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	s, err := sql.Open("sqlite", "file:"+path+"?mode=rw&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	s.SetMaxOpenConns(1)
	if err = s.Ping(); err != nil {
		s.Close()
		return nil, err
	}
	return &DB{sql: s}, nil
}
