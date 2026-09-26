package daemon

import (
	"path/filepath"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/db"
)

func TestNoFixRunPersistsAndReachesIPC(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo, err := d.InsertRepo("/project", "https://example.com/repo.git", "main")
	if err != nil {
		t.Fatal(err)
	}
	for _, disabled := range []bool{false, true} {
		run, err := d.InsertRunWithIntent(repo.ID, "feature", "head", "base", nil, disabled)
		if err != nil {
			t.Fatal(err)
		}
		reloaded, err := d.GetRun(run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if reloaded.NoFix != disabled || runToInfo(d, reloaded, nil).NoFix != disabled {
			t.Fatalf("no-fix lost on reload/IPC: %+v", reloaded)
		}
	}
}
