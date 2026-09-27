package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLaneQuotaUsesInvocationTimezoneAndRetainsItAfterReload(t *testing.T) {
	t.Setenv("TZ", "UTC")
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	store := laneTestStore(t, &now)
	inner := &fallbackTestAgent{name: "codex", run: func() (*Result, error) {
		return nil, errors.New("You've hit your usage limit. try again at Sep 15th, 2026 1:25 AM")
	}}
	lane := WithLaneHealth(inner, store, func() time.Time { return now })
	_, first := lane.Run(context.Background(), RunOpts{Env: []string{"TZ=UTC", "TZ=America/Chicago"}})
	mark, live := store.Outage(laneTestScope("codex").Key)
	if !live {
		t.Fatal("missing mark")
	}
	want := time.Date(2026, 9, 15, 6, 25, 0, 0, time.UTC)
	if !mark.Until.Equal(want) {
		t.Errorf("reset = %s; want %s for the child process's timezone", mark.Until, want)
	}
	_, skipped := lane.Run(context.Background(), RunOpts{})
	for name, err := range map[string]error{"first": first, "persisted skip": skipped} {
		if err == nil || !strings.Contains(err.Error(), "America/Chicago") || !strings.Contains(err.Error(), "-05:00") {
			t.Errorf("%s lost reset timezone: %v", name, err)
		}
	}
	if inner.calls != 1 {
		t.Fatalf("expected skipped invocation, calls = %d", inner.calls)
	}
}

func TestQuotaObservationTimezonePrecedence(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	t.Setenv("TZ", "America/Chicago")
	for _, tc := range []struct {
		name string
		env  []string
		want string
	}{
		{"inherited", nil, "America/Chicago"},
		{"last invocation value", []string{"TZ=UTC", "TZ=:America/New_York"}, "America/New_York"},
		{"empty is UTC", []string{"TZ="}, "UTC"},
		{"unsupported falls back", []string{"TZ=unknown-zone"}, "UTC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := quotaObservationTime(now, tc.env)
			if !got.Equal(now) || got.Location().String() != tc.want {
				t.Fatalf("observation = %s (%s), want same instant in %s", got, got.Location(), tc.want)
			}
		})
	}
}

func TestLaneQuotaBannerTimezoneOverridesInvocationTimezone(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	store := laneTestStore(t, &now)
	inner := &fallbackTestAgent{name: "codex", run: func() (*Result, error) {
		return nil, errors.New("You've hit your usage limit. try again at Sep 15th, 2026 1:25 AM (America/Chicago)")
	}}
	_, err := WithLaneHealth(inner, store, func() time.Time { return now }).Run(context.Background(), RunOpts{Env: []string{"TZ=Asia/Tokyo"}})
	var outage *LaneOutageError
	if !errors.As(err, &outage) {
		t.Fatalf("expected quota error: %v", err)
	}
	// The aggregate is used when every fallback failed, so it too must carry
	// the source clock, independently of the banner excerpt being displayed.
	outage.Reason = ""
	for _, msg := range []string{outage.Error(), allLanesExhausted([]*LaneOutageError{outage}, true).Error()} {
		if !strings.Contains(msg, "2026-09-15 01:25 CDT -05:00 (America/Chicago)") {
			t.Errorf("reset zone missing: %s", msg)
		}
	}
}
