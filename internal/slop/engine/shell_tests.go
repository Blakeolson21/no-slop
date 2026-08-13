package engine

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/Blakeolson21/no-slop/internal/shellenv"
)

// ShellTestRunner executes an explicitly configured command with the same
// descendant-reaping policy as the inherited pipeline.
type ShellTestRunner struct{}

// Run captures output and represents an ordinary nonzero exit in TestResult.
func (ShellTestRunner) Run(ctx context.Context, workDir, command string) (TestResult, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/c", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Dir = workDir
	shellenv.ConfigureShellCommand(cmd)
	output, err := shellenv.CombinedOutputShellCommand(cmd)
	result := TestResult{Command: command, Output: string(output)}
	if err == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return TestResult{}, fmt.Errorf("execute configured test command: %w", err)
}
