package cli

import (
	"encoding/json"
	"fmt"

	"github.com/Blakeolson21/no-slop/internal/daemon"
	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/paths"
	"github.com/spf13/cobra"
)

func newStoreCmd() *cobra.Command {
	root := &cobra.Command{Use: "store", Short: "Inspect and repair persisted pipeline state"}
	var apply, dry bool
	var backup string
	repair := &cobra.Command{Use: "repair-phantoms", Short: "Fail unowned running rows idle strictly more than six hours (dry-run by default)", RunE: func(cmd *cobra.Command, _ []string) error {
		if apply && dry {
			return fmt.Errorf("--apply and --dry-run are mutually exclusive")
		}
		p, err := paths.New()
		if err != nil {
			return err
		}
		var out *db.PhantomRepairReceipt
		client, err := ipc.Dial(p.Socket())
		if err == nil {
			defer client.Close()
			out = &db.PhantomRepairReceipt{}
			err = client.Call(ipc.MethodRepairPhantoms, &ipc.RepairPhantomsParams{Apply: apply, BackupPath: backup}, out)
			if err != nil {
				return fmt.Errorf("daemon could not repair store (an older daemon requires an owner-coordinated upgrade; no offline bypass): %w", err)
			}
		} else {
			out, err = daemon.RepairPhantomsOffline(cmd.Context(), p, apply, backup)
		}
		if out != nil {
			if printErr := json.NewEncoder(cmd.OutOrStdout()).Encode(out); printErr != nil {
				return printErr
			}
		}
		return err
	}}
	repair.Flags().BoolVar(&apply, "apply", false, "Apply the transaction after saving a SQLite snapshot")
	repair.Flags().BoolVar(&dry, "dry-run", false, "Inspect candidates and protected rows without changing the store")
	repair.Flags().StringVar(&backup, "backup", "", "New absolute snapshot path (default: app-root/repairs)")
	root.AddCommand(repair)
	return root
}
