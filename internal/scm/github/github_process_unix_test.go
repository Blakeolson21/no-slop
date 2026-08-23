//go:build unix

package github

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/scm"
	"github.com/Blakeolson21/no-slop/internal/shellenv"
)

func TestGetPRContentReapsDescendantHoldingStdout(t *testing.T) {
	host := New(func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `(sleep 30) & printf '{"title":"fix: refresh","body":"pipeline"}'`)
		shellenv.ConfigureShellCommand(cmd)
		return cmd
	}, nil, "", "test/repo")

	started := time.Now()
	content, err := host.GetPRContent(context.Background(), &scm.PR{Number: "42"})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("GetPRContent waited %s for a surviving descendant", elapsed)
	}
	if content.Title != "fix: refresh" || content.Body != "pipeline" {
		t.Fatalf("content = %#v", content)
	}
}
