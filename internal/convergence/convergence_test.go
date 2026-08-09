package convergence

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func makeRound(t *testing.T, round int, durationMS int64, findings ...types.Finding) *db.StepRound {
	t.Helper()
	payload := types.Findings{Items: findings, Summary: "round"}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	js := string(data)
	return &db.StepRound{Round: round, Trigger: "auto_fix", FindingsJSON: &js, DurationMS: durationMS}
}

func finding(file, description string) types.Finding {
	return types.Finding{
		ID:          "id-" + file,
		Severity:    "warning",
		File:        file,
		Description: description,
		Action:      types.ActionAskUser,
	}
}

func defaultThresholds() Thresholds {
	return Thresholds{NonDecreasingRounds: 3, RecurringRounds: 3, BudgetMS: 120 * 60 * 1000}
}

// A run whose findings count shrinks round over round is healthy and must not
// trip the guard: a guard that fires on converging runs is worse than none.
func TestBuildReport_ConvergingRunDoesNotTrip(t *testing.T) {
	rounds := []*db.StepRound{
		makeRound(t, 1, 60_000,
			finding("a.go", "nil pointer dereference when the config file is missing"),
			finding("b.go", "unchecked error return from file close in the writer"),
			finding("c.go", "data race on the shared counter in the worker pool"),
		),
		makeRound(t, 2, 45_000,
			finding("a.go", "the new fallback path swallows the parse error silently"),
			finding("b.go", "flush can run twice when retry fires during shutdown"),
		),
		makeRound(t, 3, 30_000,
			finding("a.go", "fallback default is documented as seconds but applied as milliseconds"),
		),
	}

	report := BuildReport(rounds, nil, false, defaultThresholds())

	if report.Warning != "" {
		t.Fatalf("converging run tripped the guard: %q", report.Warning)
	}
	if got, want := report.RoundFindings, []int{3, 2, 1}; !equalInts(got, want) {
		t.Fatalf("RoundFindings = %v, want %v", got, want)
	}
	if report.ReviewMS != 135_000 {
		t.Fatalf("ReviewMS = %d, want 135000", report.ReviewMS)
	}
	if report.NewSurfaceFindings != nil {
		t.Fatalf("NewSurfaceFindings should be nil when the submitted diff is unknown, got %v", *report.NewSurfaceFindings)
	}
}

// The observed Skill Life ladder: findings per round 1,1,1,2,3,3,3 across seven
// rounds. Non-decreasing counts across the trailing window must trip the guard.
func TestBuildReport_LadderShapeTrips(t *testing.T) {
	descriptions := []string{
		"retry loop spins without backoff in the uploader",
		"stale cache entry served after invalidation in the session store",
		"missing timeout on the outbound webhook client",
		"partial write left behind when the disk fills during export",
		"signal handler leaks the child process group on restart",
		"orphaned temp directory accumulates on failed imports",
		"unbounded queue growth when the consumer stalls",
		"double-close of the response body in the fetcher",
		"lock ordering inversion between the scheduler and the store",
		"clock skew breaks the lease renewal in the coordinator",
		"integer overflow in the pagination offset calculation",
		"symlink traversal allowed by the archive extractor",
		"credentials logged at debug level by the dialer",
		"iterator invalidated by concurrent map mutation in the router",
	}
	next := 0
	take := func() types.Finding {
		d := descriptions[next]
		next++
		return finding("distinct.go", d)
	}
	counts := []int{1, 1, 1, 2, 3, 3, 3}
	var rounds []*db.StepRound
	// Reuse the same per-round findings only within a round, never across
	// rounds, so the recurring-class trigger cannot be what fires here.
	for i, c := range counts {
		fs := make([]types.Finding, 0, c)
		for j := 0; j < c; j++ {
			if next >= len(descriptions) {
				next = 0
			}
			fs = append(fs, take())
		}
		rounds = append(rounds, makeRound(t, i+1, 10_000, fs...))
	}

	report := BuildReport(rounds, nil, false, defaultThresholds())

	if got, want := report.RoundFindings, counts; !equalInts(got, want) {
		t.Fatalf("RoundFindings = %v, want %v", got, want)
	}
	if report.Warning == "" {
		t.Fatal("ladder shape 1,1,1,2,3,3,3 did not trip the guard")
	}
	if !strings.Contains(report.Warning, "not decreased") {
		t.Fatalf("warning should name the non-decreasing trigger, got %q", report.Warning)
	}
}

