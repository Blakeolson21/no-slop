package db

import (
	"path/filepath"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestResponseReceiptSurvivesReopenAndCascadesWithRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := database.InsertRepo(t.TempDir(), "https://example.test/repo", "main")
	if err != nil {
		t.Fatal(err)
	}
	run, err := database.InsertRun(repo.ID, "feature", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	want := ResponseReceipt{RunID: run.ID, IdempotencyKey: "key", Step: types.StepReview, Round: 4, RequestHash: "digest"}
	if err := database.InsertResponseReceipt(want); err != nil {
		t.Fatal(err)
	}
	if err := database.InsertResponseReceipt(want); err == nil {
		t.Fatal("duplicate receipt overwrote acceptance")
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	got, err := database.GetResponseReceipt(run.ID, "key")
	if err != nil || got == nil || *got != want {
		t.Fatalf("receipt after reopen = %+v, %v", got, err)
	}
	if _, err := database.sql.Exec(`DELETE FROM runs WHERE id = ?`, run.ID); err != nil {
		t.Fatal(err)
	}
	got, err = database.GetResponseReceipt(run.ID, "key")
	if err != nil || got != nil {
		t.Fatalf("receipt outlived deleted run = %+v, %v", got, err)
	}
}
