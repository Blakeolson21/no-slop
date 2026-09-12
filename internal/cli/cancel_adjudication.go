package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// The verification key is store/operator configuration, never a CLI argument or
// candidate-repository setting. Lanes can submit a signed record, not mint one.
func newAxiCancelAdjudicationCmd() *cobra.Command {
	var record, reason string
	cmd := &cobra.Command{Use: "cancel-adjudication", Short: "Register an independently signed one-dispatch cancel-budget adjudication", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("cancel budget: explicit dispatch reason required")
			}
			p, d, err := openResources()
			if err != nil {
				return err
			}
			defer d.Close()
			// The key's identity, not merely its location, has to be operator
			// authority: the store root is caller-selected and lane-writable.
			authority, err := resolveCancelBudgetKey(p.Root())
			if err != nil {
				return err
			}
			raw, err := os.ReadFile(record)
			if err != nil {
				return err
			}
			id, err := d.RegisterCancelAdjudication(raw, reason, authority.Key)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "cancel_adjudication: %s\n", id)
			fmt.Fprintf(cmd.OutOrStdout(), "adjudicator_key_authority: %s\n", authority.Bound)
			return nil
		}}
	cmd.Flags().StringVar(&record, "record", "", "signed record file")
	cmd.Flags().StringVar(&reason, "reason", "", "dispatch reason, exactly matching the signed record")
	_ = cmd.MarkFlagRequired("record")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}
