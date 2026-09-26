package agent

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// FixBudgetRefusalError identifies MO's exhausted-budget response. The wrapped
// process error is preserved so this classification only adds observability;
// it does not change retry, fallback, or gate decisions.
type FixBudgetRefusalError struct {
	Limit int
	cause error
}

func (e *FixBudgetRefusalError) Error() string { return e.cause.Error() }
func (e *FixBudgetRefusalError) Unwrap() error { return e.cause }

// FixBudgetExhaustedReason reconstructs the bounded reason without retaining
// any wrapper stderr in invocation telemetry.
func FixBudgetExhaustedReason(limit int) string {
	return fmt.Sprintf("fix budget exhausted (MO_GATE_FIX_ROUNDS=%d)", limit)
}

// classifyFixBudgetRefusal reads only stderr from a failed native process,
// never agent-authored stdout, prompts, or arbitrary wrapped error text.
// MO #677's contract is exit 3 plus this exact, complete stderr line.
func classifyFixBudgetRefusal(err error, stderr string) error {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		return err
	}
	const prefix = "GATE_RUNNER_ERROR refused: fix budget exhausted (MO_GATE_FIX_ROUNDS="
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, ")") {
			continue
		}
		value := strings.TrimSuffix(strings.TrimPrefix(line, prefix), ")")
		limit, parseErr := strconv.Atoi(value)
		if parseErr == nil && limit >= 0 && strconv.Itoa(limit) == value {
			return &FixBudgetRefusalError{Limit: limit, cause: err}
		}
	}
	return err
}
