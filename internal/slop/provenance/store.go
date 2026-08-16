// Package provenance stores the generating-agent history used to condition
// later NoSlop policy decisions.
package provenance

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Blakeolson21/no-slop/internal/filelock"
)

const (
	// CurrentSchemaVersion is the JSON record shape written by this build.
	CurrentSchemaVersion = 1
	// FileName is the append-only JSON Lines history file inside slop.data_dir.
	FileName = "provenance-v1.jsonl"
)

// Finding is the stable evidence retained for one lens disposition.
type Finding struct {
	Path        string `json:"path"`
	Line        int    `json:"line"`
	Description string `json:"description"`
}

// LensFindings records which findings were accepted and rejected for a lens.
//
// Noticed is a third bucket that deliberately scores nothing. A notice is the
// one severity the gate reports without failing on, and recording it as accepted
// made it ratchet twice over: incriminating() pinned the record forever, and
// LensScores counted it toward the three-finding escalation, which only a
// reviewed clean pass can clear. A repository that keeps bumping a submodule
// never produces that pass, so the notice severity reproduced one tier at a time
// exactly the permanent penalty it was introduced to remove. Notices stay on the
// record for visibility; only warning and error ratchet.
type LensFindings struct {
	Accepted []Finding `json:"accepted"`
	Rejected []Finding `json:"rejected"`
	Noticed  []Finding `json:"noticed,omitempty"`
}

// Record is the versioned provenance and outcome schema for one gated change.
type Record struct {
	SchemaVersion   int                     `json:"schema_version"`
	RecordedAt      time.Time               `json:"recorded_at"`
	ChangeID        string                  `json:"change_id"`
	Provider        string                  `json:"provider"`
	Model           string                  `json:"model"`
	ReasoningEffort string                  `json:"reasoning_effort"`
	AgentLaneID     string                  `json:"agent_lane_id"`
	ChangeClass     string                  `json:"change_class"`
	SelectedTier    string                  `json:"selected_tier"`
	FindingsByLens  map[string]LensFindings `json:"findings_by_lens"`
	Rounds          int                     `json:"rounds"`
	FixGrowth       int                     `json:"fix_growth"`
	Outcome         string                  `json:"outcome"`
}

// Reader is the history seam consumed by the risk classifier.
type Reader interface {
	// Window returns the retained history for one lane and model, oldest
	// first, one entry per distinct change folded to the worst outcome any run
	// of it produced. It takes no limit on purpose: see FileStore.Window.
	Window(agentLaneID, model string) ([]Record, error)
	// HasIdentifiedHistory reports whether this store holds any record that
	// names a real generating lane and model. It exists so a run that supplies
	// no identity can be told apart from a repository that never used
	// identities: the first is hiding from history that exists, the second has
	// no history to hide from.
	HasIdentifiedHistory() (bool, error)
}

// Store appends records and reads recent generating-lane history.
type Store interface {
	Reader
	Append(Record) error
}

// HighWaterFileName is the sidecar recording how many records this store has
// ever accepted. It exists so that deleting or truncating the history is not
// indistinguishable from never having had one.
const HighWaterFileName = "provenance-v1.count"

// FileStore keeps one append-only JSON Lines file under a repository data dir.
//
// Residual, accepted and bounded, and stated as exactly what it is rather than
// as a guarantee. The store lives on the same filesystem as the change being
// judged, so a party with write access can delete it, truncate it, or write
// records into it by hand. What this file store guarantees is narrower than
// "an escalation cannot be cleared":
//
//   - Deleting or truncating the history is caught, not prevented. The
//     high-water sidecar makes a history shorter than the count already
//     accepted read as tampering, and the classifier escalates on an unreadable
//     history. Deleting the history and the sidecar TOGETHER still resets the
//     store.
//   - Re-running, replaying, or amending a change cannot evict anything. That
//     part is structural: retention is by age and severity with no count in it,
//     and each change folds to the worst result any run of it produced.
//   - Hand-writing a plausible record is not prevented at all. Every field a
//     forger would need is a field this file holds in the clear.
//
// Closing the last one needs a history the audited party cannot write, which is
// a service rather than a file. slop.provenance_required refuses a run whose
// store went missing, and pointing slop.data_dir at a directory the audited
// party cannot write is the operator-side answer available today.
type FileStore struct {
	dir string
	now func() time.Time
}

