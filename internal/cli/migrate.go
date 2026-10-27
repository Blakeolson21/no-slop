package cli

import (
	"encoding/json"
	"fmt"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/gate"
	"github.com/Blakeolson21/no-slop/internal/paths"
	"github.com/spf13/cobra"
)

func newMigrateCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "migrate --dry-run",
		Short: "Plan upstream object-store migration without changing gate data",
		Long:  "Inspect registered gate refs and live run commits and print a JSON migration plan.\nOnly --dry-run is supported. No objects, refs, configuration, or registrations are changed.\nThis is an advisory snapshot; applying a migration requires a fresh verification with writers stopped.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !dryRun {
				return fmt.Errorf("only --dry-run is supported; migration execution is not available")
			}
			p, err := paths.New()
			if err != nil {
				return err
			}
			d, err := db.OpenReadOnly(p.DB())
			if err != nil {
				return fmt.Errorf("open registry read-only: %w", err)
			}
			defer d.Close()
			plan, err := gate.PlanMigration(cmd.Context(), d, p)
			if err != nil {
				return err
			}
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(plan); err != nil {
				return err
			}
			if !plan.Verified {
				return &exitError{code: 1, err: fmt.Errorf("migration source verification failed; inspect the report")}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report planned object/ref imports and verify live runs without making changes")
	return cmd
}
