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

func TestDoctorPreservesPersistedResetTimezone(t *testing.T) {
	restore := telemetry.SetDefaultForTesting(&telemetryRecorder{})
	defer restore()
	t.Setenv("NS_HOME", t.TempDir())
	binDir := t.TempDir()
	writeFakeBinary(t, binDir, "codex")
	t.Setenv("PATH", binDir)
	p, err := paths.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigFile(), []byte("agent: codex\nagent_args_override:\n  codex: [--model, gpt-5.6-sol]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(72 * time.Hour).In(chicago)
	mark := doctorConfiguredOutage(t, "codex", "gpt-5.6-sol", until)
	mark.ResetTimezone = "America/Chicago"
	if err := lanehealth.NewStore(p.LaneHealthFile(), nil).Mark(mark); err != nil {
		t.Fatal(err)
	}
	out, err := executeCmd("doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	want := until.Format("2006-01-02 15:04 MST -07:00") + " (America/Chicago)"
	for _, line := range []string{doctorLineContaining(t, out, "quota-exhausted"), doctorLineContaining(t, out, "gate validation")} {
		if !strings.Contains(line, want) {
			t.Errorf("persisted zone missing from doctor: %s; want %s", line, want)
		}
	}
}
