package cli

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/paths"
)

// TestStatsAgentsReportsLocalPerformanceTelemetry proves the read-only
// report surface exposes the locally persisted invocation evidence: per-
// purpose aggregates via --agents and per-run detail (including accumulated
// parked time) via --run.
func TestStatsAgentsReportsLocalPerformanceTelemetry(t *testing.T) {
	nmHome := t.TempDir()
	t.Setenv("NS_HOME", nmHome)
	p := paths.WithRoot(nmHome)

	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	repo, err := d.InsertRepoWithID("repo-1", "/tmp/repo", "https://github.com/test/repo", "main")
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.InsertRun(repo.ID, "feature/x", "abc", "def")
	if err != nil {
		t.Fatal(err)
	}
	seed := []db.AgentInvocation{
		{RunID: run.ID, StepName: "review", Round: 1, Purpose: "review", Agent: "codex", ResolvedExecutable: strPtrCLI("/usr/bin/codex"), Model: strPtrCLI("gpt-5.2"), ModelProvider: strPtrCLI("openai"), ModelArgs: []string{"-m", "gpt-5.2"}, SessionMode: db.InvocationModeStarted, SessionKey: "deadbeef00000000", StartedAt: 1, CompletedAt: 2, DurationMS: 60_000, ExitStatus: "ok", InputTokens: 100, OutputTokens: 10, CacheReadTokens: 40, CacheCreationTokens: statsIntPtr(20)},
		{RunID: run.ID, StepName: "review", Round: 2, Purpose: "review", Agent: "codex", Model: strPtrCLI("gpt-5.2"), SessionMode: db.InvocationModeResumed, SessionKey: "deadbeef00000000", StartedAt: 3, CompletedAt: 4, DurationMS: 30_000, ExitStatus: "ok", InputTokens: 50, OutputTokens: 5, CacheReadTokens: 45, CacheCreationTokens: statsIntPtr(25)},
		{RunID: run.ID, StepName: "review", Round: 2, Purpose: "review-fix", Agent: "codex", Model: strPtrCLI("gpt-5.2"), SessionMode: db.InvocationModeStarted, SessionKey: "feedface00000000", StartedAt: 5, CompletedAt: 6, DurationMS: 45_000, ExitStatus: "ok"},
		{RunID: run.ID, StepName: "review", Round: 3, Purpose: "review-fix", Agent: "codex", Model: strPtrCLI("gpt-5.2"), SessionMode: db.InvocationModeCold, StartedAt: 7, CompletedAt: 8, DurationMS: 1_000, ExitStatus: "error", FailureCategory: "quota"},
	}
	for _, inv := range seed {
		if _, err := d.InsertAgentInvocation(inv); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.AddRunParkedDuration(run.ID, 90_000); err != nil {
		t.Fatal(err)
	}
	d.Close()

	out, err := executeCmd("stats", "--agents")
	if err != nil {
		t.Fatalf("stats --agents: %v\n%s", err, out)
	}
	for _, want := range []string{"PURPOSE", "review", "review-fix", "RESUMED", "QUOTA", "CACHE WRITE TOK", "45"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stats --agents missing %q in:\n%s", want, out)
		}
	}

	out, err = executeCmd("stats", "--run", run.ID)
	if err != nil {
		t.Fatalf("stats --run: %v\n%s", err, out)
	}
	for _, want := range []string{run.ID, "parked at gates 1m30s total", "resumed", "deadbeef00000000", "gpt-5.2", "EXECUTABLE", "/usr/bin/codex", "PROVIDER", "openai", "MODEL ARGS", `["-m","gpt-5.2"]`, "CACHE WR", "20", "error/quota"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stats --run missing %q in:\n%s", want, out)
		}
	}
	// The seeded rows carry no activity metrics, so those fields render as the
	// unknown marker, distinct from a recorded zero.
	if !strings.Contains(out, "-") {
		t.Fatalf("stats --run should render unknown metric fields as \"-\":\n%s", out)
	}
	// Every rendered table must carry a value for each header column: a format
	// string that drifts out of step with its arguments as columns are added
	// silently corrupts the report with a printf error marker instead of failing.
	assertReportColumnsAreComplete(t, out, runReportHeaders)
}

func statsIntPtr(v int) *int       { return &v }
func statsInt64Ptr(v int64) *int64 { return &v }

var reportColumnGap = regexp.MustCompile(`\s{2,}`)

// reportTables parses the rendered report into its tabwriter tables. Tables are
// blank-line separated blocks whose first line splits into more than one padded
// column, which is what separates them from the report's single-column prose.
func reportTables(out string) [][][]string {
	var tables [][][]string
	for _, block := range strings.Split(out, "\n\n") {
		var rows [][]string
		for _, line := range strings.Split(block, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			rows = append(rows, reportColumnGap.Split(strings.TrimRight(line, " "), -1))
		}
		if len(rows) == 0 || len(rows[0]) < 2 {
			continue
		}
		tables = append(tables, rows)
	}
	return tables
}

