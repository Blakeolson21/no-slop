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

	history, err := store.Window("lane-a", "model-x")
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

// TestDistinctChangesCannotAgeAnIncriminatingRecordOut replaces the test that
// pinned the old last-10-distinct-changes window as an accepted residual.
//
// It was not a residual, it was the next eviction. `git commit --amend` mints a
// fresh head SHA for byte-identical content, and the change id is
// "<base>..<head>", so ten amends of one trivial README edit produced ten
// distinct change ids and cleared a live escalation in seconds. The window's own
// comment claimed this cost ten real changes.
//
// The reviewer's probe is reproduced literally: one incriminating record, then
// twenty distinct change ids for the same trivial edit. Retention is by age and
// severity with no count in it, so the record stays.
func TestDistinctChangesCannotAgeAnIncriminatingRecordOut(t *testing.T) {
	t.Parallel()

	store := provenance.NewFileStore(t.TempDir())
	if err := store.Append(findingRecord("lane-a", "model-x", "base..bad", time.Unix(1, 0), 3, 0)); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 20; index++ {
		clean := findingRecord("lane-a", "model-x", fmt.Sprintf("base..amend%d", index), time.Unix(int64(index+2), 0), 0, 0)
		if err := store.Append(clean); err != nil {
			t.Fatal(err)
		}
	}

	history, err := store.Window("lane-a", "model-x")
	if err != nil {
		t.Fatal(err)
	}
	if provenance.LensScores(history)["test-capitulation"] != 3 {
		t.Fatalf("twenty distinct change ids evicted the incriminating record: scores = %v", provenance.LensScores(history))
	}
}

// TestAnAgedRecordWithNoFindingsIsRetiredAndAnIncriminatingOneIsNot pins both
// halves of the retention rule, so "retain everything forever" cannot pass for
// the fix either.
func TestAnAgedRecordWithNoFindingsIsRetiredAndAnIncriminatingOneIsNot(t *testing.T) {
	t.Parallel()

	store := provenance.NewFileStore(t.TempDir())
	old := time.Now().UTC().Add(-2 * provenance.RetentionWindow)
	if err := store.Append(findingRecord("lane-a", "model-x", "base..oldclean", old, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(findingRecord("lane-a", "model-x", "base..oldbad", old, 3, 0)); err != nil {
		t.Fatal(err)
	}

	history, err := store.Window("lane-a", "model-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].ChangeID != "base..oldbad" {
		t.Fatalf("history = %+v, want only the aged incriminating record retained", history)
	}
}

// noticeRecord is a run whose only finding is the one severity the gate reports
// without failing on, which is what a submodule pointer bump produces.
func noticeRecord(changeID string, at time.Time) provenance.Record {
	return provenance.Record{
		SchemaVersion: provenance.CurrentSchemaVersion,
		RecordedAt:    at,
		ChangeID:      changeID,
		Model:         "model-x",
		AgentLaneID:   "lane-a",
		SelectedTier:  "single-review",
		Outcome:       "pass",
		FindingsByLens: map[string]provenance.LensFindings{
			"submodule-pointer-unscanned": {
				Noticed: []provenance.Finding{{Path: "vendor/dep", Line: 1, Description: "submodule pointer moved"}},
			},
		},
	}
}

// TestSubmoduleNoticesNeitherPinRetentionNorEscalate pins the point of having a
// non-blocking severity at all. Filing notices as accepted findings made them
// ratchet twice: the record was retained past its window forever, and three of
// them promoted the lens and escalated every later run, clearable only by a
// reviewed clean pass that a repository which keeps bumping that submodule never
// produces. That reproduced the permanent penalty the notice was introduced to
// remove, one tier at a time.
func TestSubmoduleNoticesNeitherPinRetentionNorEscalate(t *testing.T) {
	t.Parallel()

	store := provenance.NewFileStore(t.TempDir())
	old := time.Now().UTC().Add(-2 * provenance.RetentionWindow)
	for _, changeID := range []string{"base..bump1", "base..bump2", "base..bump3"} {
		if err := store.Append(noticeRecord(changeID, old)); err != nil {
			t.Fatal(err)
		}
	}

	history, err := store.Window("lane-a", "model-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Errorf("notice-only records were pinned past retention: %+v", history)
	}
	if score := provenance.LensScores(history)["submodule-pointer-unscanned"]; score != 0 {
		t.Errorf("notices scored toward the lens escalation: score = %d", score)
	}

	// The same three records inside the window must be readable and still score
	// nothing, so visibility is kept without conditioning later runs.
	fresh := provenance.NewFileStore(t.TempDir())
	for _, changeID := range []string{"base..bump1", "base..bump2", "base..bump3"} {
		if err := fresh.Append(noticeRecord(changeID, time.Now().UTC())); err != nil {
			t.Fatal(err)
		}
	}
	recent, err := fresh.Window("lane-a", "model-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 3 {
		t.Fatalf("history = %+v, want the three notice records retained for visibility", recent)
	}
	if score := provenance.LensScores(recent)["submodule-pointer-unscanned"]; score != 0 {
		t.Errorf("in-window notices scored toward the lens escalation: score = %d", score)
	}
}

// TestOnlyAReviewedPassClearsALensScore is the other half of T4: an escalation
// has to be clearable by something, and the only thing that clears it is the
// escalated protocol actually running and coming back clean.
func TestOnlyAReviewedPassClearsALensScore(t *testing.T) {
	t.Parallel()

	incriminating := findingRecord("lane-a", "model-x", "base..bad", time.Unix(1, 0), 3, 0)

	cheapPass := findingRecord("lane-a", "model-x", "base..cheap", time.Unix(2, 0), 0, 0)
	cheapPass.SelectedTier = "leak-scan-only"
	cheapPass.Outcome = "pass"
	if score := provenance.LensScores([]provenance.Record{incriminating, cheapPass})["test-capitulation"]; score != 3 {
		t.Fatalf("a cheap-tier pass cleared the escalation: score = %d", score)
	}

	unreviewed := findingRecord("lane-a", "model-x", "base..noreview", time.Unix(3, 0), 0, 0)
	unreviewed.SelectedTier = "full-adversarial"
	unreviewed.Outcome = "pass"
	unreviewed.Rounds = 0
	if score := provenance.LensScores([]provenance.Record{incriminating, unreviewed})["test-capitulation"]; score != 3 {
		t.Fatalf("a full-tier record that never ran its review rounds cleared the escalation: score = %d", score)
	}

	reviewed := findingRecord("lane-a", "model-x", "base..reviewed", time.Unix(4, 0), 0, 0)
	reviewed.SelectedTier = "full-adversarial"
	reviewed.Outcome = "pass"
	reviewed.Rounds = 2
	if score := provenance.LensScores([]provenance.Record{incriminating, reviewed})["test-capitulation"]; score != 0 {
		t.Fatalf("a completed clean full-adversarial pass did not clear the escalation: score = %d", score)
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
