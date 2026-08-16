package cli_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/config"
	slopcli "github.com/Blakeolson21/no-slop/internal/slop/cli"
	"github.com/Blakeolson21/no-slop/internal/slop/engine"
)

// The tests in this file defend one boundary: no file the change under test can
// edit, committed or not, may make the gate run less strictly. The reviewer
// found four separate gate-strength controls being read from the author's
// working tree, one of which did not even have to be committed, and that is
// what a gate configured by the artifact it is gating looks like.

type boundaryRepo struct {
	dir  string
	base string
}

func newBoundaryRepo(t *testing.T, base map[string]string) boundaryRepo {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	for path, content := range base {
		writeFile(t, dir, path, content)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	baseSHA := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "switch", "-c", "feature/probe")
	return boundaryRepo{dir: dir, base: baseSHA}
}

func (r boundaryRepo) commit(t *testing.T, files map[string]string) {
	t.Helper()
	for path, content := range files {
		writeFile(t, r.dir, path, content)
	}
	runGit(t, r.dir, "add", "-A")
	runGit(t, r.dir, "commit", "-m", "head")
}

// gate runs the command with no reviewer available, so any tier above
// leak-scan-only ends at exit 2. That is the gate refusing to validate, which
// is the correct non-passing outcome for these probes and keeps the tests off
// the machine's real agent lookup.
func (r boundaryRepo) gate(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	full := append([]string{"gate", "--repo", r.dir, "--base", r.base}, args...)
	code := slopcli.Run(context.Background(), full, &stdout, &stderr, slopcli.Options{
		ReviewerFactory: func(context.Context, *config.Config, io.Writer) (engine.Reviewer, io.Closer, error) {
			return nil, nil, errors.New("no runnable agent found")
		},
	})
	return code, stdout.String() + stderr.String()
}

const twoTests = `package calc

import "testing"

func TestAddZero(t *testing.T) {
	if Add(0, 0) != 0 {
		t.Fatal("zero")
	}
}

func TestAddOne(t *testing.T) {
	if Add(1, 0) != 1 {
		t.Fatal("one")
	}
}
`

const oneTest = `package calc

import "testing"

func TestAddOne(t *testing.T) {
	if Add(1, 0) != 1 {
		t.Fatal("one")
	}
}
`

// TestUncommittedConfigCannotDisableAMandatoryCheck is the sharpest version of
// the boundary: the author never committed the file, so it is not even part of
// the change being judged, and it still turned the test-count floor off.
func TestUncommittedConfigCannotDisableAMandatoryCheck(t *testing.T) {
	t.Parallel()

	repo := newBoundaryRepo(t, map[string]string{"calc_test.go": twoTests})
	repo.commit(t, map[string]string{"calc_test.go": oneTest})

	code, output := repo.gate(t, "--tier", "leak-scan-only")
	if code != 1 || !strings.Contains(output, "test-capitulation") {
		t.Fatalf("control run exit = %d, want the floor to fire\n%s", code, output)
	}

	writeFile(t, repo.dir, ".no-slop.yaml", "slop:\n  test_count_floor: false\n")
	if status := runGit(t, repo.dir, "status", "--porcelain", ".no-slop.yaml"); !strings.Contains(status, "??") {
		t.Fatalf("the probe file must stay uncommitted, got %q", status)
	}
	code, output = repo.gate(t, "--tier", "leak-scan-only")
	if code != 1 {
		t.Fatalf("an uncommitted config disabled a mandatory check: exit = %d\n%s", code, output)
	}
	if strings.Contains(output, "mandatory check: test-count floor disabled") {
		t.Fatalf("the floor reported itself disabled by an uncommitted file:\n%s", output)
	}
}

