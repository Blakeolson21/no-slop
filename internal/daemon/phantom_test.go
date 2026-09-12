//go:build linux

package daemon

import (
	"context"
	"database/sql"
	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/types"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPhantomRepairProtectsLiveExecutorAndWorker(t *testing.T) {
	m, id := stepDiffFixture(t, "changed\n")
	run, err := m.db.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	other, err := m.db.InsertRun(run.RepoID, "orphan", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-7 * time.Hour).Unix()
	raw, err := sql.Open("sqlite", m.paths.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, r := range []*db.Run{run, other} {
		if _, err := raw.Exec("UPDATE runs SET status='running',created_at=?,updated_at=? WHERE id=?", old, old, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	m.cancels[id] = cancel // Parked manager ownership has no PID.
	work := m.paths.WorktreeDir(other.RepoID, other.ID)
	if err = os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "sleep", "60")
	cmd.Dir = work
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	out, err := m.RepairPhantoms(ctx, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Selected) != 0 || len(out.Protected) != 2 {
		t.Fatalf("owners=%+v", out)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	out, err = m.RepairPhantoms(ctx, true, filepath.Join(t.TempDir(), "backup.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Affected != 1 || out.Selected[0].ID != other.ID {
		t.Fatalf("repair=%+v", out)
	}
	got, err := m.db.GetRun(id)
	if err != nil || got.Status != types.RunRunning {
		t.Fatalf("live changed: %+v %v", got, err)
	}
	if _, err = os.Stat(out.ReceiptPath); err != nil {
		t.Fatal(err)
	}
}
func TestPhantomRepairOfflineRefusesLiveSingleton(t *testing.T) {
	m, _ := stepDiffFixture(t, "x")
	lock, err := acquireSingletonLock(m.paths)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, err := RepairPhantomsOffline(context.Background(), m.paths, true, filepath.Join(t.TempDir(), "backup.sqlite")); err == nil {
		t.Fatal("offline repair bypassed live daemon")
	}
}
