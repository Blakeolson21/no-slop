//go:build unix

package gatecontext

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/paths"
)

func TestInspectorSnapshotsAncestryOnce(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NS_HOME", root)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	count := filepath.Join(root, "calls")
	t.Setenv("CLASSIFIER_TEST_CALLS", count)
	// Some process tables include the kernel's PID 0 row.
	script := "#!/bin/sh\necho call >> \"$CLASSIFIER_TEST_CALLS\"\nprintf '0 0\\n65003 65002\\n65002 65001\\n65001 1\\n1 0\\n'\n"
	if err := os.WriteFile(filepath.Join(root, "ps"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := (Inspector{Paths: paths.WithRoot(root)}).Inspect(context.Background(), Request{PeerPID: 65003, DaemonPID: 65001})
	if err != nil || !result.Nested || !result.DaemonDescendant {
		t.Fatalf("ancestry classification = %+v, %v", result, err)
	}
	data, err := os.ReadFile(count)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "call") != 1 {
		t.Fatalf("expected one snapshot for the entire ancestry, got %q", data)
	}
}

func TestInspectorProcessSnapshotDeadlineFailsClosed(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NS_HOME", root)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	// exec avoids leaving a child behind even on the unfixed implementation.
	if err := os.WriteFile(filepath.Join(root, "ps"), []byte("#!/bin/sh\nexec sleep 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := (Inspector{Paths: paths.WithRoot(root)}).Inspect(ctx, Request{PeerPID: os.Getpid(), DaemonPID: 65001})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.HasPrefix(err.Error(), TimeoutErrorCode+":") {
		t.Fatalf("stalled snapshot error = %v, want a classification timeout", err)
	}
}

func TestInspectorProcessSnapshotErrorsFailClosed(t *testing.T) {
	for _, script := range []string{"exit 1", "echo invalid", "exit 0"} {
		t.Run(script, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("NS_HOME", root)
			t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
			if err := os.WriteFile(filepath.Join(root, "ps"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			_, err := (Inspector{Paths: paths.WithRoot(root)}).Inspect(context.Background(), Request{PeerPID: os.Getpid(), DaemonPID: 65001})
			if err == nil {
				t.Fatal("unreadable process ancestry must not authorize the caller")
			}
		})
	}
}
