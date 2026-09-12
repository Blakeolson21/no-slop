package db

import (
	"crypto/ed25519"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/types"
)

// exhaustBudget cancels two runs on the lane, leaving the next dispatch over
// budget, and returns the aborted run IDs.
func exhaustBudget(t *testing.T, d *DB, repoID, branch string) []string {
	t.Helper()
	var ids []string
	for i := 0; i < 2; i++ {
		r, err := d.InsertRun(repoID, branch, "head", "base")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
		if err = d.UpdateRunStatus(r.ID, types.RunCancelled); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

func signedGrant(t *testing.T, d *DB, repoID, branch, head string, ids []string) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	record := CancelAdjudication{RepoID: repoID, Branch: branch, HeadSHA: head, AbortedRunIDs: ids, Reason: "coordinator authorized one retry", Issuer: "coordinator"}
	payload, _ := json.Marshal(record)
	raw, _ := json.Marshal(SignedCancelAdjudication{Payload: payload, Signature: ed25519.Sign(priv, payload)})
	id, err := d.RegisterCancelAdjudication(raw, record.Reason, pub)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// A newer-head dispatch that the budget refuses must learn that before it is
// told anything it could cancel. This is the ordering the INSERT-time trigger
// could not give: the caller used to cancel the adjudicated run first and
// discover the refusal afterwards.
func TestAdmitRunRefusesBeforeNamingAnythingToCancel(t *testing.T) {
	d := openTestDB(t)
	repo, _ := d.InsertRepo("/test", "https://example.com/test", "main")
	ids := exhaustBudget(t, d, repo.ID, "lane")
	signedGrant(t, d, repo.ID, "lane", "authorized-head", ids)

	authorized, err := d.AdmitRun(repo.ID, "lane", "authorized-head", "base", nil)
	if err != nil {
		t.Fatalf("adjudicated dispatch refused: %v", err)
	}
	if len(authorized.Superseded) != 0 {
		t.Fatalf("nothing was active, yet %d runs were named for cancellation", len(authorized.Superseded))
	}

	refused, err := d.AdmitRun(repo.ID, "lane", "newer-head", "base", nil)
	if err == nil || !strings.Contains(err.Error(), "cancel budget") {
		t.Fatalf("over-budget newer head admitted: %v", err)
	}
	if refused != nil {
		t.Fatalf("a refused dispatch was handed %d cancellable runs", len(refused.Superseded))
	}
	status, err := d.GetRun(authorized.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status == nil || status.Status != types.RunPending {
		t.Fatalf("the adjudicated run did not survive the refused dispatch: %+v", status)
	}
	var rows int
	if err = d.sql.QueryRow(`SELECT count(*) FROM runs WHERE branch='lane' AND head_sha='newer-head'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("refused dispatch left %d run rows behind", rows)
	}
}

// An admitted dispatch names exactly the active runs of its own lane, and only
// those, so the caller cancels nothing the admission transaction did not cover.
func TestAdmitRunNamesOnlyItsOwnLanesActiveRuns(t *testing.T) {
	d := openTestDB(t)
	repo, _ := d.InsertRepo("/test", "https://example.com/test", "main")
	active, err := d.InsertRun(repo.ID, "lane", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	running, err := d.InsertRun(repo.ID, "lane", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	if err = d.UpdateRunStatus(running.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}
	done, err := d.InsertRun(repo.ID, "lane", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	if err = d.UpdateRunStatus(done.ID, types.RunCompleted); err != nil {
		t.Fatal(err)
	}
	other, err := d.InsertRun(repo.ID, "other-lane", "head", "base")
	if err != nil {
		t.Fatal(err)
	}

	admission, err := d.AdmitRun(repo.ID, "lane", "newer-head", "base", nil)
	if err != nil {
		t.Fatal(err)
	}
	named := map[string]bool{}
	for _, id := range admission.Superseded {
		named[id] = true
	}
	if !named[active.ID] || !named[running.ID] {
		t.Fatalf("active runs missing from the supersede set: %v", admission.Superseded)
	}
	if named[done.ID] || named[other.ID] || named[admission.Run.ID] {
		t.Fatalf("supersede set reached beyond this lane's active runs: %v", admission.Superseded)
	}
}

// Concurrent dispatches contend for one grant. Exactly one may be admitted, and
// every refused one must leave the lane's active run alone - it never learns
// which runs it would have cancelled.
func TestAdmitRunConcurrentDispatchesAdmitExactlyOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo, _ := d.InsertRepo("/test", "https://example.com/test", "main")
	ids := exhaustBudget(t, d, repo.ID, "lane")
	signedGrant(t, d, repo.ID, "lane", "next", ids)

	const count = 6
	conns := make([]*DB, count)
	for i := range conns {
		conns[i], err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer conns[i].Close()
	}
	start := make(chan struct{})
	results := make(chan *Admission, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for _, conn := range conns {
		wg.Add(1)
		go func(c *DB) {
			defer wg.Done()
			<-start
			admission, e := c.AdmitRun(repo.ID, "lane", "next", "base", nil)
			if e != nil {
				errs <- e
				return
			}
			results <- admission
		}(conn)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	admitted := 0
	for admission := range results {
		admitted++
		if len(admission.Superseded) != 0 {
			t.Fatalf("supersede set named runs from a lane with none active: %v", admission.Superseded)
		}
	}
	for e := range errs {
		if !strings.Contains(e.Error(), "cancel budget") {
			t.Fatal(e)
		}
	}
	if admitted != 1 {
		t.Fatalf("admitted %d concurrent dispatches against one grant", admitted)
	}
}

// The whole decision is one transaction: if the grant the triggers were meant
// to consume is not there, nothing survives - no run row, and the grant stays
// available to the dispatch that is actually entitled to it. Dropping the grant
// trigger reproduces exactly that inconsistency.
func TestAdmitRunRollsBackWhenTheGrantIsNotConsumed(t *testing.T) {
	d := openTestDB(t)
	repo, _ := d.InsertRepo("/test", "https://example.com/test", "main")
	ids := exhaustBudget(t, d, repo.ID, "lane")
	grant := signedGrant(t, d, repo.ID, "lane", "next", ids)
	if _, err := d.sql.Exec(`DROP TRIGGER gate_cancel_grant`); err != nil {
		t.Fatal(err)
	}

	admission, err := d.AdmitRun(repo.ID, "lane", "next", "base", nil)
	if err == nil {
		t.Fatalf("run admitted without a consumed adjudication: %s", admission.Run.ID)
	}
	if !strings.Contains(err.Error(), "cancel budget") {
		t.Fatalf("unexpected refusal: %v", err)
	}
	var rows int
	if err = d.sql.QueryRow(`SELECT count(*) FROM runs WHERE branch='lane' AND head_sha='next'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("rolled-back admission left %d run rows", rows)
	}
	var used any
	if err = d.sql.QueryRow(`SELECT used_run_id FROM cancel_adjudications WHERE id=?`, grant).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != nil {
		t.Fatalf("rolled-back admission consumed the grant: %v", used)
	}
}
