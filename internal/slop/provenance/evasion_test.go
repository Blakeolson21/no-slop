package provenance_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/slop/provenance"
)

func findingRecord(laneID, model, changeID string, at time.Time, accepted, rejected int) provenance.Record {
	record := provenance.Record{
		SchemaVersion:  provenance.CurrentSchemaVersion,
		RecordedAt:     at,
		ChangeID:       changeID,
		Model:          model,
		AgentLaneID:    laneID,
		SelectedTier:   "single-review",
		Outcome:        "fail",
		FindingsByLens: map[string]provenance.LensFindings{},
	}
	lens := provenance.LensFindings{}
	for index := 0; index < accepted; index++ {
		lens.Accepted = append(lens.Accepted, provenance.Finding{Path: "calc_test.go", Line: index + 1, Description: "test removed"})
	}
	for index := 0; index < rejected; index++ {
		lens.Rejected = append(lens.Rejected, provenance.Finding{Path: "calc_test.go", Line: index + 1, Description: "disputed"})
	}
	record.FindingsByLens["test-capitulation"] = lens
	return record
}

// TestReplayingOneChangeCannotEvictHistory covers the cheapest of the
// escalation evasions: a plain last-N tail meant re-running the gate on the
// same trivial change ten times aged an incriminating record out of the window
// and reversed an active escalation.
func TestReplayingOneChangeCannotEvictHistory(t *testing.T) {
	t.Parallel()

	store := provenance.NewFileStore(t.TempDir())
	if err := store.Append(findingRecord("lane-a", "model-x", "base..bad", time.Unix(1, 0), 3, 0)); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 12; index++ {
		clean := findingRecord("lane-a", "model-x", "base..trivial", time.Unix(int64(index+2), 0), 0, 0)
		if err := store.Append(clean); err != nil {
			t.Fatal(err)
		}
	}

	history, err := store.Recent("lane-a", "model-x", 10)
	if err != nil {
		t.Fatal(err)
	}
	accepted := 0
	for _, record := range history {
		accepted += len(record.FindingsByLens["test-capitulation"].Accepted)
	}
	if accepted != 3 {
		t.Fatalf("history holds %d accepted findings across %d records, want the incriminating record retained", accepted, len(history))
	}
}

// TestTenDistinctChangesStillAgeHistoryOut states the residual plainly. The
// window is a window; de-duplication makes ageing it out cost real changes
// rather than a loop, and this test says so rather than leaving the reader to
// assume the history is permanent.
func TestTenDistinctChangesStillAgeHistoryOut(t *testing.T) {
	t.Parallel()

	store := provenance.NewFileStore(t.TempDir())
	if err := store.Append(findingRecord("lane-a", "model-x", "base..bad", time.Unix(1, 0), 3, 0)); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 10; index++ {
		clean := findingRecord("lane-a", "model-x", fmt.Sprintf("base..change%d", index), time.Unix(int64(index+2), 0), 0, 0)
		if err := store.Append(clean); err != nil {
			t.Fatal(err)
		}
	}
	history, err := store.Recent("lane-a", "model-x", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range history {
		if record.ChangeID == "base..bad" {
			t.Fatal("ten distinct changes should have aged the record out; the documented residual no longer matches the code")
		}
	}
}

// TestHasIdentifiedHistorySeparatesOptOutFromEvasion is what lets an omitted
// --lane-id be told apart from a repository that never recorded identities.
func TestHasIdentifiedHistorySeparatesOptOutFromEvasion(t *testing.T) {
	t.Parallel()

	anonymous := provenance.NewFileStore(t.TempDir())
	if err := anonymous.Append(provenance.Record{
		SchemaVersion: provenance.CurrentSchemaVersion,
		RecordedAt:    time.Unix(1, 0),
		Outcome:       "pass",
	}); err != nil {
		t.Fatal(err)
	}
	identified, err := anonymous.HasIdentifiedHistory()
	if err != nil {
		t.Fatal(err)
	}
	if identified {
		t.Fatal("a record whose lane and model are unknown must not count as identified history")
	}

	named := provenance.NewFileStore(t.TempDir())
	if err := named.Append(findingRecord("lane-a", "model-x", "base..head", time.Unix(1, 0), 1, 0)); err != nil {
		t.Fatal(err)
	}
	identified, err = named.HasIdentifiedHistory()
	if err != nil {
		t.Fatal(err)
	}
	if !identified {
		t.Fatal("a record naming a lane and model is identified history")
	}
}
