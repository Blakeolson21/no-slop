package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/Blakeolson21/no-slop/internal/cli"
	"github.com/Blakeolson21/no-slop/internal/daemon"
	"github.com/Blakeolson21/no-slop/internal/paths"
	pipelinesteps "github.com/Blakeolson21/no-slop/internal/pipeline/steps"
	"github.com/Blakeolson21/no-slop/internal/telemetry"
	"github.com/Blakeolson21/no-slop/internal/update"
)

func main() {
	os.Exit(run())
}

func run() int {
	if err := validateIdentityAliasConflicts(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if root, ok, err := daemonLogSinkRootFromArgs(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	} else if ok {
		if err := setStateRootEnv(root); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := daemon.RunBootstrapLogSink(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	if root, ok, err := daemonRunRootFromArgs(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	} else if ok {
		if root != "" {
			if err := setStateRootEnv(root); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
		}
		if err := daemon.Run(); err != nil {
			writeDaemonRunError(os.Stderr, err)
			return 1
		}
		return 0
	}

	if handled, err := update.MaybeHandleBackgroundCheck(os.Args[1:]); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}

	if err := update.MaybeNotifyAndCheck(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(cliLogWriter(), nil)))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		defer cancel()
		_ = telemetry.Close(ctx)
	}()

	return cli.Execute()
}

func validateIdentityAliasConflicts() error {
	if err := telemetry.ValidateDefaultConfig(); err != nil {
		return err
	}
	if err := update.ValidateEnv(); err != nil {
		return err
	}
	if err := daemon.ValidateControlEnv(); err != nil {
		return err
	}
	return pipelinesteps.ValidateDemoModeConfig()
}

func setStateRootEnv(root string) error {
	if err := os.Setenv("NS_HOME", root); err != nil {
		return err
	}
	return os.Setenv("NM_HOME", root)
}

func writeDaemonRunError(stderr *os.File, err error) {
	if errors.Is(err, daemon.ErrSingletonLockHeld) {
		p, pathErr := paths.New()
		if pathErr == nil {
			stderrInfo, stderrErr := stderr.Stat()
			bootstrapInfo, bootstrapErr := os.Stat(p.DaemonBootstrapLog())
			if stderrErr == nil && bootstrapErr == nil && os.SameFile(stderrInfo, bootstrapInfo) {
				return
			}
		}
	}
	fmt.Fprintln(stderr, err)
}

func daemonLogSinkRootFromArgs(args []string) (string, bool, error) {
	if len(args) != 4 || args[0] != "daemon" || args[1] != "log-sink" || args[2] != "--root" {
		return "", false, nil
	}
	if args[3] == "" {
		return "", false, fmt.Errorf("empty value for --root")
	}
	return args[3], true, nil
}

func daemonRunRootFromArgs(args []string) (string, bool, error) {
	if len(args) < 2 || args[0] != "daemon" || args[1] != "run" {
		return "", false, nil
	}
	if len(args) == 2 {
		return "", true, nil
	}
	if len(args) == 3 {
		arg := args[2]
		if arg == "--help" || arg == "-h" {
			return "", false, nil
		}
		if arg == "--root" {
			return "", false, fmt.Errorf("missing value for --root")
		}
		if value, ok := strings.CutPrefix(arg, "--root="); ok {
			return value, true, nil
		}
		return "", false, nil
	}
	if len(args) == 4 && args[2] == "--root" {
		return args[3], true, nil
	}
	return "", false, nil
}

func cliLogWriter() io.Writer {
	p, err := paths.New()
	if err != nil {
		return io.Discard
	}
	f, err := os.OpenFile(p.CLILog(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return io.Discard
	}
	return f
}
