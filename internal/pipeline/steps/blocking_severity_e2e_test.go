package steps

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/agent"
	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/pipeline"
)

// resolveRepoPolicy walks the exact production chain a run uses to decide the
// blocking floor: parse the pushed branch's .no-slop.yaml, parse the trusted
// default-branch copy, resolve the trust boundary, then merge with the global
// config (internal/daemon/manager.go startRun / runConfig).
func resolveRepoPolicy(t *testing.T, pushedYAML, trustedYAML string) *config.Config {
	t.Helper()
	pushed, err := config.LoadRepoFromBytes([]byte(pushedYAML))
	if err != nil {
		t.Fatalf("parse pushed .no-slop.yaml: %v", err)
	}
	var trusted *config.RepoConfig
	if trustedYAML != "" {
		trusted, err = config.LoadRepoFromBytes([]byte(trustedYAML))
		if err != nil {
			t.Fatalf("parse trusted .no-slop.yaml: %v", err)
		}
	}
	effective := config.EffectiveRepoConfig(pushed, trusted, trusted != nil && trusted.AllowRepoCommands)
	return config.Merge(config.DefaultGlobalConfig(), effective)
}

// TestBlockingSeverity_ReviewGateFromRepoConfig drives a real review step from
// the .no-slop.yaml text an end user writes, so the observable outcome (does
// the run park for a decision?) is proven across the whole chain rather than by
// setting the resolved field directly.
func TestBlockingSeverity_ReviewGateFromRepoConfig(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		pushedYAML    string
		trustedYAML   string
		findings      string
		wantParks     bool
		wantFloor     string
		wantOutcomeAs string
	}{
		{
			name:          "omitted setting keeps warnings blocking",
			pushedYAML:    "agent: claude\n",
			trustedYAML:   "agent: claude\n",
			findings:      `{"findings":[{"severity":"warning","description":"review warning"}]}`,
			wantParks:     true,
			wantFloor:     config.BlockingSeverityWarning,
			wantOutcomeAs: "parked for a decision",
		},
		{
			name:          "trusted error floor makes a warning advisory",
			pushedYAML:    "agent: claude\n",
			trustedYAML:   "blocking_severity: error\n",
			findings:      `{"findings":[{"severity":"warning","description":"review warning"}]}`,
			wantParks:     false,
			wantFloor:     config.BlockingSeverityError,
			wantOutcomeAs: "completed with an advisory finding",
		},
		{
			name:          "trusted error floor still blocks an error",
			pushedYAML:    "agent: claude\n",
			trustedYAML:   "blocking_severity: error\n",
			findings:      `{"findings":[{"severity":"error","description":"review error"}]}`,
			wantParks:     true,
			wantFloor:     config.BlockingSeverityError,
			wantOutcomeAs: "parked for a decision",
		},
		{
			name:          "feature branch cannot weaken its own gate",
			pushedYAML:    "blocking_severity: error\n",
			trustedYAML:   "agent: claude\n",
			findings:      `{"findings":[{"severity":"warning","description":"review warning"}]}`,
			wantParks:     true,
			wantFloor:     config.BlockingSeverityWarning,
			wantOutcomeAs: "parked for a decision",
		},
		{
			name:          "trusted error floor still fails closed on an unclassified severity",
			pushedYAML:    "agent: claude\n",
			trustedYAML:   "blocking_severity: error\n",
			findings:      `{"findings":[{"description":"finding with no severity"}]}`,
			wantParks:     true,
			wantFloor:     config.BlockingSeverityError,
			wantOutcomeAs: "parked for a decision",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, baseSHA, headSHA := setupGitRepo(t)
			ag := &mockAgent{
				name: "test",
				runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
					return &agent.Result{Output: json.RawMessage(tc.findings)}, nil
				},
			}
			sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
			sctx.Config = resolveRepoPolicy(t, tc.pushedYAML, tc.trustedYAML)

			if sctx.Config.BlockingSeverity != tc.wantFloor {
				t.Fatalf("resolved blocking_severity = %q, want %q", sctx.Config.BlockingSeverity, tc.wantFloor)
			}

			outcome, err := (&ReviewStep{}).Execute(sctx)
			if err != nil {
				t.Fatal(err)
			}
			if outcome.NeedsApproval != tc.wantParks {
				t.Fatalf("review NeedsApproval = %v, want %v (%s)", outcome.NeedsApproval, tc.wantParks, tc.wantOutcomeAs)
			}
			t.Logf(".no-slop.yaml (trusted default branch): %q | pushed branch: %q -> resolved floor %q -> review %s",
				tc.trustedYAML, tc.pushedYAML, sctx.Config.BlockingSeverity, tc.wantOutcomeAs)
		})
	}
}

// TestBlockingSeverity_UnsupportedValueIsRejectedAtLoad proves the operator
// sees a parse-time rejection rather than a silently ignored key.
func TestBlockingSeverity_UnsupportedValueIsRejectedAtLoad(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"info", "critical", "none", "Error"} {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			if _, err := config.LoadRepoFromBytes([]byte("blocking_severity: " + value + "\n")); err == nil {
				t.Fatalf("blocking_severity: %s was accepted; want a parse-time rejection", value)
			} else {
				t.Logf("blocking_severity: %s -> %v", value, err)
			}
		})
	}
}

// TestBlockingSeverity_HousekeepingLintHonorsTheFloor covers the fourth call
// site end to end: the lint outcome that comes from the combined document+lint
// housekeeping pass rather than lint's own agent pass.
func TestBlockingSeverity_HousekeepingLintHonorsTheFloor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		trustedYAML string
		severity    string
		wantParks   bool
	}{
		{name: "default floor parks a warning", trustedYAML: "agent: claude\n", severity: "warning", wantParks: true},
		{name: "error floor makes a warning advisory", trustedYAML: "blocking_severity: error\n", severity: "warning", wantParks: false},
		{name: "error floor parks an error", trustedYAML: "blocking_severity: error\n", severity: "error", wantParks: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, baseSHA, headSHA := setupGitRepo(t)
			sctx := newHousekeepingContext(t, &mockAgent{name: "test"}, dir, baseSHA, headSHA, config.Commands{})
			sctx.Config = resolveRepoPolicy(t, "agent: claude\n", tc.trustedYAML)

			findingsJSON, err := json.Marshal(Findings{Items: []Finding{{Severity: tc.severity, Description: "housekeeping lint finding"}}})
			if err != nil {
				t.Fatal(err)
			}
			sctx.Shared.SetHousekeepingLint(pipeline.HousekeepingLintResult{FindingsJSON: string(findingsJSON), Summary: "housekeeping"})

			outcome, err := (&LintStep{}).Execute(sctx)
			if err != nil {
				t.Fatal(err)
			}
			if outcome.NeedsApproval != tc.wantParks {
				t.Fatalf("housekeeping lint NeedsApproval = %v for %q finding at floor %q, want %v",
					outcome.NeedsApproval, tc.severity, sctx.Config.BlockingSeverity, tc.wantParks)
			}
		})
	}
}
