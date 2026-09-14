package cli

import (
	"bytes"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/paths"
	"github.com/Blakeolson21/no-slop/internal/types"
)

// seedStoreParkFixture prepares a fresh NS_HOME store with one terminal run
// still carrying a park marker (the lint's target), one parked running run
// (must never be flagged), and one clean terminal run. It returns the three
// run IDs and the marker timestamp backdate used for the stale rows.
func seedStoreParkFixture(t *testing.T) (staleID, liveID, cleanID string, parkedAt int64) {
	t.Helper()
	nmHome := makeSocketSafeTempDir(t)
	t.Setenv("NS_HOME", nmHome)
	p, err := paths.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo, err := database.InsertRepo(t.TempDir(), "https://example.com/repo.git", "main")
	if err != nil {
		t.Fatal(err)
	}
	newRun := func(branch string) string {
		run, err := database.InsertRun(repo.ID, branch, "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222")
		if err != nil {
			t.Fatal(err)
		}
		if err := database.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
			t.Fatal(err)
		}
		if err := database.SetRunAwaitingAgent(run.ID); err != nil {
			t.Fatal(err)
		}
		return run.ID
	}
	staleID = newRun("feature/stale")
	liveID = newRun("feature/live")
	cleanID = newRun("feature/clean")
	if err := database.ClearRunAwaitingAgent(cleanID); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateRunStatus(staleID, types.RunCompleted); err != nil {
		t.Fatal(err)
	}
	// Simulate the legacy row the lint and repair exist for: a terminal status
	// written before the shared clear-on-terminal fragment, marker still set.
	parkedAt = time.Now().Unix() - 10
	legacy, err := sql.Open("sqlite", p.DB())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`UPDATE runs SET awaiting_agent_since = ? WHERE id = ?`, parkedAt, staleID); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	return staleID, liveID, cleanID, parkedAt
}

func TestAxiLintStoreReportsStaleParkMarkers(t *testing.T) {
	staleID, _, _, _ := seedStoreParkFixture(t)

	cmd := newAxiLintStoreCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("lint-store with a stale marker must exit nonzero")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 1 {
		t.Fatalf("lint-store error = %v, want exit code 1", err)
	}
	text := out.String()
	for _, want := range []string{
		"stale_park_markers: 1",
		staleID,
		"runs[1]{ID,Status,Branch,AwaitingAgentSince}",
		",completed,feature/stale,",
		"migrate-park-markers",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("lint-store output missing %q in:\n%s", want, text)
		}
	}
}

func TestAxiLintStoreCleanStoreExitsZero(t *testing.T) {
	seedStoreParkFixture(t)
	p, err := paths.New()
	if err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.RepairParkMarkers(); err != nil {
		t.Fatal(err)
	}
	database.Close()

	cmd := newAxiLintStoreCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("clean lint-store must exit zero, got %v, output:\n%s", err, out.String())
	}
	text := out.String()
	if !strings.Contains(text, "stale_park_markers: 0") {
		t.Errorf("clean lint-store output missing \"stale_park_markers: 0\" in:\n%s", text)
	}
}

func TestAxiMigrateParkMarkersClearsStaleMarkersIdempotently(t *testing.T) {
	staleID, liveID, _, _ := seedStoreParkFixture(t)
	p, err := paths.New()
	if err != nil {
		t.Fatal(err)
	}

	cmd := newAxiMigrateParkMarkersCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("migrate-park-markers: %v, output:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "repaired: 1") {
		t.Errorf("migrate output missing \"repaired: 1\" in:\n%s", out.String())
	}

	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repaired, err := database.GetRun(staleID)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.AwaitingAgentSince != nil {
		t.Errorf("AwaitingAgentSince = %d after migrate, want nil", *repaired.AwaitingAgentSince)
	}
	if repaired.ParkedMS < 10000 {
		t.Errorf("parked_ms = %d after migrate, want the parked time folded in (>= 10000)", repaired.ParkedMS)
	}
	live, err := database.GetRun(liveID)
	if err != nil {
		t.Fatal(err)
	}
	if live.AwaitingAgentSince == nil {
		t.Error("migrate must not clear the marker of a parked running run")
	}

	// Idempotent: a second run repairs nothing.
	out.Reset()
	cmd = newAxiMigrateParkMarkersCmd()
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if !strings.Contains(out.String(), "repaired: 0") {
		t.Errorf("second migrate output missing \"repaired: 0\" in:\n%s", out.String())
	}
}
