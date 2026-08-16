package cli_test

import (
	"strings"
	"testing"
)

// The private-name blocklist was the one gate-strength input still read from the
// head worktree. loadBlocklist took the PATH from the base config and the
// CONTENT from the worktree, and `.noslop-blocklist` is not a field of
// config.Slop, so slopConfigDrift never compared it and the run reported
// nothing. Emptying a tracked, operator-configured blocklist with one shell
// redirect passed a blocklisted identity at exit 0.
//
// That contradicted the sentence the product states about itself: no file the
// change can edit, committed or not, can make the gate run less strictly. The
// path was covered. The content was not.

const blocklistGateConfig = "slop:\n  leak_scan:\n    blocklist_file: .noslop-blocklist\n  risk:\n    single_review_threshold: 90\n    full_adversarial_threshold: 99\n"

const blockedName = "acme-secret-codename"

// trackedBlocklistRepo is the reviewer's U3 shape: an operator who configured a
// blocklist at the base ref and committed the file it names.
func trackedBlocklistRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	writeFile(t, dir, ".no-slop.yaml", blocklistGateConfig)
	writeFile(t, dir, ".noslop-blocklist", blockedName+"\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/notes")
	writeFile(t, dir, "docs/notes.txt", "the "+blockedName+" rollout is next week\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "notes")
	return dir
}

// TestATrackedBlocklistIsReadFromTheBaseRef is the control. The operator's list
// is in force and the private name in the change is caught.
func TestATrackedBlocklistIsReadFromTheBaseRef(t *testing.T) {
	t.Parallel()

	dir := trackedBlocklistRepo(t)

	code, output := runGateIn(t, dir)
	if code != 1 {
		t.Fatalf("exit = %d, want 1: the change carries a blocklisted private name\n%s", code, output)
	}
	if !strings.Contains(output, "private name matches the configured identity blocklist") {
		t.Fatalf("the identity scan did not fire:\n%s", output)
	}
	if !strings.Contains(output, "at the base ref") {
		t.Fatalf("the run does not say which revision supplied the blocklist content:\n%s", output)
	}
	// The finding must never reproduce the private name it matched, which is the
	// whole reason a blocklist is a list of things not to print.
	if strings.Contains(output, blockedName) {
		t.Fatalf("the run printed the private name it was told to protect:\n%s", output)
	}
}

// TestEmptyingATrackedBlocklistAtHeadCannotDisarmTheIdentityScan is the probe.
// One redirect empties the tracked file; the base ref's copy is untouched and
// is the one in force.
func TestEmptyingATrackedBlocklistAtHeadCannotDisarmTheIdentityScan(t *testing.T) {
	t.Parallel()

	for _, probe := range []struct {
		name      string
		committed bool
	}{
		{"emptied in the worktree only", false},
		{"emptied and committed", true},
	} {
		t.Run(probe.name, func(t *testing.T) {
			t.Parallel()

			dir := trackedBlocklistRepo(t)
			writeFile(t, dir, ".noslop-blocklist", "")
			if probe.committed {
				runGit(t, dir, "add", "-A")
				runGit(t, dir, "commit", "-m", "empty the blocklist")
			}

			code, output := runGateIn(t, dir)
			if code == 0 {
				t.Fatalf("emptying the blocklist passed a blocklisted identity:\n%s", output)
			}
			if !strings.Contains(output, "private name matches the configured identity blocklist") {
				t.Fatalf("the head copy disarmed the identity scan:\n%s", output)
			}
			if !strings.Contains(output, "(1 entries)") {
				t.Fatalf("the run did not report the entry count actually in force:\n%s", output)
			}
			// A head edit to a gate-strength input is drift, and drift is named
			// rather than silently ignored, exactly as it is for .no-slop.yaml.
			if !strings.Contains(output, "gate-config-drift") {
				t.Fatalf("editing the blocklist at head was not reported as drift:\n%s", output)
			}
			if !strings.Contains(output, ".noslop-blocklist") {
				t.Fatalf("the drift finding does not name the file that drifted:\n%s", output)
			}
		})
	}
}

// TestAddingToTheBlocklistAtHeadIsStillDrift pins the direction rule the
// repo config already follows. A tightening cannot certify itself either: the
// run that would bless the addition is the run it reconfigures.
func TestAddingToTheBlocklistAtHeadIsStillDrift(t *testing.T) {
	t.Parallel()

	dir := trackedBlocklistRepo(t)
	writeFile(t, dir, ".noslop-blocklist", blockedName+"\nsecond-private-name\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "extend the blocklist")

	code, output := runGateIn(t, dir)
	if code == 0 {
		t.Fatalf("exit = %d, want the drift reported\n%s", code, output)
	}
	if !strings.Contains(output, "gate-config-drift") {
		t.Fatalf("adding an entry at head was not reported as drift:\n%s", output)
	}
}

// TestAnUntrackedBlocklistIsReadFromTheWorktreeAndSaysSo is the honest limit.
// `.noslop-blocklist` is documented as local private data that is gitignored,
// so it cannot in general be read from the base ref, and pretending otherwise
// would break every operator using it as documented. What the run must not do
// is leave a reader thinking that copy carries the same guarantee as the rest
// of the base-ref boundary.
func TestAnUntrackedBlocklistIsReadFromTheWorktreeAndSaysSo(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	writeFile(t, dir, ".no-slop.yaml", blocklistGateConfig)
	writeFile(t, dir, ".gitignore", ".noslop-blocklist\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/notes")
	writeFile(t, dir, ".noslop-blocklist", blockedName+"\n")
	writeFile(t, dir, "docs/notes.txt", "the "+blockedName+" rollout is next week\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "notes")

	code, output := runGateIn(t, dir)
	if code != 1 {
		t.Fatalf("exit = %d, want 1: an untracked blocklist still scans\n%s", code, output)
	}
	if !strings.Contains(output, "private name matches the configured identity blocklist") {
		t.Fatalf("the untracked blocklist was not honored:\n%s", output)
	}
	if !strings.Contains(output, "outside the base-ref boundary") {
		t.Fatalf("the run does not say the blocklist content sits outside the boundary:\n%s", output)
	}
}