// NewFileStore constructs a repository-local provenance store.
func NewFileStore(dataDir string) *FileStore {
	return &FileStore{dir: dataDir, now: time.Now}
}

// Append writes exactly one new record without rewriting existing history.
func (s *FileStore) Append(record Record) error {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return fmt.Errorf("append provenance: data directory is not configured")
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("append provenance: create data directory: %w", err)
	}
	if record.SchemaVersion == 0 {
		record.SchemaVersion = CurrentSchemaVersion
	}
	if record.RecordedAt.IsZero() {
		record.RecordedAt = s.now().UTC()
	}
	normalizeRecord(&record)
	if err := validateRecord(record); err != nil {
		return fmt.Errorf("append provenance: %w", err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("append provenance: encode record: %w", err)
	}

	path := filepath.Join(s.dir, FileName)
	lock, err := filelock.Acquire(path + ".lock")
	if err != nil {
		return fmt.Errorf("append provenance: %w", err)
	}
	defer lock.Release()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("append provenance: open history: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("append provenance: write history: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("append provenance: sync history: %w", err)
	}
	if err := s.raiseHighWater(); err != nil {
		return err
	}
	return nil
}

// raiseHighWater records the number of records the history now holds. The count
// only ever rises, so a later read can tell a store that was truncated from one
// that was never written.
func (s *FileStore) raiseHighWater() error {
	present, err := countLines(filepath.Join(s.dir, FileName))
	if err != nil {
		return fmt.Errorf("append provenance: count history: %w", err)
	}
	recorded, _, err := s.readHighWater()
	if err != nil {
		return err
	}
	if present <= recorded {
		return nil
	}
	path := filepath.Join(s.dir, HighWaterFileName)
	if err := os.WriteFile(path, []byte(strconv.Itoa(present)+"\n"), 0o644); err != nil {
		return fmt.Errorf("append provenance: record history high-water mark: %w", err)
	}
	return nil
}

func (s *FileStore) readHighWater() (int, bool, error) {
	content, err := os.ReadFile(filepath.Join(s.dir, HighWaterFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read provenance: open history high-water mark: %w", err)
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil || value < 0 {
		return 0, true, fmt.Errorf("read provenance: history high-water mark is unreadable")
	}
	return value, true, nil
}

func countLines(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	count := 0
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			count++
		}
	}
	return count, scanner.Err()
}

// RetentionWindow is how long an unremarkable record stays in the conditioning
// window. A record that recorded accepted findings is retained past it, so the
// window bounds bookkeeping rather than bounding accountability.
const RetentionWindow = 90 * 24 * time.Hour

// Window returns the retained matching lane/model history in append order, one
// entry per distinct change, each folded to the WORST outcome any run of that
// change recorded.
//
// The retention rule is age and severity, and it deliberately takes no count.
// Every count-shaped rule this store has had was an eviction tool, because the
// audited party mints the thing being counted:
//
//   - A plain tail of the last N RECORDS meant re-running the gate ten times on
//     one trivial change aged an incriminating record out in ten seconds.
//   - Keying on distinct CHANGES fixed that and left `git commit --amend`:
//     amending mints a new head SHA for byte-identical content, so ten amends
//     of one README edit produced ten distinct change ids and cleared a live
//     escalation just as fast. The store's own comment claimed this cost ten
//     real changes; it cost ten commit objects describing one one-line diff.
//   - Keeping the LATEST record per change was cheaper still: one identical
//     re-run overwrote the incriminating record outright.
//
// There is no count an author cannot inflate, so no count decides retention. A
// record is retained while it is younger than RetentionWindow, and a record
// carrying accepted findings is retained regardless of age. Rotation therefore
// cannot remove an incriminating record at any price; only a reviewed pass
// clears its weight, which LensScores decides from the history itself.
func (s *FileStore) Window(agentLaneID, model string) ([]Record, error) {
	records, err := s.readAll()
	if err != nil {
		return nil, err
	}
	cutoff := s.now().UTC().Add(-RetentionWindow)
	var matches []Record
	for _, record := range records {
		if record.AgentLaneID != agentLaneID || record.Model != model {
			continue
		}
		if record.RecordedAt.Before(cutoff) && !incriminating(record) {
			continue
		}
		matches = append(matches, record)
	}
	return worstPerChange(matches), nil
}

