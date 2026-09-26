//go:build !windows

package gatecontext

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Blakeolson21/no-slop/internal/shellenv"
)

// One snapshot avoids a process launch per ancestor on busy machines.
func processParents(ctx context.Context) (map[int]int, error) {
	cmd := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=,ppid=")
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	shellenv.ConfigureShellCommand(cmd)
	out, err := shellenv.OutputShellCommand(cmd)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		return nil, err
	}
	parents := make(map[int]int)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid process ancestry row")
		}
		pid, pidErr := strconv.Atoi(fields[0])
		ppid, parentErr := strconv.Atoi(fields[1])
		if pidErr != nil || parentErr != nil || pid < 0 || ppid < 0 {
			return nil, fmt.Errorf("invalid process ancestry identifiers")
		}
		parents[pid] = ppid
	}
	if len(parents) == 0 {
		return nil, fmt.Errorf("empty process ancestry snapshot")
	}
	return parents, nil
}
