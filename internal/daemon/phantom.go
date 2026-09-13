package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/paths"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// RepairPhantoms never evicts an executor, including one parked without a PID.
// The manager mutex fences publication/removal of every executor and cancel
// handle through the transaction. A running row with no manager owner can then
// be repaired only if process inspection also finds no worker in its worktree.
func (m *RunManager) RepairPhantoms(ctx context.Context, apply bool, backup string) (*db.PhantomRepairReceipt, error) {
	ownershipHeld := false
	releaseOwnership := func() {
		if ownershipHeld {
			m.mu.Unlock()
			ownershipHeld = false
		}
	}
	defer releaseOwnership()
	out, err := repairPhantoms(ctx, m.db, m.paths, apply, backup, func(rows []db.PhantomRun) (map[string]string, error) {
		owners, err := phantomWorkerOwners(m.paths, rows)
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		ownershipHeld = true
		for _, r := range rows {
			if m.executors[r.ID] != nil || m.cancels[r.ID] != nil || m.dones[r.ID] != nil {
				owners[r.ID] = "live executor/ownership handle"
			}
		}
		return owners, nil
	}, releaseOwnership)
	if err != nil {
		return out, err
	}
	if apply {
		for _, r := range out.Selected {
			status := string(types.RunFailed)
			m.broadcast(ipc.Event{Type: ipc.EventRunCompleted, RunID: r.ID, RepoID: r.RepoID, Status: &status})
		}
	}
	return out, nil
}

// RepairPhantomsOffline is for a stopped daemon only. It takes the same OS lock
// as daemon startup and never starts or stops a worker. A live older daemon
// must be upgraded by its owner; it cannot be bypassed with offline SQL.
func RepairPhantomsOffline(ctx context.Context, p *paths.Paths, apply bool, backup string) (*db.PhantomRepairReceipt, error) {
	lock, err := acquireSingletonLock(p)
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	d, err := db.OpenRepair(p.DB(), apply)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return repairPhantoms(ctx, d, p, apply, backup, func(rows []db.PhantomRun) (map[string]string, error) { return phantomWorkerOwners(p, rows) }, nil)
}
func repairPhantoms(ctx context.Context, d *db.DB, p *paths.Paths, apply bool, backup string, owners func([]db.PhantomRun) (map[string]string, error), afterRepair func()) (*db.PhantomRepairReceipt, error) {
	dir := filepath.Join(p.Root(), "repairs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	if apply && backup == "" {
		backup = filepath.Join(dir, "phantoms-"+stamp+".sqlite")
	}
	if backup != "" && !filepath.IsAbs(backup) {
		return nil, fmt.Errorf("backup path must be absolute")
	}
	out, err := d.RepairPhantoms(ctx, db.PhantomRepairOptions{Apply: apply, BackupPath: backup}, owners)
	if afterRepair != nil {
		afterRepair()
	}
	if err != nil {
		return out, err
	}
	out.Host, _ = os.Hostname()
	out.Store = p.DB()
	info, err := os.Stat(p.DB())
	if err != nil {
		return out, err
	}
	out.StoreIdentity = phantomStoreIdentity(info)
	out.ReceiptPath = filepath.Join(dir, "phantoms-"+stamp+".json")
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return out, err
	}
	f, err := os.OpenFile(out.ReceiptPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return out, err
	}
	_, writeErr := f.Write(append(data, '\n'))
	syncErr := f.Sync()
	closeErr := f.Close()
	for _, err := range []error{writeErr, syncErr, closeErr} {
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