// incriminating reports whether a record carries accepted findings, which is
// the property that exempts it from ageing out.
func incriminating(record Record) bool {
	for _, findings := range record.FindingsByLens {
		if len(findings.Accepted) > 0 {
			return true
		}
	}
	return false
}

// LensScores counts the accepted findings each lens carries across a retained
// window, and is the one place a lens's weight can be cleared.
//
// Accepted findings only. Subtracting rejections let a forged record with three
// rejected findings cancel a real record with three accepted ones, so the
// audited party could clear an active escalation by writing history rather than
// by producing better changes.
//
// A lens's count resets at a REVIEWED PASS: a record whose change actually ran
// the full-adversarial protocol, completed its review rounds, passed, and
// carried no accepted finding for that lens. That is the only thing that clears
// an escalation. Rotation cannot, because Window retains incriminating records
// regardless of age, and a re-run cannot, because worstPerChange keeps the
// worst result any run of a change produced.
//
// The residual is stated rather than papered over: this store is a file on the
// same filesystem as the change being judged, so a party with write access can
// hand-write a record claiming a clean full-adversarial pass, exactly as they
// can hand-write anything else in it. What the rule removes is the version that
// needed no forgery at all. slop.provenance_required and a data directory the
// audited party cannot write are the operator-side answers; see the FileStore
// comment.
func LensScores(records []Record) map[string]int {
	scores := make(map[string]int)
	for _, record := range records {
		reviewed := isReviewedPass(record)
		for lens, findings := range record.FindingsByLens {
			scores[lens] += len(findings.Accepted)
		}
		if !reviewed {
			continue
		}
		for lens := range scores {
			if len(record.FindingsByLens[lens].Accepted) == 0 {
				scores[lens] = 0
			}
		}
	}
	return scores
}

// isReviewedPass reports whether a record is evidence that the escalated
// protocol actually ran on this change and came back clean.
func isReviewedPass(record Record) bool {
	return record.SelectedTier == "full-adversarial" && record.Outcome == "pass" && record.Rounds >= 2
}

// HasIdentifiedHistory reports whether any record names a real lane and model.
func (s *FileStore) HasIdentifiedHistory() (bool, error) {
	records, err := s.readAll()
	if err != nil {
		return false, err
	}
	for _, record := range records {
		if identifiedKey(record.AgentLaneID) && identifiedKey(record.Model) {
			return true, nil
		}
	}
	return false, nil
}

func identifiedKey(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && trimmed != "unknown"
}

// worstPerChange folds every record written for one identified change id into a
// single record carrying, per lens, the largest accepted-finding set any run of
// that change recorded, and the highest tier any of them selected.
//
// Per-lens rather than per-record, because escalation is decided per lens: a
// run that found three test-capitulation findings and a later run of the same
// change that found three evidence-mismatch findings are both real history, and
// picking whichever record "looks worse" overall would drop one of them.
//
// A record whose change id is absent or "unknown" names nothing, so it is never
// folded into another: collapsing on a non-identity would drop real history.
func worstPerChange(records []Record) []Record {
	position := make(map[string]int, len(records))
	var folded []Record
	for _, record := range records {
		if !identifiedKey(record.ChangeID) {
			folded = append(folded, record)
			continue
		}
		index, seen := position[record.ChangeID]
		if !seen {
			position[record.ChangeID] = len(folded)
			folded = append(folded, record)
			continue
		}
		folded[index] = foldWorst(folded[index], record)
	}
	return folded
}

