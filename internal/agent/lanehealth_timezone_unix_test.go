//go:build unix

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeQuotaResetUsesTheTimezonePassedToTheChild(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	seen := filepath.Join(dir, "seen-tz")
	script := `#!/bin/sh
cat >/dev/null
printf '%s' "$TZ" > "$NS_TEST_TZ_FILE"
printf '%s\n' "You've hit your usage limit. try again at Sep 15th, 2026 1:25 AM" >&2
exit 1
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TZ", "UTC")
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	store := laneTestStore(t, &now)
	ag := WithLaneHealth(&codexAgent{bin: bin}, store, func() time.Time { return now })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := ag.Run(ctx, RunOpts{CWD: dir, Prompt: "probe fixture", Env: []string{
		"TZ=UTC", "TZ=America/Chicago", "NS_TEST_TZ_FILE=" + seen,
	}})
	if !IsQuotaOutage(err) {
		t.Fatalf("expected classified process failure: %v", err)
	}
	gotEnv, readErr := os.ReadFile(seen)
	if readErr != nil || string(gotEnv) != "America/Chicago" {
		t.Fatalf("child timezone = %q, read error = %v", gotEnv, readErr)
	}
	mark, live := store.Outage("codex")
	if !live || !mark.Until.Equal(time.Date(2026, 9, 15, 6, 25, 0, 0, time.UTC)) {
		t.Fatalf("provider clock disagrees with the child's actual timezone: %+v", mark)
	}
	if !strings.Contains(err.Error(), "CDT -05:00 (America/Chicago)") {
		t.Fatalf("reset zone missing: %v", err)
	}
}
