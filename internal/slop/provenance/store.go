// Package provenance stores the generating-agent history used to condition
// later NoSlop policy decisions.
package provenance

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
type LensFindings struct {
	Accepted []Finding `json:"accepted"`
	Rejected []Finding `json:"rejected"`
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
	Recent(agentLaneID, model string, limit int) ([]Record, error)
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

// FileStore keeps one append-only JSON Lines file under a repository data dir.
//
// Residual, accepted and bounded: the store lives on the same filesystem as the
// change being judged, so a party with write access can delete or truncate it.
// Deleting it reads as a first-time lane, which loses an escalation rather than
// granting a pass, and the gate prints the store's location on every run
// precisely so that reading the output tells you whether the history it
// consulted was inside the worktree. Closing this properly needs a history the
// audited party cannot write, which is a service, not a file.
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
	return nil
}

// Recent returns the last matching lane/model records in append order, one per
// distinct change.
//
// The de-duplication is load-bearing. A plain tail meant re-running the gate on
// the same trivial change ten times evicted an incriminating record and
// reversed an active escalation, at the cost of ten seconds. Keying the window
// on distinct changes makes ageing history out cost ten real changes instead,
// which is work rather than a loop. It does not make the window unreachable,
// and it is not claimed to: see the FileStore doc comment.
func (s *FileStore) Recent(agentLaneID, model string, limit int) ([]Record, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("read provenance: limit must be positive")
	}
	records, err := s.readAll()
	if err != nil {
		return nil, err
	}
	var matches []Record
	for _, record := range records {
		if record.AgentLaneID == agentLaneID && record.Model == model {
			matches = append(matches, record)
		}
	}
	matches = latestPerChange(matches)
	if len(matches) > limit {
		matches = append([]Record(nil), matches[len(matches)-limit:]...)
	}
	return matches, nil
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

// latestPerChange keeps the last record written for each identified change id,
// preserving the order in which those changes were first seen. A record whose
// change id is absent or "unknown" names nothing, so it is never folded into
// another: collapsing on a non-identity would drop real history.
func latestPerChange(records []Record) []Record {
	position := make(map[string]int, len(records))
	var deduped []Record
	for _, record := range records {
		if !identifiedKey(record.ChangeID) {
			deduped = append(deduped, record)
			continue
		}
		if index, seen := position[record.ChangeID]; seen {
			deduped[index] = record
			continue
		}
		position[record.ChangeID] = len(deduped)
		deduped = append(deduped, record)
	}
	return deduped
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
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
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