// TestCommittedConfigWeakeningIsItselfFlagged is the other half of the
// boundary. Ignoring the head value silently would leave a contributor who
// tightened the gate wondering why nothing changed, and one who loosened it
// unnamed.
func TestCommittedConfigWeakeningIsItselfFlagged(t *testing.T) {
	t.Parallel()

	repo := newBoundaryRepo(t, map[string]string{"calc_test.go": twoTests})
	repo.commit(t, map[string]string{
		"calc_test.go":  oneTest,
		".no-slop.yaml": "slop:\n  test_count_floor: false\n  risk:\n    single_review_threshold: 99\n    full_adversarial_threshold: 100\n",
	})

	code, output := repo.gate(t, "--tier", "leak-scan-only")
	if code == 0 {
		t.Fatalf("a committed config weakening passed:\n%s", output)
	}
	for _, want := range []string{
		"finding: [gate-config-drift]",
		"slop.test_count_floor",
		"slop.risk.single_review_threshold",
		"finding: [test-capitulation]",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
}

// TestBaseConfigStillTightensTheGate keeps the boundary from becoming an excuse
// to ignore configuration. A value already on the base ref is the operator's,
// and it applies.
func TestBaseConfigStillTightensTheGate(t *testing.T) {
	t.Parallel()

	repo := newBoundaryRepo(t, map[string]string{
		".no-slop.yaml":      "slop:\n  risk:\n    high_risk_paths:\n      - \"**/policy.md\"\n",
		"services/policy.md": "# Policy\n\nRequests need an approval.\n",
	})
	repo.commit(t, map[string]string{"services/policy.md": "# Policy\n\nApproval is optional.\n"})

	code, output := repo.gate(t)
	if code == 0 {
		t.Fatalf("a base-configured high-risk path was ignored:\n%s", output)
	}
	if strings.Contains(output, "finding: [gate-config-drift]") {
		t.Fatalf("an unchanged config reported drift:\n%s", output)
	}
}

// TestUnparsableBaseConfigFailsClosed keeps an undeterminable gate strength
// from resolving to the permissive default.
func TestUnparsableBaseConfigFailsClosed(t *testing.T) {
	t.Parallel()

	// The head copy parses cleanly, so only the base ref's copy can be the
	// thing that refuses. That is the point: the base ref is the authority, so
	// an unreadable base config is an unreadable gate strength.
	repo := newBoundaryRepo(t, map[string]string{
		".no-slop.yaml": "slop:\n  risk:\n    single_review_threshold: [not, a, number]\n",
		"README.md":     "# Project\n",
	})
	repo.commit(t, map[string]string{
		".no-slop.yaml": "slop:\n  risk:\n    single_review_threshold: 3\n",
		"README.md":     "# Project\n\nPlain update.\n",
	})

	code, output := repo.gate(t)
	if code != 2 {
		t.Fatalf("exit = %d, want a refusal on an unreadable base config\n%s", code, output)
	}
	if !strings.Contains(output, "base repo config") {
		t.Fatalf("the refusal does not name the base config:\n%s", output)
	}
}

// TestBlocklistFlagAddsNamesRatherThanReplacingThem keeps the command line from
// becoming the fifth relocated control. Replacing the configured list would let
// the audited party point the identity scan at an empty file.
func TestBlocklistFlagAddsNamesRatherThanReplacingThem(t *testing.T) {
	t.Parallel()

	repo := newBoundaryRepo(t, map[string]string{
		".no-slop.yaml":     "slop:\n  leak_scan:\n    blocklist_file: .configured-names\n",
		".configured-names": "acme-internal\n",
		"notes.txt":         "nothing\n",
	})
	repo.commit(t, map[string]string{"notes.txt": "the acme-internal host is here\n"})
	writeFile(t, repo.dir, ".empty-names", "# nothing\n")

	code, output := repo.gate(t, "--tier", "leak-scan-only", "--blocklist", ".empty-names")
	if code != 1 || !strings.Contains(output, "leak-identity-scan") {
		t.Fatalf("an empty --blocklist replaced the configured one: exit = %d\n%s", code, output)
	}
}

// TestConfiguredPatternThatMatchesNothingIsReported closes the silent half of
// the glob problem: an operator who configures a protection and gets silence
// cannot tell a pattern that covers nothing from one that covers everything.
func TestConfiguredPatternThatMatchesNothingIsReported(t *testing.T) {
	t.Parallel()

	repo := newBoundaryRepo(t, map[string]string{
		".no-slop.yaml": "slop:\n  risk:\n    high_risk_paths:\n      - internal/nowhere/**\n",
		"README.md":     "# Project\n",
	})
	repo.commit(t, map[string]string{"README.md": "# Project\n\nPlain update.\n"})

	_, output := repo.gate(t)
	if !strings.Contains(output, `config warning: slop.risk.high_risk_paths pattern "internal/nowhere/**" matches no path at head`) {
		t.Fatalf("an unmatched configured pattern produced no warning:\n%s", output)
	}
}

// TestThreadFlagWithNoURLIsRefused removes the silent no-op an empty --thread
// used to be.
func TestThreadFlagWithNoURLIsRefused(t *testing.T) {
	t.Parallel()

	repo := newBoundaryRepo(t, map[string]string{"README.md": "# Project\n"})
	repo.commit(t, map[string]string{"README.md": "# Project\n\nPlain update.\n"})

	code, output := repo.gate(t, "--thread", "   ")
	if code != 2 || !strings.Contains(output, "--thread requires") {
		t.Fatalf("an empty --thread was accepted as a no-op: exit = %d\n%s", code, output)
	}
}

// TestExemptionAccountingReportsSuppressedFindings keeps the honored count from
// overstating the bypass: a marker on a clean line suppresses nothing.
func TestExemptionAccountingReportsSuppressedFindings(t *testing.T) {
	t.Parallel()

	repo := newBoundaryRepo(t, map[string]string{"README.md": "# Project\n"})
	repo.commit(t, map[string]string{
		"fixtures/notes.txt": "AKIAIOSFODNN7EXAMPLE # noslop:allow-leak\nplain line # noslop:allow-leak\n",
	})

	code, output := repo.gate(t, "--tier", "leak-scan-only")
	if code != 0 {
		t.Fatalf("exit = %d, want the exemptions honored\n%s", code, output)
	}
	for _, want := range []string{
		"leak exemption: fixtures/notes.txt:1: noslop:allow-leak (1 findings suppressed)",
		"leak exemption: fixtures/notes.txt:2: noslop:allow-leak (0 findings suppressed)",
		"leak scan: 2 leak exemptions honored, 1 findings suppressed",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
}

// TestProseQuotingTheMarkerDoesNotExemptItsOwnLine pins the anchoring. Plain
// substring matching meant documentation about the feature exempted itself.
func TestProseQuotingTheMarkerDoesNotExemptItsOwnLine(t *testing.T) {
	t.Parallel()

	repo := newBoundaryRepo(t, map[string]string{"README.md": "# Project\n"})
	repo.commit(t, map[string]string{
		"fixtures/notes.txt": "the marker noslop:allow-leakage is not the marker, key AKIAIOSFODNN7EXAMPLE\n", // noslop:allow-leak
	})

	code, output := repo.gate(t, "--tier", "leak-scan-only")
	if code != 1 || !strings.Contains(output, "leak-identity-scan") {
		t.Fatalf("a near-miss of the marker exempted a live credential: exit = %d\n%s", code, output)
	}
}
