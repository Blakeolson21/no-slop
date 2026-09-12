package db

import (
	"database/sql"
	"fmt"

	"github.com/Blakeolson21/no-slop/internal/types"
)

// Admission is the outcome of one atomic gate-admission decision.
//
// Superseded lists the runs this dispatch is entitled to cancel. They were
// identified inside the same transaction that admitted Run, so the caller may
// cancel exactly these and nothing else. A refused admission returns an error
// and no Admission at all, which is what keeps a refused dispatch from
// destroying an authorized run.
type Admission struct {
	Run        *Run
	Superseded []string
}

// AdmitRun makes the whole gate-admission decision in a single transaction:
// it reserves the cancel budget, consumes at most one signed adjudication
// grant, inserts the run, and identifies the runs eligible for cancellation.
//
// Ordering is the point. The budget is enforced by the runs INSERT triggers, so
// the INSERT is the admission decision and it happens first; only a run that
// was actually admitted ever learns which active runs it supersedes, and the
// caller cannot cancel anything before this function returns. Previously the
// caller cancelled the active run first and discovered the refusal afterwards,
// which let an over-budget dispatch destroy an adjudicated run it was never
// allowed to replace.
//
// Every failure rolls the transaction back, so a refused dispatch leaves no run
// row behind and leaves the one-use grant unconsumed for the dispatch that is
// actually entitled to it.
func (d *DB) AdmitRun(repoID, branch, headSHA, baseSHA string, intent *RunIntent) (*Admission, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, fmt.Errorf("admit run: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	run := newRunRecord(repoID, branch, headSHA, baseSHA, intent)
	if _, err := tx.Exec(insertRunSQL, insertRunArgs(run)...); err != nil {
		// The budget trigger raises here. Returning before any cancellation is
		// the fix: the caller still has an untouched active run.
		return nil, fmt.Errorf("insert run: %w", err)
	}

	// Fail closed on the grant the triggers were supposed to consume. When the
	// lane is over budget an admitted run must carry the exact adjudication that
	// authorized it; a run that reached the table without one is rolled back
	// rather than left to reach a provider.
	var overBudget int
	if err := tx.QueryRow(`SELECT count(*) FROM gate_aborts WHERE repo_id = ? AND branch = ?`, repoID, branch).Scan(&overBudget); err != nil {
		return nil, fmt.Errorf("admit run: read abort history: %w", err)
	}
	if overBudget >= gateCancelBudget {
		var grant sql.NullString
		if err := tx.QueryRow(`SELECT cancel_adjudication_id FROM runs WHERE id = ?`, run.ID).Scan(&grant); err != nil {
			return nil, fmt.Errorf("admit run: read adjudication: %w", err)
		}
		if !grant.Valid || grant.String == "" {
			return nil, fmt.Errorf("cancel budget exhausted: admitted run carries no consumed adjudication")
		}
	}

	// The supersede set is read after the INSERT, under the write lock this
	// transaction already holds, so it is a consistent view of the lane at the
	// moment this run was admitted rather than a stale pre-decision snapshot.
	rows, err := tx.Query(
		`SELECT id FROM runs WHERE repo_id = ? AND branch = ? AND status IN (?, ?) AND id <> ? ORDER BY created_at, id`,
		repoID, branch, types.RunPending, types.RunRunning, run.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("admit run: identify superseded runs: %w", err)
	}
	var superseded []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("admit run: scan superseded run: %w", err)
		}
		superseded = append(superseded, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("admit run: identify superseded runs: %w", err)
	}
	rows.Close()

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("admit run: %w", err)
	}
	committed = true
	return &Admission{Run: run, Superseded: superseded}, nil
}

// gateCancelBudget is the number of recorded aborts on a lane that makes the
// next dispatch require a signed adjudication. It must stay equal to the
// threshold compiled into the gate_cancel_budget trigger.
const gateCancelBudget = 2
