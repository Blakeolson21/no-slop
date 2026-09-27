package lanehealth

import (
	"testing"
	"time"
)

func TestRemarkPreservesLastProbeAcrossStoreReload(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	store := testStore(t, &now)
	mark, _ := Classify("codex", "You've hit your usage limit. try again at Sep 15th, 2026 1:25 AM", now)
	mark = testScoped("codex", mark)
	if err := store.Mark(mark); err != nil {
		t.Fatal(err)
	}
	now = now.Add(ProbeInterval)
	key := mark.ScopeKey
	if !store.ClaimProbe(key) {
		t.Fatal("expected one probe")
	}
	probedAt := now
	now = now.Add(time.Minute)
	mark, _ = Classify("codex", "You've hit your usage limit. try again at Sep 15th, 2026 1:25 AM", now)
	mark = testScoped("codex", mark)
	if err := NewStore(store.path, func() time.Time { return now }).Mark(mark); err != nil {
		t.Fatal(err)
	}
	got, live := store.Outage(key)
	if !live || !got.LastProbeAt.Equal(probedAt) {
		t.Fatalf("rejected probe lost its persisted timestamp: %+v", got)
	}
	if !got.ObservedAt.Equal(now) {
		t.Fatalf("new observation lost: %+v", got)
	}
	if store.ClaimProbe(key) {
		t.Fatal("remark must retain backoff")
	}
}