// foldWorst merges a later record for the same change into the record already
// held, keeping whichever side is more incriminating on each axis. Nothing here
// can lower a count, which is the property that makes a re-run useless as an
// eviction tool.
func foldWorst(held, later Record) Record {
	merged := held
	merged.FindingsByLens = make(map[string]LensFindings, len(held.FindingsByLens)+len(later.FindingsByLens))
	for lens, findings := range held.FindingsByLens {
		merged.FindingsByLens[lens] = findings
	}
	for lens, findings := range later.FindingsByLens {
		existing, ok := merged.FindingsByLens[lens]
		if !ok || len(findings.Accepted) > len(existing.Accepted) {
			existing.Accepted = findings.Accepted
		}
		if !ok || len(findings.Rejected) > len(existing.Rejected) {
			existing.Rejected = findings.Rejected
		}
		if !ok || len(findings.Noticed) > len(existing.Noticed) {
			existing.Noticed = findings.Noticed
		}
		merged.FindingsByLens[lens] = existing
	}
	if tierRank(later.SelectedTier) > tierRank(held.SelectedTier) {
		merged.SelectedTier = later.SelectedTier
	}
	if outcomeRank(later.Outcome) > outcomeRank(held.Outcome) {
		merged.Outcome = later.Outcome
	}
	if later.Rounds > merged.Rounds {
		merged.Rounds = later.Rounds
	}
	if later.FixGrowth > merged.FixGrowth {
		merged.FixGrowth = later.FixGrowth
	}
	if later.RecordedAt.After(merged.RecordedAt) {
		merged.RecordedAt = later.RecordedAt
	}
	return merged
}

func tierRank(tier string) int {
	switch tier {
	case "leak-scan-only":
		return 1
	case "single-review":
		return 2
	case "full-adversarial":
		return 3
	default:
		return 0
	}
}

// outcomeRank orders outcomes from least to most incriminating. "error" is a
// run that never reached a verdict, so it ranks below a recorded failure: the
// eviction the reviewer found worked precisely because an error record was
// allowed to stand in for a completed one.
func outcomeRank(outcome string) int {
	switch outcome {
	case "pass":
		return 1
	case "error":
		return 2
	case "fail":
		return 3
	default:
		return 0
	}
}

func (s *FileStore) readAll() ([]Record, error) {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return nil, fmt.Errorf("read provenance: data directory is not configured")
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return nil, fmt.Errorf("read provenance: create data directory: %w", err)
	}
	path := filepath.Join(s.dir, FileName)
	lock, err := filelock.Acquire(path + ".lock")
	if err != nil {
		return nil, fmt.Errorf("read provenance: %w", err)
	}
	defer lock.Release()
	highWater, hasHighWater, err := s.readHighWater()
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// A store that has never been written reads as no history, which
			// keeps the v1 route. A store that HAS been written and is now
			// missing is the cheapest way to clear an escalation, so the
			// sidecar count turns it into an unreadable history instead, and
			// the classifier escalates on that.
			if hasHighWater && highWater > 0 {
				return nil, fmt.Errorf("read provenance: history is absent but %d records were recorded here; the store was removed", highWater)
			}
			return nil, nil
		}
		return nil, fmt.Errorf("read provenance: open history: %w", err)
	}
	defer file.Close()

	var records []Record
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		var record Record
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("read provenance: decode line %d: %w", line, err)
		}
		if err := validateRecord(record); err != nil {
			return nil, fmt.Errorf("read provenance: line %d: %w", line, err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read provenance: scan history: %w", err)
	}
	if hasHighWater && len(records) < highWater {
		return nil, fmt.Errorf("read provenance: history holds %d records but %d were recorded here; the store was truncated", len(records), highWater)
	}
	return records, nil
}

func normalizeRecord(record *Record) {
	unknownFields := []*string{
		&record.ChangeID,
		&record.Provider,
		&record.Model,
		&record.ReasoningEffort,
		&record.AgentLaneID,
		&record.ChangeClass,
		&record.SelectedTier,
		&record.Outcome,
	}
	for _, field := range unknownFields {
		*field = strings.TrimSpace(*field)
		if *field == "" {
			*field = "unknown"
		}
	}
	if record.FindingsByLens == nil {
		record.FindingsByLens = make(map[string]LensFindings)
	}
	for lens, findings := range record.FindingsByLens {
		if findings.Accepted == nil {
			findings.Accepted = []Finding{}
		}
		if findings.Rejected == nil {
			findings.Rejected = []Finding{}
		}
		record.FindingsByLens[lens] = findings
	}
}

func validateRecord(record Record) error {
	if record.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported schema version %d", record.SchemaVersion)
	}
	if record.RecordedAt.IsZero() {
		return fmt.Errorf("recorded_at is required")
	}
	if record.Rounds < 0 {
		return fmt.Errorf("rounds must not be negative")
	}
	if record.FixGrowth < 0 {
		return fmt.Errorf("fix_growth must not be negative")
	}
	return nil
}
