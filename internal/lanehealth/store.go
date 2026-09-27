package lanehealth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Blakeolson21/no-slop/internal/filelock"
)

// maxOutages bounds the persisted state so a misconfigured account/model list
// cannot grow the file forever. Entries closest to expiry are dropped first.
const maxOutages = 32

const stateVersion = 2

// Store persists account/model outages in a small JSON file under NS_HOME so a
// mark discovered by one run is honored by every concurrent run and by every
// later run, including after a daemon restart. Without that, each run pays a
// full agent spawn to rediscover the same exhausted account/model - the 2026-08-04 incident,
// where roughly a dozen runs failed one after another on the same dead Codex
// quota.
//
// Reads are lock-free and see whole files only, because writes land via
// os.Rename. Writes take a short advisory file lock so two runs marking
// different lanes at the same moment cannot lose one another's mark.
type Store struct {
	path string
	now  func() time.Time
}

// NewStore returns a Store persisting at path. now is injectable for
// deterministic tests; nil means time.Now.
func NewStore(path string, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{path: path, now: now}
}

type state struct {
	Version int               `json:"version"`
	Outages map[string]Outage `json:"outages"`
}

// Outage reports the live outage for scopeKey, if any. A mark whose reset time
// has arrived is not live: that account/model is presumed recovered and gets
// tried again.
func (s *Store) Outage(scopeKey string) (Outage, bool) {
	if s == nil || s.path == "" {
		return Outage{}, false
	}
	current := s.load()
	outage, ok := current.Outages[scopeKey]
	if !ok || !outage.Until.After(s.now()) {
		return Outage{}, false
	}
	return outage, true
}

// Mark records an outage, replacing any existing mark for the same
// account/model. Unscoped legacy marks are ignored because there is no safe way
// to map them onto an exact provider account and model.
func (s *Store) Mark(outage Outage) error {
	if s == nil || s.path == "" || outage.Lane == "" {
		return nil
	}
	if outage.ScopeKey == "" {
		outage.ScopeKey = ScopeKey(outage.AccountID, outage.Model)
	}
	if outage.ScopeKey == "" {
		return nil
	}
	return s.mutate(func(current *state) {
		// A failed recovery probe produces a fresh classification, which has no
		// probe timestamp of its own. Keep the durable claim (including one
		// made by another process) rather than making a probed outage look as
		// though it has never been tested. ObservedAt still restarts backoff.
		if previous := current.Outages[outage.ScopeKey]; previous.LastProbeAt.After(outage.LastProbeAt) {
			outage.LastProbeAt = previous.LastProbeAt
		}
		current.Outages[outage.ScopeKey] = outage
	})
}

// ClearObservedBefore drops the mark for scopeKey when it was observed no later
// than startedAt. An account/model that just completed an invocation is demonstrably
// healthy, so its mark - including one written from a misread banner - must not
// outlive that evidence.
//
// The cutoff is what makes the mark sticky across concurrent runs: an
// invocation authorized before the provider ran out of quota still completes,
// and its success says nothing about a banner another run hit while it was
// streaming. Clearing that fresher mark would send the next run right back into
// the dead account/model, which is the burst this package exists to stop. A mark with no
// ObservedAt - a legacy row, or one written by hand - carries no such evidence
// and is always cleared.
func (s *Store) ClearObservedBefore(scopeKey string, startedAt time.Time) error {
	if s == nil || s.path == "" || scopeKey == "" {
		return nil
	}
	current := s.load()
	if !clearable(current, scopeKey, startedAt) {
		return nil
	}
	return s.mutate(func(current *state) {
		if clearable(*current, scopeKey, startedAt) {
			delete(current.Outages, scopeKey)
		}
	})
}

func clearable(current state, scopeKey string, startedAt time.Time) bool {
	outage, present := current.Outages[scopeKey]
	return present && !outage.ObservedAt.After(startedAt)
}

