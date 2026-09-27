package cli

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/lanehealth"
	"github.com/Blakeolson21/no-slop/internal/paths"
	"github.com/Blakeolson21/no-slop/internal/telemetry"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// A lane parked by a quota cooldown is installed and looks healthy, so without
// this row the operator has no way to see why the pipeline is not using it.
func TestDoctorReportsAQuotaExhaustedAgentLane(t *testing.T) {
	restore := telemetry.SetDefaultForTesting(&telemetryRecorder{})
	defer restore()

	home := t.TempDir()
	t.Setenv("NS_HOME", home)

	binDir := t.TempDir()
	writeFakeBinary(t, binDir, "codex")
	t.Setenv("PATH", binDir)

	p, err := paths.New()
	if err != nil {
		t.Fatalf("paths.New: %v", err)
	}
	until := time.Now().Add(72 * time.Hour).Truncate(time.Minute)
	store := lanehealth.NewStore(p.LaneHealthFile(), nil)
	if err := store.Mark(doctorTestOutage("codex", "gpt-5.6-sol", until)); err != nil {
		t.Fatalf("Mark: %v", err)
	}

	out, err := executeCmd("doctor")
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, out)
	}

	line := doctorLineContaining(t, out, "quota-exhausted")
	if !strings.Contains(line, "quota-exhausted") {
		t.Fatalf("doctor must report the scoped quota cooldown:\n%s", line)
	}
	if !strings.Contains(line, until.Local().Format("2006-01-02 15:04 MST")) {
		t.Fatalf("codex row must name the reset time:\n%s", line)
	}
}

// The Agents section and the gate-validation row describe the same lane, so a
// bare "codex is runnable" next to "codex quota-exhausted" tells the operator
// the parked lane is fine.
func TestDoctorGateValidationNamesTheCooldownOnTheResolvedAgent(t *testing.T) {
	restore := telemetry.SetDefaultForTesting(&telemetryRecorder{})
	defer restore()

	home := t.TempDir()
	t.Setenv("NS_HOME", home)

	binDir := t.TempDir()
	writeFakeBinary(t, binDir, "codex")
	t.Setenv("PATH", binDir)

	p, err := paths.New()
	if err != nil {
		t.Fatalf("paths.New: %v", err)
	}
	until := time.Now().Add(72 * time.Hour).Truncate(time.Minute)
	store := lanehealth.NewStore(p.LaneHealthFile(), nil)
	if err := os.WriteFile(p.ConfigFile(), []byte("agent: codex\nagent_args_override:\n  codex: [--model, gpt-5.6-sol]\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := store.Mark(doctorConfiguredOutage(t, types.AgentCodex, "gpt-5.6-sol", until)); err != nil {
		t.Fatalf("Mark: %v", err)
	}

	out, err := executeCmd("doctor")
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, out)
	}

	line := doctorLineContaining(t, out, "gate validation")
	if !strings.Contains(line, "quota-exhausted") {
		t.Fatalf("gate validation must not report the parked lane as simply runnable:\n%s", line)
	}
	if !strings.Contains(line, until.Local().Format("2006-01-02 15:04 MST")) {
		t.Fatalf("gate validation must name the reset time:\n%s", line)
	}
	if !strings.Contains(line, "model gpt-5.6-sol") {
		t.Fatalf("gate validation must identify the exact model scope:\n%s", line)
	}
}

func TestDoctorGateValidationStaysPlainForAHealthyAgent(t *testing.T) {
	restore := telemetry.SetDefaultForTesting(&telemetryRecorder{})
	defer restore()

	t.Setenv("NS_HOME", t.TempDir())
	binDir := t.TempDir()
	writeFakeBinary(t, binDir, "codex")
	t.Setenv("PATH", binDir)

	out, err := executeCmd("doctor")
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, out)
	}
	line := doctorLineContaining(t, out, "gate validation")
	if !strings.Contains(line, "codex is runnable") {
		t.Fatalf("gate validation must report an unmarked agent as runnable:\n%s", line)
	}
	if strings.Contains(line, "quota-exhausted") {
		t.Fatalf("an unmarked agent must not be reported as parked:\n%s", line)
	}
}

func doctorLineContaining(t *testing.T, out, needle string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	t.Fatalf("no %q row in doctor output:\n%s", needle, out)
	return ""
}

func TestDoctorReportsAnExpiredMarkAsHealthy(t *testing.T) {
	restore := telemetry.SetDefaultForTesting(&telemetryRecorder{})
	defer restore()

	home := t.TempDir()
	t.Setenv("NS_HOME", home)

	binDir := t.TempDir()
	codexPath := writeFakeBinary(t, binDir, "codex")
	t.Setenv("PATH", binDir)

	p, err := paths.New()
	if err != nil {
		t.Fatalf("paths.New: %v", err)
	}
	// Written by a clock in the past so the mark is already expired when doctor
	// reads it with the real clock.
	past := time.Now().Add(-2 * time.Hour)
	store := lanehealth.NewStore(p.LaneHealthFile(), func() time.Time { return past.Add(-time.Hour) })
	if err := store.Mark(doctorTestOutage("codex", "gpt-5.6-sol", past)); err != nil {
		t.Fatalf("Mark: %v", err)
	}

	out, err := executeCmd("doctor")
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, out)
	}

	if strings.Contains(out, "quota-exhausted") {
		t.Fatalf("an expired mark must not be reported:\n%s", out)
	}
	line := doctorAgentLine(t, out, "codex")
	if !strings.Contains(line, codexPath) {
		t.Fatalf("codex row must report the resolved binary:\n%s", line)
	}
}

func doctorTestOutage(lane, model string, until time.Time) lanehealth.Outage {
	accountID := "test-account-" + lane
	return lanehealth.Outage{
		ScopeKey: lanehealth.ScopeKey(accountID, model), AccountID: accountID, Model: model,
		Lane: lane, Until: until, Reason: "You've hit your usage limit",
	}
}

func doctorConfiguredOutage(t *testing.T, name types.AgentName, model string, until time.Time) lanehealth.Outage {
	t.Helper()
	a, err := agent.NewWithOptions(name, string(name), []string{"--model", model}, agent.Options{})
	if err != nil {
		t.Fatalf("NewWithOptions(%s): %v", name, err)
	}
	scope, ok := agent.ResolveQuotaScope(a, agent.RunOpts{})
	_ = a.Close()
	if !ok {
		t.Fatalf("could not resolve test scope for %s", name)
	}
	return lanehealth.Outage{
		ScopeKey: scope.Key, AccountID: scope.AccountID, Model: scope.Model,
		Lane: string(name), Until: until, Reason: "You've hit your usage limit",
	}
}
