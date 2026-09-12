package cli

import (
	"encoding/json"
	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/paths"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestStoreRepairDryRunAndFlagConflict(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux worker liveness")
	}
	root := t.TempDir()
	t.Setenv("NS_HOME", root)
	p := paths.WithRoot(root)
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	out, err := executeCmd("store", "repair-phantoms", "--dry-run")
	if err != nil {
		t.Fatalf("%s %v", out, err)
	}
	var receipt db.PhantomRepairReceipt
	if err = json.Unmarshal([]byte(out), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Apply || receipt.Affected != 0 || receipt.Store != p.DB() {
		t.Fatalf("dry=%+v", receipt)
	}
	if _, err = executeCmd("store", "repair-phantoms", "--dry-run", "--apply"); err == nil {
		t.Fatal("conflicting flags accepted")
	}
	backup := filepath.Join(root, "backup.sqlite")
	out, err = executeCmd("store", "repair-phantoms", "--apply", "--backup", backup)
	if err != nil {
		t.Fatalf("%s %v", out, err)
	}
	if _, err = os.Stat(backup); err != nil {
		t.Fatal(err)
	}
}