// A defect class that is relocated instead of closed must be recognized as
// recurring even though every occurrence has a different id, file, and
// wording. Modeled on the real evidence: env-file parsing semantics reported
// three times in three different scripts.
func TestBuildReport_RecurringClassAcrossFilesDetected(t *testing.T) {
	rounds := []*db.StepRound{
		makeRound(t, 4, 10_000,
			types.Finding{
				ID: "duplicate-postgres-url-bypasses-authority-seam", Severity: "error",
				File:   "scripts/convex-self-hosted-vps/restore.sh",
				Action: types.ActionAskUser,
				Description: "restore.sh parses POSTGRES_URL from the env file with quoting semantics " +
					"that disagree with docker compose env parsing, bypassing the authority seam",
			},
		),
		makeRound(t, 6, 10_000,
			types.Finding{
				ID: "compose-env-authority-quoting-diverges", Severity: "error",
				File:   "scripts/convex-self-hosted-vps/backup.sh",
				Action: types.ActionAskUser,
				Description: "backup.sh env file parsing diverges from docker compose quoting semantics " +
					"for the authority env values",
			},
			types.Finding{
				ID: "unrelated-shell-word-splitting", Severity: "warning",
				File:        "scripts/deploy.sh",
				Action:      types.ActionAutoFix,
				Description: "unquoted command substitution word-splits the rsync target list",
			},
		),
		makeRound(t, 7, 10_000,
			types.Finding{
				ID: "shared-env-reader-first-assignment", Severity: "error",
				File:   "scripts/convex-self-hosted-vps/convex-env.sh",
				Action: types.ActionAskUser,
				Description: "the shared env reader keeps first-assignment-wins semantics, still diverging " +
					"from docker compose env parsing and quoting semantics",
			},
		),
	}

	report := BuildReport(rounds, nil, false, defaultThresholds())

	var recurred *RecurringClass
	for i := range report.Recurring {
		if len(report.Recurring[i].Rounds) >= 3 {
			recurred = &report.Recurring[i]
		}
	}
	if recurred == nil {
		t.Fatalf("expected one class recurring across 3 rounds, got %+v", report.Recurring)
	}
	if got, want := recurred.Rounds, []int{4, 6, 7}; !equalInts(got, want) {
		t.Fatalf("recurring rounds = %v, want %v", got, want)
	}
	if len(recurred.Files) != 3 {
		t.Fatalf("recurring class should span 3 distinct files, got %v", recurred.Files)
	}
	// The unrelated finding must not be folded into the recurring class.
	for _, f := range recurred.Files {
		if f == "scripts/deploy.sh" {
			t.Fatalf("unrelated finding was merged into the recurring class: %v", recurred.Files)
		}
	}
	if report.Warning == "" || !strings.Contains(report.Warning, "recurred") {
		t.Fatalf("recurring class across 3 rounds should trip the guard, got %q", report.Warning)
	}
}

// A fix round that reports findings in files absent from the originally
// submitted diff is expanding the change surface; those findings are counted.
func TestBuildReport_NewSurfaceFindingsCounted(t *testing.T) {
	submitted := []string{"scripts/restore.sh", "scripts/backup.sh"}
	rounds := []*db.StepRound{
		makeRound(t, 1, 10_000,
			finding("scripts/restore.sh", "quoting semantics disagree with docker compose parsing"),
		),
		makeRound(t, 2, 10_000,
			// convex-env.sh did not exist in the submitted diff: the fixer created it.
			finding("scripts/convex-env.sh", "the new shared reader keeps first-assignment-wins ordering"),
			finding("scripts/backup.sh", "backup still reads the raw env file directly"),
		),
	}

	report := BuildReport(rounds, submitted, true, defaultThresholds())

	if report.NewSurfaceFindings == nil {
		t.Fatal("NewSurfaceFindings should be set when the submitted diff is known")
	}
	if *report.NewSurfaceFindings != 1 {
		t.Fatalf("NewSurfaceFindings = %d, want 1", *report.NewSurfaceFindings)
	}
}

