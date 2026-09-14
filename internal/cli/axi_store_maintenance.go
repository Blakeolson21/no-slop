package cli

import (
	"fmt"
	"strings"

	toon "github.com/toon-format/toon-go"

	"github.com/Blakeolson21/no-slop/internal/types"
	"github.com/spf13/cobra"
)

// newAxiMigrateStatusNamesCmd implements `axi migrate-status-names`: the
// one-release backfill for the 2026-09 gate-state rename (fix_review ->
// parked_for_responder_after_fix, awaiting_approval ->
// parked_for_responder_approval, fixing -> fixer_running, runs.status pending
// -> run_starting). Idempotent, safe to run while the daemon is up (two
// single-statement UPDATEs in one transaction), and prints the row counts it
// changed. The coordinator runs this against host stores after deploy; it is
// never run automatically.
func newAxiMigrateStatusNamesCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "migrate-status-names",
		Short:         "Backfill renamed gate-state tokens in the store (idempotent)",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return trackAxiSurface("axi-migrate-status-names", "axi/migrate-status-names", nil, func() error {
				env, err := openAxiEnv(false)
				if err != nil {
					return emitError(cmd, 1, err.Error())
				}
				defer env.close()

				runsUpdated, stepsUpdated, err := env.d.MigrateStatusNames()
				if err != nil {
					return emitError(cmd, 1, err.Error())
				}
				emitDoc(cmd,
					toon.Field{Key: "migrated", Value: toon.NewObject(
						toon.Field{Key: "runs_updated", Value: runsUpdated},
						toon.Field{Key: "step_results_updated", Value: stepsUpdated},
					)},
					toon.Field{Key: "note", Value: "idempotent: re-running reports 0 rows once no legacy literals remain"},
				)
				return nil
			})
		},
	}
}

// newAxiLintStoreCmd implements `axi lint-store`: the same DISTINCT status
// check the daemon startup validator runs, exposed so an operator can check a
// store without starting the daemon. Prints every offending value; exits
// non-zero when the store is not clean.
func newAxiLintStoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "lint-store",
		Short:         "Check stored runs.status / step_results.status values against the declared legal sets",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return trackAxiSurface("axi-lint-store", "axi/lint-store", nil, func() error {
				env, err := openAxiEnv(false)
				if err != nil {
					return emitError(cmd, 1, err.Error())
				}
				defer env.close()

				if err := env.d.ValidateStatusNames(); err != nil {
					return emitError(cmd, 1, err.Error(),
						fmt.Sprintf("run `no-slop axi migrate-status-names` to rewrite legacy literals, or fix the named values by hand"),
					)
				}
				emitDoc(cmd,
					toon.Field{Key: "store", Value: "ok"},
					toon.Field{Key: "legal_runs_status", Value: types.LegalRunStatusNames()},
					toon.Field{Key: "legal_step_status", Value: types.LegalStepStatusNames()},
				)
				return nil
			})
		},
	}
}

// offendingStatusValues renders the offending values named by the validator in
// a stable order for tests.
func offendingStatusValues(err error) []string {
	if err == nil {
		return nil
	}
	msg := err.Error()
	start := strings.Index(msg, "[")
	end := strings.Index(msg, "]")
	if start < 0 || end < start {
		return nil
	}
	body := strings.Trim(msg[start+1:end], " ")
	if body == "" {
		return nil
	}
	return strings.Split(body, " ")
}
