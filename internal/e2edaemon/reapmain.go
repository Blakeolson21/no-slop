//go:build ignore

// Command: go run ./internal/e2edaemon/reapmain.go
//
// Suite-wrapper reaper entrypoint. Invoked from scripts/e2e.sh on EXIT/INT/TERM.
// Does not claim to survive SIGKILL of the wrapper shell itself; next-run
// recovery (TestMain + pre-reap) covers that case via the on-disk inventory.
package main

import (
	"fmt"
	"os"

	"github.com/Blakeolson21/no-slop/internal/e2edaemon"
	"github.com/Blakeolson21/no-slop/internal/identity"
)

func main() {
	os.Exit(run())
}

func run() int {
	inventoryDir, dirErr := e2edaemon.DirFromEnv()
	if dirErr != nil {
		fmt.Fprintf(os.Stderr, "e2e-reap: %v\n", dirErr)
		return 2
	}
	reapAbandoned, reapErr := identity.EnvEnabled("NS_E2E_REAP_ABANDONED", "NM_E2E_REAP_ABANDONED")
	if reapErr != nil {
		fmt.Fprintf(os.Stderr, "e2e-reap: %v\n", reapErr)
		return 2
	}
	if reapAbandoned {
		parent, err := identity.LookupEnv("NS_E2E_DAEMON_INVENTORY_PARENT", "NM_E2E_DAEMON_INVENTORY_PARENT")
		if err != nil {
			fmt.Fprintf(os.Stderr, "e2e-reap: %v\n", err)
			return 2
		}
		for _, err := range e2edaemon.ReapAbandoned(parent, inventoryDir) {
			fmt.Fprintf(os.Stderr, "e2e-reap: %v\n", err)
		}
		return 0
	}
	inv, err := e2edaemon.OpenDir(inventoryDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e-reap: open inventory: %v\n", err)
		return 0 // best-effort; never fail the suite wrapper hard
	}
	result := inv.ReapAll()
	if len(result.Errors) > 0 {
		for _, e := range result.Errors {
			fmt.Fprintf(os.Stderr, "e2e-reap: %s\n", e)
		}
	}
	verbose, verboseErr := identity.EnvEnabled("NS_E2E_REAP_VERBOSE", "NM_E2E_REAP_VERBOSE")
	if verboseErr != nil {
		fmt.Fprintf(os.Stderr, "e2e-reap: %v\n", verboseErr)
		return 2
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "e2e-reap: entries=%d stopped=%d killed=%d removed=%d skipped=%d\n",
			result.Entries, result.Stopped, result.Killed, result.Removed, result.Skipped)
	}
	return 0
}
