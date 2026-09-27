package lanehealth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreScopesOutagesByAccountAndModel(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	store := testStore(t, &now)
	mark := Outage{
		Lane:       "codex",
		AccountID:  "account-a",
		Model:      "gpt-5.6-sol",
		Until:      now.Add(4 * 24 * time.Hour),
		ObservedAt: now,
	}
	if err := store.Mark(mark); err != nil {
		t.Fatalf("Mark: %v", err)
	}

	key := ScopeKey(mark.AccountID, mark.Model)
	if got, live := store.Outage(key); !live || got.AccountID != mark.AccountID || got.Model != mark.Model {
		t.Fatalf("same account/model outage = %+v, live %t", got, live)
	}
	for _, scope := range [][2]string{
		{"account-b", "gpt-5.6-sol"},
		{"account-a", "gpt-5.6-flash"},
	} {
		other := ScopeKey(scope[0], scope[1])
		if got, live := store.Outage(other); live {
			t.Errorf("unrelated account/model %q is blocked by %+v", other, got)
		}
		if store.ClaimProbe(other) {
			t.Errorf("unrelated account/model %q claimed another scope's probe", other)
		}
	}

	now = now.Add(ProbeInterval)
	if !store.ClaimProbe(key) {
		t.Fatal("the marked account/model must claim its due probe")
	}
	if store.ClaimProbe(key) {
		t.Fatal("the same account/model must not claim a second probe in the interval")
	}
}

func TestStoreIgnoresLegacyLaneWideMarks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lane-health.json")
	legacy := `{"lanes":{"codex":{"lane":"codex","until":"2026-09-12T12:00:00Z","reason":"usage limit"}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}
	store := NewStore(path, func() time.Time { return time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC) })
	key := ScopeKey("account-a", "gpt-5.6-sol")
	if outage, live := store.Outage(key); live {
		t.Fatalf("legacy lane-wide mark must not block a scoped account/model: %+v", outage)
	}
}
