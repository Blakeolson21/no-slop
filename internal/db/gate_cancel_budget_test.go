package db

import (
	"crypto/ed25519"
	"encoding/json"
	"github.com/Blakeolson21/no-slop/internal/types"
	"path/filepath"
	"strings"
	"testing"
)

func TestGateCancelBudgetSurvivesReopenAndStatusChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := d.InsertRepo("/test", "https://example.com/test", "main")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		r, e := d.InsertRun(repo.ID, "codex/lane", "head", "base")
		if e != nil {
			t.Fatal(e)
		}
		if e = d.UpdateRunErrorStatus(r.ID, "aborted by user", types.RunCancelled); e != nil {
			t.Fatal(e)
		}
		// Later diagnostics cannot erase a spent abort or count it twice.
		if e = d.UpdateRunStatus(r.ID, types.RunFailed); e != nil {
			t.Fatal(e)
		}
		if e = d.UpdateRunStatus(r.ID, types.RunCancelled); e != nil {
			t.Fatal(e)
		}
	}
	d.Close()
	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.InsertRun(repo.ID, "codex/lane", "head2", "base"); err == nil || !strings.Contains(err.Error(), "cancel budget") {
		t.Fatalf("third dispatch was not refused: %v", err)
	}
	if _, err = d.InsertRun(repo.ID, "codex/other", "head2", "base"); err != nil {
		t.Fatalf("unrelated lane blocked: %v", err)
	}
}

func TestGateCancelBudgetSignedGrantConsumedAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo, _ := d.InsertRepo("/test", "https://example.com/test", "main")
	var ids []string
	for i := 0; i < 2; i++ {
		r, e := d.InsertRun(repo.ID, "lane", "head", "base")
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, r.ID)
		if e = d.UpdateRunStatus(r.ID, types.RunCancelled); e != nil {
			t.Fatal(e)
		}
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	record := CancelAdjudication{RepoID: repo.ID, Branch: "lane", HeadSHA: "next", AbortedRunIDs: ids, Reason: "coordinator authorized one retry", Issuer: "coordinator"}
	payload, _ := json.Marshal(record)
	raw, _ := json.Marshal(SignedCancelAdjudication{Payload: payload, Signature: ed25519.Sign(priv, payload)})
	if _, err = d.RegisterCancelAdjudication(raw, "", pub); err == nil {
		t.Fatal("missing reason accepted")
	}
	if _, err = d.RegisterCancelAdjudication(raw, "different reason", pub); err == nil {
		t.Fatal("wrong reason accepted")
	}
	wrong, _, _ := ed25519.GenerateKey(nil)
	if _, err = d.RegisterCancelAdjudication(raw, record.Reason, wrong); err == nil {
		t.Fatal("self signature accepted")
	}
	id, err := d.RegisterCancelAdjudication(raw, record.Reason, pub)
	if err != nil {
		t.Fatal(err)
	}
	// Each connection races a separate atomic SQLite INSERT, as after restarts.
	const count = 6
	connections := make([]*DB, count)
	for i := range connections {
		connections[i], err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer connections[i].Close()
	}
	start := make(chan struct{})
	result := make(chan error, count)
	for _, conn := range connections {
		go func(c *DB) { <-start; _, e := c.InsertRun(repo.ID, "lane", "next", "base"); result <- e }(conn)
	}
	close(start)
	accepted := 0
	for range connections {
		e := <-result
		if e == nil {
			accepted++
		} else if !strings.Contains(e.Error(), "cancel budget") {
			t.Fatal(e)
		}
	}
	if accepted != 1 {
		t.Fatalf("admitted %d concurrent third attempts", accepted)
	}
	var used, reason string
	if err = d.sql.QueryRow(`SELECT used_run_id,reason FROM cancel_adjudications WHERE id=?`, id).Scan(&used, &reason); err != nil {
		t.Fatal(err)
	}
	if used == "" || reason != record.Reason {
		t.Fatal("grant/run evidence missing")
	}
	if _, err = d.RegisterCancelAdjudication(raw, record.Reason, pub); err != nil {
		t.Fatal(err)
	}
	if _, err = d.InsertRun(repo.ID, "lane", "next", "base"); err == nil {
		t.Fatal("replayed grant was reusable")
	}
}
