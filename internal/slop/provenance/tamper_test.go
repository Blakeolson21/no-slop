package provenance_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/slop/provenance"
)

// The round-3 review found two ways to clear a live escalation from inside the
// worktree, both cheaper than producing better changes. These are their probes.

func appendRecord(t *testing.T, store *provenance.FileStore, changeID string, accepted int, outcome string) {
	t.Helper()
	findings := make([]provenance.Finding, accepted)
	for index := range findings {
		findings[index] = provenance.Finding{Description: "test weakened"}
	}
	record := provenance.Record{
		ChangeID:     changeID,
		Model:        "model-x",
		AgentLaneID:  "lane-a",
		SelectedTier: "single-review",
		Outcome:      outcome,
	}
	if accepted > 0 {
		record.FindingsByLens = map[string]provenance.LensFindings{
			"test-capitulation": {Accepted: findings},
		}
	}
	if err := store.Append(record); err != nil {
		t.Fatal(err)
	}
}

func acceptedCount(records []provenance.Record, lens string) int {
	total := 0
	for _, record := range records {
		total += len(record.FindingsByLens[lens].Accepted)
	}
	return total
}

// TestARerunCannotOverwriteAnIncriminatingRecord is the S5 probe. Keeping the
// LATEST record per change id meant one identical re-run appended a clean
// record under the same change id, that record replaced the incriminating one,
// and an active escalation was gone in a single command. The old plain tail
// needed ten re-runs; the replacement needed one.
func TestARerunCannotOverwriteAnIncriminatingRecord(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := provenance.NewFileStore(dir)
	appendRecord(t, store, "aaa..bbb", 3, "fail")

	history, err := store.Recent("lane-a", "model-x", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := acceptedCount(history, "test-capitulation"); got != 3 {
		t.Fatalf("seeded history reports %d accepted findings, want 3", got)
	}

	// The re-run: same change id, no findings, and an outcome of "error"
	// because the run refused at the reviewer before producing any.
	appendRecord(t, store, "aaa..bbb", 0, "error")

	history, err = store.Recent("lane-a", "model-x", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := acceptedCount(history, "test-capitulation"); got != 3 {
		t.Fatalf("one re-run cleared the record: %d accepted findings remain, want 3", got)
	}
	if len(history) != 1 {
		t.Fatalf("history = %d entries, want the change to still count once", len(history))
	}
}

// TestAWorseRerunRaisesTheRecord is the other direction. Folding to the worst
// record must not lose new evidence either.
func TestAWorseRerunRaisesTheRecord(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := provenance.NewFileStore(dir)
	appendRecord(t, store, "aaa..bbb", 1, "fail")
	appendRecord(t, store, "aaa..bbb", 4, "fail")

	history, err := store.Recent("lane-a", "model-x", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := acceptedCount(history, "test-capitulation"); got != 4 {
		t.Fatalf("accepted findings = %d, want the worse run's 4", got)
	}
}

// TestDistinctChangesStillEachCount keeps the window meaningful: folding is per
// change id, not across the history.
func TestDistinctChangesStillEachCount(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := provenance.NewFileStore(dir)
	appendRecord(t, store, "aaa..bbb", 1, "fail")
	appendRecord(t, store, "bbb..ccc", 1, "fail")
	appendRecord(t, store, "ccc..ddd", 1, "fail")

	history, err := store.Recent("lane-a", "model-x", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("history = %d entries, want three distinct changes", len(history))
	}
	if got := acceptedCount(history, "test-capitulation"); got != 3 {
		t.Fatalf("accepted findings = %d, want 3", got)
	}
}

// TestDeletingTheHistoryIsNotAFreshStart is the S6 probe. Removing
// <workdir>/.noslop-data/provenance-v1.jsonl printed the same line an honest
// first-time lane prints and dropped the tier back, which made every other
// escalation control unbackstoppable.
func TestDeletingTheHistoryIsNotAFreshStart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := provenance.NewFileStore(dir)
	appendRecord(t, store, "aaa..bbb", 3, "fail")

	if err := os.Remove(filepath.Join(dir, provenance.FileName)); err != nil {
		t.Fatal(err)
	}

	_, err := provenance.NewFileStore(dir).Recent("lane-a", "model-x", 10)
	if err == nil {
		t.Fatal("deleting the history read as a first-time lane")
	}
	if !strings.Contains(err.Error(), "the store was removed") {
		t.Fatalf("error does not say the store was removed: %v", err)
	}
}

// TestTruncatingTheHistoryIsNotAFreshStart covers the half-measure: dropping
// only the incriminating lines.
func TestTruncatingTheHistoryIsNotAFreshStart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := provenance.NewFileStore(dir)
	appendRecord(t, store, "aaa..bbb", 3, "fail")
	appendRecord(t, store, "bbb..ccc", 3, "fail")

	path := filepath.Join(dir, provenance.FileName)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(content), "\n")
	if err := os.WriteFile(path, []byte(lines[0]), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = provenance.NewFileStore(dir).Recent("lane-a", "model-x", 10)
	if err == nil {
		t.Fatal("truncating the history read as a shorter honest history")
	}
	if !strings.Contains(err.Error(), "the store was truncated") {
		t.Fatalf("error does not say the store was truncated: %v", err)
	}
}

// TestANeverWrittenStoreIsStillAFreshStart is the control. The high-water mark
// must distinguish a store that was removed from one that never existed, or
// every repository adopting NoSlop would start at full-adversarial forever.
func TestANeverWrittenStoreIsStillAFreshStart(t *testing.T) {
	t.Parallel()

	history, err := provenance.NewFileStore(t.TempDir()).Recent("lane-a", "model-x", 10)
	if err != nil {
		t.Fatalf("an empty data directory refused to read: %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("history = %+v, want empty", history)
	}
}

// TestAStoreWrittenBeforeTheHighWaterMarkExistedIsAdopted keeps the upgrade
// path open: an existing history with no sidecar is honest history, not
// tampering, and the next append records its mark.
func TestAStoreWrittenBeforeTheHighWaterMarkExistedIsAdopted(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := provenance.NewFileStore(dir)
	appendRecord(t, store, "aaa..bbb", 1, "fail")
	if err := os.Remove(filepath.Join(dir, provenance.HighWaterFileName)); err != nil {
		t.Fatal(err)
	}

	history, err := provenance.NewFileStore(dir).Recent("lane-a", "model-x", 10)
	if err != nil {
		t.Fatalf("a pre-sidecar history refused to read: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history = %d entries, want the existing record", len(history))
	}

	appendRecord(t, provenance.NewFileStore(dir), "bbb..ccc", 1, "fail")
	if _, err := os.Stat(filepath.Join(dir, provenance.HighWaterFileName)); err != nil {
		t.Fatalf("the next append did not record a high-water mark: %v", err)
	}
}