// ClaimProbe reports whether the caller may send one probe invocation through
// an account/model that is currently marked, and durably records the claim so
// concurrent runs and later runs do not all probe the same scope at once.
//
// It answers true only when the claim was written, so a lock or write failure
// keeps the lane skipped rather than turning every run into a probe. A mark
// with no ObservedAt - a legacy row, or one written by hand - starts its probe
// clock at the first claim instead of being probed immediately.
//
// Every invocation of a marked scope asks, and all but one per interval are
// refused, so a refusal is decided from a lock-free read and takes the
// exclusive lock only when it is going to write. Reading stale state can only
// understate how long ago the last probe was, so no probe is lost, and the
// decision is made again under the lock before anything is recorded.
func (s *Store) ClaimProbe(scopeKey string) bool {
	if s == nil || s.path == "" || scopeKey == "" {
		return false
	}
	now := s.now()
	if write, _ := probeDecision(s.load(), scopeKey, now); !write {
		return false
	}
	claimed := false
	err := s.mutate(func(current *state) {
		write, claim := probeDecision(*current, scopeKey, now)
		if !write {
			return
		}
		outage := current.Outages[scopeKey]
		outage.LastProbeAt = now
		current.Outages[scopeKey] = outage
		claimed = claim
	})
	if err != nil {
		return false
	}
	return claimed
}

// probeDecision reports whether lane's probe clock has to be written, and
// whether that write is a claim the caller may probe on. Starting the clock for
// a mark with no observation time is a write that is not a claim.
func probeDecision(current state, scopeKey string, now time.Time) (write, claim bool) {
	outage, ok := current.Outages[scopeKey]
	if !ok || !outage.Until.After(now) {
		return false, false
	}
	since := lastProbeReference(outage)
	if since.IsZero() {
		return true, false
	}
	if now.Sub(since) < ProbeInterval {
		return false, false
	}
	return true, true
}

func lastProbeReference(outage Outage) time.Time {
	if outage.LastProbeAt.After(outage.ObservedAt) {
		return outage.LastProbeAt
	}
	return outage.ObservedAt
}

// Snapshot returns every live outage, ordered by lane, account, and model.
func (s *Store) Snapshot() []Outage {
	if s == nil || s.path == "" {
		return nil
	}
	now := s.now()
	current := s.load()
	live := make([]Outage, 0, len(current.Outages))
	for _, outage := range current.Outages {
		if outage.Until.After(now) {
			live = append(live, outage)
		}
	}
	sort.Slice(live, func(i, j int) bool {
		if live[i].Lane != live[j].Lane {
			return live[i].Lane < live[j].Lane
		}
		if live[i].AccountID != live[j].AccountID {
			return live[i].AccountID < live[j].AccountID
		}
		return live[i].Model < live[j].Model
	})
	return live
}

func (s *Store) mutate(apply func(*state)) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create lane health directory: %w", err)
	}
	lock, err := filelock.Acquire(s.path + ".lock")
	if err != nil {
		return fmt.Errorf("lock lane health state: %w", err)
	}
	defer lock.Release()

	current := s.load()
	apply(&current)
	s.prune(&current)
	return s.save(current)
}

// load fails open: an unreadable or corrupt state file means "every lane
// healthy", which degrades to the pre-cooldown behavior instead of wedging
// every run behind a file it cannot parse.
func (s *Store) load() state {
	data, err := os.ReadFile(s.path)
	if err == nil {
		var parsed state
		if json.Unmarshal(data, &parsed) == nil && parsed.Version == stateVersion && parsed.Outages != nil {
			return parsed
		}
	}
	return state{Version: stateVersion, Outages: map[string]Outage{}}
}

func (s *Store) prune(current *state) {
	now := s.now()
	for key, outage := range current.Outages {
		if !outage.Until.After(now) {
			delete(current.Outages, key)
		}
	}
	if len(current.Outages) <= maxOutages {
		return
	}
	outages := make([]Outage, 0, len(current.Outages))
	for _, outage := range current.Outages {
		outages = append(outages, outage)
	}
	sort.Slice(outages, func(i, j int) bool { return outages[i].Until.After(outages[j].Until) })
	kept := make(map[string]Outage, maxOutages)
	for _, outage := range outages[:maxOutages] {
		kept[outage.ScopeKey] = outage
	}
	current.Outages = kept
}

// save writes atomically via rename so a concurrent reader never observes a
// partial file.
func (s *Store) save(current state) error {
	current.Version = stateVersion
	data, err := json.Marshal(current)
	if err != nil {
		return fmt.Errorf("encode lane health state: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".lane-health-*")
	if err != nil {
		return fmt.Errorf("create lane health temp file: %w", err)
	}
	tmpPath := tmp.Name()
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write lane health state: %w", firstErr(writeErr, closeErr))
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace lane health state: %w", err)
	}
	return nil
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