func TestBuildReport_BudgetTrips(t *testing.T) {
	th := defaultThresholds()
	th.NonDecreasingRounds = 0
	th.RecurringRounds = 0
	rounds := []*db.StepRound{
		makeRound(t, 1, 90*60*1000, finding("a.go", "nil pointer dereference when the config file is missing")),
		makeRound(t, 2, 40*60*1000, finding("b.go", "unchecked error return from file close in the writer")),
	}

	report := BuildReport(rounds, nil, false, th)
	if report.Warning == "" || !strings.Contains(report.Warning, "budget") {
		t.Fatalf("cumulative review time over budget should trip the guard, got %q", report.Warning)
	}

	under := []*db.StepRound{
		makeRound(t, 1, 30*60*1000, finding("a.go", "nil pointer dereference when the config file is missing")),
	}
	if r := BuildReport(under, nil, false, th); r.Warning != "" {
		t.Fatalf("under-budget run tripped: %q", r.Warning)
	}
}

func TestBuildReport_DisabledThresholdsNeverTrip(t *testing.T) {
	var rounds []*db.StepRound
	for i := 0; i < 7; i++ {
		rounds = append(rounds, makeRound(t, i+1, 60*60*1000,
			finding("same.go", "the exact same finding text repeated every round forever")))
	}
	report := BuildReport(rounds, nil, false, Thresholds{})
	if report.Warning != "" {
		t.Fatalf("disabled thresholds must never trip, got %q", report.Warning)
	}
	// Telemetry is still produced: labeling is independent of the guard.
	if len(report.Recurring) == 0 {
		t.Fatal("recurring classes should still be reported with the guard disabled")
	}
}

// The non-decreasing window is strict: a decrease anywhere inside the trailing
// window means the loop is still making progress.
func TestBuildReport_DecreaseInsideWindowDoesNotTrip(t *testing.T) {
	rounds := []*db.StepRound{
		makeRound(t, 1, 10_000,
			finding("a.go", "nil pointer dereference when the config file is missing"),
			finding("b.go", "unchecked error return from file close in the writer"),
			finding("c.go", "data race on the shared counter in the worker pool"),
		),
		makeRound(t, 2, 10_000,
			finding("d.go", "missing timeout on the outbound webhook client"),
		),
		makeRound(t, 3, 10_000,
			finding("e.go", "partial write left behind when the disk fills during export"),
		),
	}
	report := BuildReport(rounds, nil, false, Thresholds{NonDecreasingRounds: 3})
	if report.Warning != "" {
		t.Fatalf("3,1,1 has a decrease inside the window and must not trip, got %q", report.Warning)
	}

	short := rounds[:2]
	if r := BuildReport(short, nil, false, Thresholds{NonDecreasingRounds: 3}); r.Warning != "" {
		t.Fatalf("fewer rounds than the window must not trip, got %q", r.Warning)
	}
}

func TestParseReportRoundTrip(t *testing.T) {
	n := 2
	in := Report{
		RoundFindings:      []int{1, 1, 2},
		ReviewMS:           5000,
		NewSurfaceFindings: &n,
		Recurring:          []RecurringClass{{Label: "env-parse-semantic", Rounds: []int{1, 3}, Files: []string{"a.sh", "b.sh"}}},
		Warning:            "review loop is not converging: test",
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	out, ok := ParseReport(string(data))
	if !ok || out == nil {
		t.Fatal("ParseReport failed on valid JSON")
	}
	if out.Warning != in.Warning || len(out.Recurring) != 1 || out.Recurring[0].Label != in.Recurring[0].Label {
		t.Fatalf("round trip mismatch: %+v", out)
	}
	if _, ok := ParseReport("not json"); ok {
		t.Fatal("ParseReport should reject invalid JSON")
	}
	if _, ok := ParseReport(""); ok {
		t.Fatal("ParseReport should reject empty input")
	}
}

func TestFormatRoundCounts(t *testing.T) {
	if got := FormatRoundCounts([]int{1, 1, 1, 2, 3, 3, 3}); got != "1,1,1,2,3,3,3" {
		t.Fatalf("FormatRoundCounts = %q", got)
	}
	if got := FormatRoundCounts(nil); got != "" {
		t.Fatalf("FormatRoundCounts(nil) = %q", got)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
