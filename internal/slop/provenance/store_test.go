package provenance_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/slop/provenance"
)

func TestFileStoreAppendsVersionedRecordsAndReturnsRecentLaneModelHistory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := provenance.NewFileStore(dir)
	records := []provenance.Record{
		recordAt("lane-a", "model-a", "pass", time.Unix(1, 0)),
		recordAt("lane-b", "model-a", "fail", time.Unix(2, 0)),
		recordAt("lane-a", "model-a", "fail", time.Unix(3, 0)),
	}
	for _, record := range records {
		if err := store.Append(record); err != nil {
			t.Fatal(err)
		}
	}

	history, err := store.Recent("lane-a", "model-a", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Outcome != "pass" || history[1].Outcome != "fail" {
		t.Fatalf("history = %+v, want matching records in append order", history)
	}
	if history[1].SchemaVersion != provenance.CurrentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", history[1].SchemaVersion, provenance.CurrentSchemaVersion)
	}

	content, err := os.ReadFile(filepath.Join(dir, provenance.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(content)), "\n") + 1; lines != 3 {
		t.Fatalf("record lines = %d, want 3", lines)
	}
}

func TestFileStoreSerializesConcurrentAppends(t *testing.T) {
	t.Parallel()

	store := provenance.NewFileStore(t.TempDir())
	const total = 20
	var wg sync.WaitGroup
	errs := make(chan error, total)
	for index := 0; index < total; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			record := recordAt("lane-a", "model-a", "pass", time.Unix(int64(index+1), 0))
			errs <- store.Append(record)
		}(index)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	history, err := store.Recent("lane-a", "model-a", total)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != total {
		t.Fatalf("history has %d records, want %d", len(history), total)
	}
}

func TestFileStoreRejectsMalformedOrUnknownHistory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, provenance.FileName), []byte("{not-json}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := provenance.NewFileStore(dir).Recent("lane-a", "model-a", 10); err == nil {
		t.Fatal("expected malformed history to fail closed")
	}
}

func TestFileStoreReturnsNoHistoryBeforeDataDirectoryExists(t *testing.T) {
	t.Parallel()

	store := provenance.NewFileStore(filepath.Join(t.TempDir(), "not-created-yet"))
	history, err := store.Recent("lane-a", "model-a", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Fatalf("history = %+v, want none", history)
	}
}

func recordAt(lane, model, outcome string, recordedAt time.Time) provenance.Record {
	return provenance.Record{
		SchemaVersion:   provenance.CurrentSchemaVersion,
		RecordedAt:      recordedAt,
		Provider:        "provider-a",
		Model:           model,
		ReasoningEffort: "high",
		AgentLaneID:     lane,
		ChangeClass:     "source",
		SelectedTier:    "single-review",
		FindingsByLens: map[string]provenance.LensFindings{
			"test-capitulation": {
				Accepted: []provenance.Finding{{Path: "calc_test.go", Line: 12, Description: "test removed"}},
				Rejected: []provenance.Finding{},
			},
		},
		Rounds:    1,
		FixGrowth: 0,
		Outcome:   outcome,
	}
}
