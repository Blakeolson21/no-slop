//go:build !windows

package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestManagedServerAdaptersRestartForInvocationEnvironment(t *testing.T) {
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test binary: %v", err)
	}
	_, err = exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}

	for _, tc := range []struct{ name string }{{name: "opencode"}, {name: "rovodev"}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			record := filepath.Join(dir, "starts")
			script := filepath.Join(dir, "managed-server")
			body := `#!/bin/sh
port=
while [ "$#" -gt 0 ]; do
	case "$1" in
		--port|--disable-session-token) port="$2"; shift 2 ;;
		*) shift ;;
	esac
done
exec "$NS_TEST_MANAGED_SERVER_BINARY" -test.run=^TestManagedServerDutyHelperProcess$ -- "$port"
`
			if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
				t.Fatalf("write helper script: %v", err)
			}

			var ensure func(context.Context, string, []string) (string, error)
			var closeAgent func() error
			switch tc.name {
			case "opencode":
				a := &opencodeAgent{bin: script}
				ensure = a.ensureServer
				closeAgent = a.Close
			case "rovodev":
				a := &rovodevAgent{bin: script}
				ensure = a.ensureServer
				closeAgent = a.Close
			}
			t.Cleanup(func() { _ = closeAgent() })

			baseEnv := []string{
				"NS_TEST_MANAGED_SERVER_HELPER=1",
				"NS_TEST_MANAGED_SERVER_BINARY=" + testBinary,
				"NS_TEST_MANAGED_SERVER_RECORD=" + record,
			}
			reviewEnv := append(append([]string{}, baseEnv...), GateStepKindEnvVar+"=review", GateTurnKindEnvVar+"=review")
			fixEnv := append(append([]string{}, baseEnv...), GateStepKindEnvVar+"=review", GateTurnKindEnvVar+"=fix")

			if _, err := ensure(context.Background(), dir, reviewEnv); err != nil {
				t.Fatalf("start review server: %v", err)
			}
			if _, err := ensure(context.Background(), dir, fixEnv); err != nil {
				t.Fatalf("start fix server: %v", err)
			}
			if _, err := ensure(context.Background(), dir, fixEnv); err != nil {
				t.Fatalf("reuse fix server: %v", err)
			}

			data, err := os.ReadFile(record)
			if err != nil {
				t.Fatalf("read server starts: %v", err)
			}
			if got, want := strings.Fields(string(data)), []string{"review/review", "review/fix"}; !slices.Equal(got, want) {
				t.Fatalf("managed server duties = %v, want %v", got, want)
			} else {
				t.Logf("%s managed-server launch duties: %v", tc.name, got)
			}
		})
	}
}

func TestManagedServerDutyHelperProcess(t *testing.T) {
	if os.Getenv("NS_TEST_MANAGED_SERVER_HELPER") != "1" {
		return
	}
	args := os.Args
	port := args[len(args)-1]
	record, err := os.OpenFile(os.Getenv("NS_TEST_MANAGED_SERVER_RECORD"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(record, "%s/%s\n", os.Getenv(GateStepKindEnvVar), os.Getenv(GateTurnKindEnvVar)); err != nil {
		_ = record.Close()
		t.Fatal(err)
	}
	if err := record.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	if err := server.Serve(listener); err != nil {
		t.Fatal(err)
	}
}
