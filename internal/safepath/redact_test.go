package safepath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactTextPublicationShapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"linux", "/home/testuser/repo/out.log", "~/repo/out.log"},
		{"macos", "/Users/testuser/repo/out.log", "~/repo/out.log"},
		{"windows", `C:\Users\testuser\repo\out.log`, `~\repo\out.log`},
		{"windows forward separators", "C:/Users/testuser/repo/out.log", "~/repo/out.log"},
		{"json escaped windows", `{"path":"C:\\Users\\testuser\\out.log"}`, `{"path":"~\\out.log"}`},
		{"json escaped line initial posix", `{"log":"first\n/home/testuser/out.log"}`, `{"log":"first\n~/out.log"}`},
		{"json escaped line initial windows", `{"log":"first\nC:\\Users\\testuser\\out.log"}`, `{"log":"first\n~\\out.log"}`},
		{"file URL", "file:///home/testuser/repo/out.log", "~/repo/out.log"},
		{"repeated roots", "/home/testuser/a /Users/testuser/b C:/Users/testuser/c", "~/a ~/b ~/c"},
		{"pytest rootdir", "rootdir: /home/testuser/.no-slop/worktrees/run/repo", "rootdir: ~/.no-slop/worktrees/run/repo"},
		{"fenced evidence", "```text\n/home/testuser/evidence/out.log\n```", "```text\n~/evidence/out.log\n```"},
		{"colon list", "PATH=/home/testuser/bin:/home/testuser/go/bin", "PATH=~/bin:~/go/bin"},
		{"attached flag", "-I/home/testuser/include", "-I~/include"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := RedactText(tt.in); got != tt.want {
				t.Fatalf("RedactText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRedactTextNegativeControls(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		"https://api.github.com/users/octocat",
		"https://users/list",
		"https://home/x",
		"src/users/service.ts:42",
		"/srv/home/config.yaml",
		"ordinary prose about a home directory",
	} {
		if got := RedactText(in); got != in {
			t.Errorf("RedactText(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestRedactTextUsesResolvedCurrentHome(t *testing.T) {
	root := t.TempDir()
	realHome := filepath.Join(root, "real", "operator")
	if err := os.MkdirAll(realHome, 0o755); err != nil {
		t.Fatal(err)
	}
	linkedHome := filepath.Join(root, "linked-home")
	if err := os.Symlink(realHome, linkedHome); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Setenv("HOME", linkedHome)
	t.Setenv("USERPROFILE", linkedHome)

	resolvedHome, err := filepath.EvalSymlinks(linkedHome)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{linkedHome + "/repo/out.log", resolvedHome + "/repo/out.log"} {
		if got := RedactText(path); got != "~/repo/out.log" {
			t.Errorf("RedactText(%q) = %q, want %q", path, got, "~/repo/out.log")
		}
	}
}

func TestRedactTextNeverGrowsPublication(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		"/home/testuser/a",
		"/Users/testuser",
		`C:\Users\testuser\a`,
		`{"log":"line\n/home/testuser/x"}`,
		strings.Repeat("/home/testuser/x ", 20),
		"no path",
	} {
		if got := RedactText(in); len(got) > len(in) {
			t.Fatalf("RedactText grew %q from %d to %d bytes", in, len(in), len(got))
		}
	}
}
