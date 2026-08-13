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
	reapAbandoned, reapErr := identity.EnvEnabled("NS_E2E_REAP_ABANDONED", "NM_E2E_REAP_ABANDONED")
	if reapErr != nil {
		fmt.Fprintf(os.Stderr, "e2e-reap: %v\n", reapErr)
		return
	}
	if reapAbandoned {
		parent, err := identity.LookupEnv("NS_E2E_DAEMON_INVENTORY_PARENT", "NM_E2E_DAEMON_INVENTORY_PARENT")
		if err != nil {
			fmt.Fprintf(os.Stderr, "e2e-reap: %v\n", err)
			return
		}
		for _, err := range e2edaemon.ReapAbandoned(parent, e2edaemon.DirFromEnv()) {
			fmt.Fprintf(os.Stderr, "e2e-reap: %v\n", err)
		}
		return
	}
	inv, err := e2edaemon.Open()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e-reap: open inventory: %v\n", err)
		os.Exit(0) // best-effort; never fail the suite wrapper hard
	}
	result := inv.ReapAll()
	if len(result.Errors) > 0 {
		for _, e := range result.Errors {
			fmt.Fprintf(os.Stderr, "e2e-reap: %s\n", e)
		}
	}
	verbose, _ := identity.EnvEnabled("NS_E2E_REAP_VERBOSE", "NM_E2E_REAP_VERBOSE")
	if verbose {
		fmt.Fprintf(os.Stderr, "e2e-reap: entries=%d stopped=%d killed=%d removed=%d skipped=%d\n",
			result.Entries, result.Stopped, result.Killed, result.Removed, result.Skipped)
	}
}
