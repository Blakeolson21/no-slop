package cli

import (
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// The Mac gate guard answers "would these arguments start a run?" through
// IsAXIRunInvocation, before the CLI itself has parsed anything. The only way
// that answer can be wrong is by disagreeing with the dispatcher it stands in
// for, which is exactly how a leading root flag once walked past the guard and
// into the run path. So the assertion here is agreement: for each argument
// vector, ask the guard, then let the real cobra tree dispatch it and see
// whether control actually reached the `axi run` command.
func TestIsAXIRunInvocationAgreesWithCobraDispatch(t *testing.T) {
	argVectors := [][]string{
		{"axi", "run"},
		{"axi", "run", "--intent", "ship the thing"},
		{"axi", "run", "--yes", "--intent", "x"},
		{"axi", "run", "--skip", "lint", "--intent", "x"},
		{"--skip", "lint", "axi", "run", "--intent", "x"},
		{"--skip=lint", "axi", "run"},
		{"--yes", "axi", "run"},
		{"-y", "axi", "run"},
		{"--skip", "lint", "--yes", "axi", "run"},
		{"axi"},
		{"axi", "status"},
		{"axi", "respond"},
		{"help", "axi", "run"},
		{"rerun"},
		{"daemon", "run"},
		{"status"},
	}

	for _, args := range argVectors {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			guard := IsAXIRunInvocation(args)
			dispatched := dispatchReachesAXIRun(t, args)
			if guard != dispatched {
				t.Fatalf("IsAXIRunInvocation(%q) = %v, but cobra dispatch reached axi run = %v", args, guard, dispatched)
			}
		})
	}
}

// dispatchReachesAXIRun runs the real root command over args and reports
// whether execution reached the `axi run` command body. Every command body in
// the tree is swapped for a stub so no command ever does real work, and the
// environment-dependent persistent pre-run guard is cleared so this measures
// argument dispatch alone.
func dispatchReachesAXIRun(t *testing.T, args []string) bool {
	t.Helper()

	root := newRootCmd()
	root.PersistentPreRunE = nil
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs(args)

	axiRun := findCommandPath(t, root, "axi", "run")
	reached := false
	stubBodies(root, axiRun, &reached)

	_ = root.Execute()
	return reached
}

func stubBodies(cmd, target *cobra.Command, reached *bool) {
	cmd.Run = nil
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		if c == target {
			*reached = true
		}
		return nil
	}
	for _, child := range cmd.Commands() {
		stubBodies(child, target, reached)
	}
}

func findCommandPath(t *testing.T, root *cobra.Command, names ...string) *cobra.Command {
	t.Helper()

	cmd := root
	for _, name := range names {
		var next *cobra.Command
		for _, child := range cmd.Commands() {
			if child.Name() == name {
				next = child
				break
			}
		}
		if next == nil {
			t.Fatalf("command %q not found under %q", name, cmd.CommandPath())
		}
		cmd = next
	}
	return cmd
}