// assertReportColumnsAreComplete asserts the rendered report carries no printf
// formatting error and that its tables are the expected ones, pinned by their
// exact header columns. Pinning the headers is what keeps the guard honest: a
// detection heuristic that stops matching would otherwise make it silently
// vacuous rather than failing.
//
// Data rows are checked only for excess columns. A tabwriter renders an empty
// cell as pure padding, which merges with the neighbouring column gap, so a row
// with a legitimately empty cell parses as fewer columns than its header; a
// missing or extra argument instead surfaces as the "%!" marker checked above.
func assertReportColumnsAreComplete(t *testing.T, out string, wantHeaders [][]string) {
	t.Helper()
	if strings.Contains(out, "%!") {
		t.Fatalf("report contains a printf formatting error:\n%s", out)
	}
	tables := reportTables(out)
	if len(tables) != len(wantHeaders) {
		t.Fatalf("parsed %d tables, want %d:\n%s", len(tables), len(wantHeaders), out)
	}
	for i, want := range wantHeaders {
		header := tables[i][0]
		if !reflect.DeepEqual(header, want) {
			t.Fatalf("table %d header = %#v, want %#v", i, header, want)
		}
		for _, row := range tables[i][1:] {
			if len(row) > len(header) {
				t.Fatalf("table %d row has %d columns but header has %d:\nrow: %#v\nfull report:\n%s",
					i, len(row), len(header), row, out)
			}
		}
	}
}

// runReportHeaders is the column contract of `stats --run`: the identity
// columns this change added (EXECUTABLE, MODEL, PROVIDER, MODEL ARGS) are part
// of the report's public shape.
var runReportHeaders = [][]string{
	{"STEP", "ROUND", "PURPOSE", "AGENT", "EXECUTABLE", "MODEL", "PROVIDER", "MODEL ARGS", "SESSION", "KEY",
		"DURATION", "MODEL TIME", "SUBPROC", "RT", "TOOLS (w/t/e/r/g/o)", "FIND", "WORK (f/l)", "FALLBACK", "EXIT"},
	{"STEP", "ROUND", "PURPOSE", "SESSION", "Δ IN (round)", "Δ OUT", "Δ CACHE RD", "IN (raw)", "OUT (raw)",
		"CACHE RD (raw)", "CACHE WR", "FRESH IN", "REASON"},
}

// TestStatsRendersPopulatedFidelityMetrics proves the report surfaces the new
// activity histogram, subprocess/model time split, and per-round token deltas
// when they are recorded.
func TestStatsRendersPopulatedFidelityMetrics(t *testing.T) {
	nmHome := t.TempDir()
	t.Setenv("NS_HOME", nmHome)
	p := paths.WithRoot(nmHome)

	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	repo, err := d.InsertRepoWithID("repo-1", "/tmp/repo", "https://github.com/test/repo", "main")
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.InsertRun(repo.ID, "feature/x", "abc", "def")
	if err != nil {
		t.Fatal(err)
	}
	inv := db.AgentInvocation{
		RunID: run.ID, StepName: "review", Round: 2, Purpose: "review-fix", Agent: "codex",
		Model: strPtrCLI("gpt-5.6-sol"), ModelProvider: strPtrCLI("openai"),
		SessionMode: db.InvocationModeResumed, SessionKey: "deadbeef00000000",
		StartedAt: 1, CompletedAt: 2, DurationMS: 10_000, SubprocessWaitMS: statsInt64Ptr(2_000),
		ExitStatus: "ok", InputTokens: 2500, OutputTokens: 250, CacheReadTokens: 1800,
		FreshInputTokens: statsIntPtr(700), ReasoningTokens: statsIntPtr(9),
		DeltaInputTokens: statsIntPtr(1500), DeltaOutputTokens: statsIntPtr(150), DeltaCacheReadTokens: statsIntPtr(1200),
		ModelRoundtrips: statsIntPtr(24), ToolCalls: statsIntPtr(7),
		ToolWaitCalls: statsIntPtr(0), ToolTestLintCalls: statsIntPtr(2), ToolEditCalls: statsIntPtr(3),
		ToolReadCalls: statsIntPtr(1), ToolGitCalls: statsIntPtr(1), ToolOtherCalls: statsIntPtr(0),
		WorkloadFiles: statsIntPtr(12), WorkloadLines: statsIntPtr(1060), FindingCount: statsIntPtr(3),
	}
	if _, err := d.InsertAgentInvocation(inv); err != nil {
		t.Fatal(err)
	}
	d.Close()

	out, err := executeCmd("stats", "--agents")
	if err != nil {
		t.Fatalf("stats --agents: %v\n%s", err, out)
	}
	for _, want := range []string{"ROUNDTRIPS", "TEST/LINT", "SUBPROC", "24", "METRICS", "1/1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stats --agents missing %q in:\n%s", want, out)
		}
	}

	out, err = executeCmd("stats", "--run", run.ID)
	if err != nil {
		t.Fatalf("stats --run: %v\n%s", err, out)
	}
	// Per-round delta (1500) is shown distinctly from the raw cumulative (2500),
	// the tool histogram and the workload render, and the model-time split appears.
	for _, want := range []string{"Δ IN (round)", "1500", "2500", "7 0/2/3/1/1/0", "12/1060", "MODEL"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stats --run missing %q in:\n%s", want, out)
		}
	}
	assertReportColumnsAreComplete(t, out, runReportHeaders)
}

func strPtrCLI(s string) *string { return &s }
