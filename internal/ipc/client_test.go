package ipc

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type timeoutDialError struct{}

func (timeoutDialError) Error() string   { return "dial timeout" }
func (timeoutDialError) Timeout() bool   { return true }
func (timeoutDialError) Temporary() bool { return true }

func TestDialConnectTimeoutFailsFastAndNamesSocket(t *testing.T) {
	const timeout = 25 * time.Millisecond
	t.Setenv("NS_DAEMON_CONNECT_TIMEOUT", timeout.String())
	t.Setenv("NM_DAEMON_CONNECT_TIMEOUT", "")

	originalDial := dialNetworkWithTimeout
	dialNetworkWithTimeout = func(network, address string, gotTimeout time.Duration) (net.Conn, error) {
		if gotTimeout != timeout {
			t.Fatalf("dial timeout = %v, want %v", gotTimeout, timeout)
		}
		time.Sleep(gotTimeout + 10*time.Millisecond)
		return nil, timeoutDialError{}
	}
	t.Cleanup(func() {
		dialNetworkWithTimeout = originalDial
	})

	socketPath := filepath.Join(t.TempDir(), "no-slop-dead.sock")
	if runtime.GOOS == "windows" {
		endpoint := fmt.Sprintf("127.0.0.1:1\ntoken\n%d", os.Getpid())
		if err := os.WriteFile(socketPath, []byte(endpoint), 0o600); err != nil {
			t.Fatalf("write endpoint file: %v", err)
		}
	}
	started := time.Now()
	client, err := Dial(socketPath)
	elapsed := time.Since(started)
	if client != nil {
		t.Fatal("Dial returned a client for a timed-out socket")
	}
	if err == nil {
		t.Fatal("Dial returned nil error for a timed-out socket")
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("Dial took %v, want fast failure", elapsed)
	}
	if !IsConnectTimeout(err) {
		t.Fatalf("Dial error %T %v, want connect timeout", err, err)
	}
	if !strings.Contains(err.Error(), socketPath) {
		t.Fatalf("Dial error = %q, want socket path %q", err.Error(), socketPath)
	}
}

func TestConnectTimeoutAcceptsLegacyAliasAndRejectsConflict(t *testing.T) {
	t.Setenv("NS_DAEMON_CONNECT_TIMEOUT", "")
	t.Setenv("NM_DAEMON_CONNECT_TIMEOUT", "41ms")
	got, err := connectTimeout()
	if err != nil {
		t.Fatalf("connectTimeout() error = %v", err)
	}
	if got != 41*time.Millisecond {
		t.Fatalf("legacy timeout = %v, want 41ms", got)
	}
	t.Setenv("NS_DAEMON_CONNECT_TIMEOUT", "42ms")
	if _, err := connectTimeout(); err == nil {
		t.Fatal("expected conflicting timeout aliases to fail")
	}
}
