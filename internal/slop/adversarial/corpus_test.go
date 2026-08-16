package adversarial_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/slop/adversarial"
	slopcli "github.com/Blakeolson21/no-slop/internal/slop/cli"
	"github.com/Blakeolson21/no-slop/internal/slop/engine"
)

// unavailableReviewer mirrors the reviewer's sandbox, where no AI agent
// resolves. Every tier above leak-scan-only therefore ends at exit 2, which is
// the gate refusing to validate rather than waving the change through. Exit 0
// with "verdict: pass" is the fail-open signature the corpus hunts.
func unavailableReviewer(context.Context, *config.Config, io.Writer) (engine.Reviewer, io.Closer, error) {
	return nil, nil, errors.New("no runnable agent found")
}

// TestCorpusInvariant is the corpus's own gate. A case in a never-passes class
// that expects exit 0, or that does not forbid a passing verdict, is rejected
// here before it can be run, so nobody can re-open the class by relaxing a
// single case.
func TestCorpusInvariant(t *testing.T) {
	t.Parallel()

	cases := adversarial.Cases()
	if len(cases) < 20 {
		t.Fatalf("corpus holds %d cases; the reviewer's probe set is larger than that and must not be trimmed", len(cases))
	}
	seen := make(map[string]bool, len(cases))
	byClass := make(map[adversarial.Class]int)
	for _, probe := range cases {
		if probe.Name == "" || probe.Summary == "" {
			t.Errorf("case %q must carry a name and a one-line summary", probe.Name)
		}
		if seen[probe.Name] {
			t.Errorf("duplicate case name %q", probe.Name)
		}
		seen[probe.Name] = true
		byClass[probe.Class]++
		if len(probe.Base) == 0 || len(probe.Head)+len(probe.HeadDelete) == 0 {
			t.Errorf("case %q needs a base tree and a head change", probe.Name)
		}
		if !probe.Class.NeverPasses() {
			continue
		}
		if probe.WantExit == 0 {
			t.Errorf("case %q is in class %s and may not expect exit 0", probe.Name, probe.Class)
		}
		if !contains(probe.WantNotStdout, "verdict: pass") {
			t.Errorf("case %q is in class %s and must forbid \"verdict: pass\"", probe.Name, probe.Class)
		}
	}
	for _, class := range []adversarial.Class{
		adversarial.ClassAuthorizationWeakening,
		adversarial.ClassFleetInstructionRewrite,
		adversarial.ClassMandatoryCheckIntegrity,
		adversarial.ClassOracleIntegrity,
		adversarial.ClassControl,
	} {
		if byClass[class] == 0 {
			t.Errorf("class %s has no cases", class)
		}
	}
}

func TestCorpus(t *testing.T) {
	t.Parallel()

	for _, probe := range adversarial.Cases() {
		t.Run(probe.Name, func(t *testing.T) {
			t.Parallel()
			runs := probe.Repeat
			if runs < 1 {
				runs = 1
			}
			var first string
			for attempt := 0; attempt < runs; attempt++ {
				exitCode, stdout := runCase(t, probe)
				// Each repeat builds its own repository, so the base commit id
				// and the temporary path are new every time. The property under
				// test is that the same claim is judged the same way, not that
				// two different repositories hash alike, so the volatile
				// identifiers come out before the comparison.
				report := volatileIdentifiers.ReplaceAllString(fmt.Sprintf("exit=%d\n%s", exitCode, stdout), "<volatile>")
				if attempt == 0 {
					first = report
				} else if report != first {
					t.Fatalf("run %d differs from run 1.\nrun 1:\n%s\nrun %d:\n%s", attempt+1, first, attempt+1, report)
				}
				if exitCode != probe.WantExit {
					t.Fatalf("exit = %d, want %d\n%s", exitCode, probe.WantExit, stdout)
				}
				for _, want := range probe.WantStdout {
					if !strings.Contains(stdout, want) {
						t.Fatalf("output missing %q:\n%s", want, stdout)
					}
				}
				for _, unwanted := range probe.WantNotStdout {
					if strings.Contains(stdout, unwanted) {
						t.Fatalf("output must not contain %q:\n%s", unwanted, stdout)
					}
				}
			}
		})
	}
}

// volatileIdentifiers matches the run-to-run values a fresh probe repository
// necessarily changes: the resolved base commit id and the temporary directory.
var volatileIdentifiers = regexp.MustCompile(`\b[0-9a-f]{40}\b|/[^\s]*/TestCorpus[^\s]*`)

func runCase(t *testing.T, probe adversarial.Case) (int, string) {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "user.email", "probe@example.com")
	git(t, dir, "config", "user.name", "Probe")
	git(t, dir, "config", "commit.gpgsign", "false")
	for path, content := range probe.Base {
		write(t, dir, path, content)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))

	git(t, dir, "switch", "-q", "-c", "probe")
	if len(probe.Intermediate) > 0 {
		for path, content := range probe.Intermediate {
			write(t, dir, path, content)
		}
		git(t, dir, "add", "-A")
		git(t, dir, "commit", "-q", "-m", "intermediate")
	}
	for path, content := range probe.Head {
		write(t, dir, path, content)
	}
	for _, path := range probe.HeadDelete {
		if err := os.Remove(filepath.Join(dir, path)); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "head")
	for path, content := range probe.Uncommitted {
		write(t, dir, path, content)
	}
	for _, setup := range probe.GitSetup {
		git(t, dir, setup...)
	}

	// The harness is an in-process orchestrator that resolved the base itself,
	// which is precisely the caller the pipeline channel exists for. It is a Go
	// field with no command-line or file equivalent, so a case that wants to
	// attack base resolution asks for StandaloneBase and gets none of it.
	options := slopcli.Options{ReviewerFactory: unavailableReviewer}
	if !probe.StandaloneBase {
		options.PipelineBase = &slopcli.PipelineBase{
			Commit:        base,
			Origin:        "adversarial corpus harness",
			DefaultBranch: "main",
		}
	}
	args := append([]string{"gate", "--repo", dir}, probe.Args...)
	var stdout, stderr bytes.Buffer
	exitCode := slopcli.Run(context.Background(), args, &stdout, &stderr, options)
	return exitCode, stdout.String() + stderr.String()
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_COUNT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
