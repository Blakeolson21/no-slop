package cli

import (
	"fmt"

	toon "github.com/toon-format/toon-go"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/paths"
	"github.com/spf13/cobra"
)

// Store-lifecycle commands (`axi lint-store`, `axi migrate-park-markers`)
// inspect and repair the shared runs store for invariant violations that the
// execution-time write paths no longer produce: a finished run (any
// non-pending/running status) must never still carry an awaiting-agent park
// marker. The check is stated as NOT IN (pending, running) rather than an
// allowlist of terminal statuses so an unrecognized or legacy status value
// fails visible instead of silently escaping the lint.

func newAxiLintStoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lint-store",
		Short: "Report shared-store rows that violate run lifecycle invariants",
		Long: "Read-only check of the shared runs store. Reports every run row\n" +
			"whose status is no longer active (pending/running) but which still\n" +
			"carries an awaiting_agent_since park marker: any reader joining on\n" +
			"the marker would read a finished run as parked. Exits nonzero when\n" +
			"a violation is found.\n\n" +
			"Run `no-slop axi migrate-park-markers` to repair (idempotent).",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return trackReadSurface("axi-lint-store", nil, func() (string, string, error) {
				return "", "", runAxiLintStore(cmd)
			})
		},
	}
	return cmd
}

func runAxiLintStore(cmd *cobra.Command) error {
	p, err := paths.New()
	if err != nil {
		return emitError(cmd, 1, fmt.Sprintf("resolve paths: %v", err))
	}
	d, err := db.OpenReadOnly(p.DB())
	if err != nil {
		return emitError(cmd, 1, fmt.Sprintf("open store: %v", err))
	}
	defer d.Close()

	stale, err := d.StaleParkMarkers()
	if err != nil {
		return emitError(cmd, 1, fmt.Sprintf("lint store: %v", err))
	}

	fields := []toon.Field{
		{Key: "store", Value: collapseHome(p.DB())},
		{Key: "stale_park_markers", Value: len(stale)},
	}
	if len(stale) > 0 {
		rows := make([]staleParkRow, 0, len(stale))
		for _, s := range stale {
			rows = append(rows, staleParkRow{ID: s.ID, Status: string(s.Status), Branch: s.Branch, AwaitingAgentSince: s.AwaitingAgentSince})
		}
		fields = append(fields,
			toon.Field{Key: "runs", Value: rows},
			toon.Field{Key: "help", Value: []string{"Run `no-slop axi migrate-park-markers` to clear the stale markers (idempotent)"}},
		)
		emitDoc(cmd, fields...)
		return &exitError{code: 1}
	}
	emitDoc(cmd, fields...)
	return nil
}

func newAxiMigrateParkMarkersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate-park-markers",
		Short: "Clear stale awaiting-agent park markers on finished runs",
		Long: "One-statement idempotent repair: every run row whose status is no\n" +
			"longer active (pending/running) but which still carries an\n" +
			"awaiting_agent_since park marker has the marker nulled and the\n" +
			"parked time folded into parked_ms. Prints the repaired count; a\n" +
			"second run prints 0.",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return trackAxiSurface("axi-migrate-park-markers", "/axi/migrate-park-markers", nil, func() error {
				return runAxiMigrateParkMarkers(cmd)
			})
		},
	}
	return cmd
}

func runAxiMigrateParkMarkers(cmd *cobra.Command) error {
	p, err := paths.New()
	if err != nil {
		return emitError(cmd, 1, fmt.Sprintf("resolve paths: %v", err))
	}
	d, err := db.Open(p.DB())
	if err != nil {
		return emitError(cmd, 1, fmt.Sprintf("open store: %v", err))
	}
	defer d.Close()

	repaired, err := d.RepairParkMarkers()
	if err != nil {
		return emitError(cmd, 1, fmt.Sprintf("repair park markers: %v", err))
	}
	emitDoc(cmd,
		toon.Field{Key: "repaired", Value: repaired},
	)
	return nil
}

// staleParkRow renders one violating row for `axi lint-store`.
type staleParkRow struct {
	ID                 string `json:"id"`
	Status             string `json:"status"`
	Branch             string `json:"branch"`
	AwaitingAgentSince int64  `json:"awaiting_agent_since"`
}
