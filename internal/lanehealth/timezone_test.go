package lanehealth

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestClassifyPersistsResetTimezone(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	store := testStore(t, &now)
	mark, _ := Classify("codex", "You've hit your usage limit. try again at Sep 15th, 2026 1:25 AM (America/Chicago)", now)
	if err := store.Mark(mark); err != nil {
		t.Fatal(err)
	}
	persisted, ok := store.Outage("codex")
	if !ok {
		t.Fatal("missing mark")
	}
	raw, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"reset_timezone":"America/Chicago"`) {
		t.Fatalf("named timezone lost on reload: %s", raw)
	}
	if got := persisted.Until.Format(time.RFC3339); got != "2026-09-15T01:25:00-05:00" {
		t.Fatalf("reset = %s", got)
	}
}

func TestResetTimeRetainsZoneAndOffsetAcrossJSON(t *testing.T) {
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	for _, month := range []time.Month{time.January, time.September} {
		until := time.Date(2026, month, 15, 1, 25, 0, 0, chicago)
		original := Outage{Until: until, ResetTimezone: "America/Chicago"}
		raw, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		var loaded Outage
		if err := json.Unmarshal(raw, &loaded); err != nil {
			t.Fatal(err)
		}
		want := until.Format("2006-01-02 15:04 MST -07:00") + " (America/Chicago)"
		if got := loaded.ResetTime(); got != want {
			t.Errorf("reset = %q, want %q", got, want)
		}
	}
}

func TestResetTimePreservesLegacyDisplayAndStoredOffset(t *testing.T) {
	until := time.Date(2026, 9, 15, 1, 25, 0, 0, time.FixedZone("CDT", -5*60*60))
	if got, want := (Outage{Until: until}).ResetTime(), until.Local().Format("2006-01-02 15:04 MST"); got != want {
		t.Errorf("legacy reset = %q, want %q", got, want)
	}
	// The persisted offset wins if timezone data is unavailable or disagrees.
	for _, zone := range []string{"Unavailable/Zone", "UTC"} {
		got := (Outage{Until: until, ResetTimezone: zone}).ResetTime()
		if !strings.Contains(got, "2026-09-15 01:25") || !strings.Contains(got, "-05:00") {
			t.Errorf("lost stored clock for %s: %s", zone, got)
		}
	}
}
