package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/gatecontext"
	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/paths"
)

func TestGateContextTimeoutIsRetryableBeforeRecovery(t *testing.T) {
	testRecoveryClassificationFailure(t, func(context.Context, json.RawMessage) (interface{}, error) {
		time.Sleep(200 * time.Millisecond)
		return &ipc.GateContextResult{}, nil
	}, 100*time.Millisecond, 75)
}

func TestGateContextServerTimeoutIsRetryableBeforeRecovery(t *testing.T) {
	testRecoveryClassificationFailure(t, func(context.Context, json.RawMessage) (interface{}, error) {
		return nil, errors.New(gatecontext.TimeoutErrorCode + ": context deadline exceeded")
	}, time.Second, 75)
}

func TestGateContextOtherFailuresDoNotBecomeRetryableTimeouts(t *testing.T) {
	t.Run("classifier error", func(t *testing.T) {
		testRecoveryClassificationFailure(t, func(context.Context, json.RawMessage) (interface{}, error) {
			return nil, errors.New("cannot read registry")
		}, time.Second, 1)
	})
	t.Run("nested caller", func(t *testing.T) {
		testRecoveryClassificationFailure(t, func(context.Context, json.RawMessage) (interface{}, error) {
			return &ipc.GateContextResult{Nested: true, AgentDescendant: true}, nil
		}, time.Second, 1)
	})
}

func testRecoveryClassificationFailure(t *testing.T, handler ipc.HandlerFunc, timeout time.Duration, wantExit int) {
	t.Helper()
	t.Setenv("NS_HOME", makeSocketSafeTempDir(t))
	p, err := paths.New()
	if err != nil {
		t.Fatal(err)
	}
	srv := ipc.NewServer()
	srv.Handle(ipc.MethodHealth, func(context.Context, json.RawMessage) (interface{}, error) {
		return &ipc.HealthResult{Status: "ok"}, nil
	})
	srv.Handle(ipc.MethodGateContext, handler)
	if err := srv.Listen(p.Socket()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.ServeReady() }()
	t.Cleanup(func() {
		srv.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"axi", "sync", "--recover"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err = cmd.ExecuteContext(ctx)
	var exit *exitError
	gotExit := 1
	if err == nil {
		gotExit = 0
	} else if errors.As(err, &exit) {
		gotExit = exit.code
	}
	if gotExit != wantExit {
		t.Fatalf("error = %v (exit %d), want exit %d; output:\n%s", err, gotExit, wantExit, out.String())
	}
	if wantExit == 75 {
		for _, want := range []string{"code: gate_context_timeout", "retryable: true", "daemon_reachable: true"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("missing %q in output:\n%s", want, out.String())
			}
		}
	} else if strings.Contains(out.String(), "retryable: true") {
		t.Fatalf("non-timeout misreported as retryable:\n%s", out.String())
	}
	if _, err := os.Stat(p.DB()); !os.IsNotExist(err) {
		t.Fatalf("classification failure must not create recovery resources: stat error = %v", err)
	}
}
