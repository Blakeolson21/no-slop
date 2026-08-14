package prose_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/slop/prose"
)

func TestGHThreadReaderReadsPullRequestStateAndComments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}

	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.txt")
	ghPath := filepath.Join(dir, "gh")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ARGS_PATH\"\nprintf '%s' '{\"state\":\"OPEN\",\"comments\":[{\"body\":\"existing claim\"}]}'\n"
	if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARGS_PATH", argsPath)

	thread, err := prose.NewGHThreadReader(ghPath).Read(context.Background(), "https://github.com/example/project/pull/17")
	if err != nil {
		t.Fatal(err)
	}
	if !thread.Open || len(thread.Comments) != 1 || thread.Comments[0] != "existing claim" {
		t.Fatalf("thread = %+v", thread)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(args); !strings.Contains(got, "pr\nview\nhttps://github.com/example/project/pull/17\n--json\nstate,comments") {
		t.Fatalf("gh args = %q", got)
	}
}
