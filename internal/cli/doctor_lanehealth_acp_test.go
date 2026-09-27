package cli

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/lanehealth"
	"github.com/Blakeolson21/no-slop/internal/paths"
	"github.com/Blakeolson21/no-slop/internal/telemetry"
)

// An ACP-driven lane records its outage under the identity the agent reports
// ("acp:<target>"), not under the alias the operator configured, so doctor has
// to resolve the configured name the same way before it can see the cooldown.
// Reading the row by the alias reports the parked lane as installed and
// runnable, which is the invisibility this surface exists to remove.
func TestDoctorReportsAQuotaExhaustedACPAliasLane(t *testing.T) {
	restore := telemetry.SetDefaultForTesting(&telemetryRecorder{})
	defer restore()

	t.Setenv("NS_HOME", t.TempDir())

	binDir := t.TempDir()
	writeFakeBinary(t, binDir, "cursor-agent")
	writeFakeBinary(t, binDir, "acpx")
	t.Setenv("PATH", binDir)

	p, err := paths.New()
	if err != nil {
		t.Fatalf("paths.New: %v", err)
	}
	if err := os.WriteFile(p.ConfigFile(), []byte("agent: cursor\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	until := time.Now().Add(72 * time.Hour).Truncate(time.Minute)
	store := lanehealth.NewStore(p.LaneHealthFile(), nil)
	if err := store.Mark(doctorTestOutage("acp:cursor", "cursor-model", until)); err != nil {
		t.Fatalf("Mark: %v", err)
	}

	out, err := executeCmd("doctor")
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, out)
	}

	agentLine := doctorLineContaining(t, out, "quota-exhausted")
	if !strings.Contains(agentLine, "quota-exhausted") {
		t.Fatalf("doctor must report the quota cooldown recorded for acp:cursor:\n%s", agentLine)
	}
	if !strings.Contains(agentLine, "acp:cursor") {
		t.Fatalf("quota row must name the recorded lane:\n%s", agentLine)
	}
	if !strings.Contains(agentLine, until.Local().Format("2006-01-02 15:04 MST")) {
		t.Fatalf("cursor row must name the reset time:\n%s", agentLine)
	}

	gateLine := doctorLineContaining(t, out, "gate validation")
	if !strings.Contains(gateLine, "cursor is runnable") || strings.Contains(gateLine, "quota-exhausted") {
		t.Fatalf("an alias mark for an unknown account/model must not claim the whole lane is unavailable:\n%s", gateLine)
	}
}
