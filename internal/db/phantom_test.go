package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestPhantomRepairRechecksAndIsIdempotent(t *testing.T) {
	d := openTestDB(t)
	repo, err := d.InsertRepo("/phantom-test", "https://example.com/repo", "main")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	var ids []string
	for _, br := range []string{"stale", "live", "recent", "boundary", "revived"} {
		r, err := d.InsertRun(repo.ID, br, "head", "base")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
		if err := d.UpdateRunStatus(r.ID, types.RunRunning); err != nil {
			t.Fatal(err)
		}
		age := int64(7 * 3600)
		if br == "recent" {
			age = 60
		}
		if br == "boundary" {
			age = 6 * 3600
		}
		if _, err := d.sql.Exec("UPDATE runs SET updated_at=?,created_at=? WHERE id=?", now-age, now-age, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	checks := 0
	owner := func(rows []PhantomRun) (map[string]string, error) {
		checks++
		return map[string]string{ids[1]: "live executor"}, nil
	}
	dry, err := d.RepairPhantoms(context.Background(), PhantomRepairOptions{Now: now}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(dry.Selected) != 2 || dry.Affected != 0 {
		t.Fatalf("dry=%+v", dry)
	}
	// A row selected in the dry-run becomes active before the separate apply.
	if _, err := d.sql.Exec("UPDATE runs SET updated_at=? WHERE id=?", now, ids[4]); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup.sqlite")
	applied, err := d.RepairPhantoms(context.Background(), PhantomRepairOptions{Now: now, Apply: true, BackupPath: backup}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Affected != 1 || len(applied.Selected) != 1 || applied.Selected[0].ID != ids[0] || applied.BackupSHA256 == "" {
		t.Fatalf("apply=%+v", applied)
	}
	r, err := d.GetRun(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != types.RunFailed || r.Error == nil || *r.Error != "phantom: no agent" {
		t.Fatalf("run=%+v", r)
	}
	saved, err := OpenReadOnly(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer saved.Close()
	before, err := saved.GetRun(ids[0])
	if err != nil || before.Status != types.RunRunning {
		t.Fatalf("backup=%+v %v", before, err)
	}
	again, err := d.RepairPhantoms(context.Background(), PhantomRepairOptions{Now: now, Apply: true, BackupPath: filepath.Join(t.TempDir(), "repeat.sqlite")}, owner)
	if err != nil || again.Affected != 0 {
		t.Fatalf("repeat=%+v %v", again, err)
	}
	if checks < 3 {
		t.Fatal("ownership not checked for each transaction")
	}
	active, err := d.GetActiveRuns()
	if err != nil || len(active) != 4 {
		t.Fatalf("active=%d %v", len(active), err)
	}
}

func TestPhantomRepairTransactionRollbackAndActivityProtection(t *testing.T) {
	d := openTestDB(t)
	repo, err := d.InsertRepo("/rollback", "https://example.com/r", "main")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	var ids []string
	for _, branch := range []string{"stale", "heartbeat", "recorded-agent"} {
		r, err := d.InsertRun(repo.ID, branch, "head", "base")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
		if _, err = d.sql.Exec("UPDATE runs SET status='running',updated_at=?,created_at=? WHERE id=?", now-7*3600, now-7*3600, r.ID); err != nil {
			t.Fatal(err)
		}
		sr, err := d.InsertStepResult(r.ID, types.StepReview)
		if err != nil {
			t.Fatal(err)
		}
		if branch == "heartbeat" {
			if err = d.TouchStepActivity(sr.ID, "worker heartbeat"); err != nil {
				t.Fatal(err)
			}
		}
		if branch == "recorded-agent" {
			if _, err = d.sql.Exec("UPDATE step_results SET agent_pid=123 WHERE id=?", sr.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	owners := func([]PhantomRun) (map[string]string, error) { return map[string]string{}, nil }
	dry, err := d.RepairPhantoms(context.Background(), PhantomRepairOptions{Now: now}, owners)
	if err != nil || len(dry.Selected) != 1 || dry.Selected[0].ID != ids[0] {
		t.Fatalf("dry=%+v %v", dry, err)
	}
	// Force failure after the run write, while completing its step. Nothing from
	// that transaction may leak out, and the complete pre-write snapshot survives.
	if _, err = d.sql.Exec(`CREATE TRIGGER fail_phantom BEFORE UPDATE ON step_results BEGIN SELECT RAISE(ABORT,'injected repair failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = d.sql.Exec("UPDATE step_results SET status='running' WHERE run_id=?", ids[0]); err == nil {
		t.Fatal("trigger should reject update")
	}
	if _, err = d.sql.Exec("DROP TRIGGER fail_phantom"); err != nil {
		t.Fatal(err)
	}
	if _, err = d.sql.Exec("UPDATE step_results SET status='running' WHERE run_id=?", ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err = d.sql.Exec(`CREATE TRIGGER fail_phantom BEFORE UPDATE ON step_results BEGIN SELECT RAISE(ABORT,'injected repair failure'); END`); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "rollback.sqlite")
	if _, err = d.RepairPhantoms(context.Background(), PhantomRepairOptions{Now: now, Apply: true, BackupPath: backup}, owners); err == nil {
		t.Fatal("injected failure ignored")
	}
	r, err := d.GetRun(ids[0])
	if err != nil || r.Status != types.RunRunning || r.Error != nil {
		t.Fatalf("partial transaction leaked: %+v %v", r, err)
	}
}

func TestPhantomRepairPreservesExistingBackup(t *testing.T) {
	d := openTestDB(t)
	for _, content := range []string{"", "existing backup must survive"} {
		t.Run(fmt.Sprintf("bytes_%d", len(content)), func(t *testing.T) {
			backup := filepath.Join(t.TempDir(), "existing.sqlite")
			if err := os.WriteFile(backup, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			checked := false
			_, err := d.RepairPhantoms(context.Background(), PhantomRepairOptions{Apply: true, BackupPath: backup}, func([]PhantomRun) (map[string]string, error) {
				checked = true
				return nil, nil
			})
			if err == nil || checked {
				t.Fatalf("existing backup: err=%v ownership checked=%v, want refusal before repair", err, checked)
			}
			got, err := os.ReadFile(backup)
			if err != nil || string(got) != content {
				t.Fatalf("existing backup changed: content=%q err=%v", got, err)
			}
		})
	}
}
