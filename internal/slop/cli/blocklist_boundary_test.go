package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	slopcli "github.com/Blakeolson21/no-slop/internal/slop/cli"
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

// TestDeletingATrackedBlocklistAtHeadIsStillDrift closes the direction the
// comparison could not see. Emptying the file to zero bytes was drift and
// deleting it outright was silence, which are the same edit with the same
// effect on nothing: the base ref's copy stays in force either way, so the
// difference was only in whether the run said what the change did.
func TestDeletingATrackedBlocklistAtHeadIsStillDrift(t *testing.T) {
	t.Parallel()

	for _, probe := range []struct {
		name      string
		committed bool
	}{
		{"removed from the worktree only", false},
		{"removed and committed", true},
	} {
		t.Run(probe.name, func(t *testing.T) {
			t.Parallel()

			dir := trackedBlocklistRepo(t)
			if probe.committed {
				runGit(t, dir, "rm", "-q", ".noslop-blocklist")
				runGit(t, dir, "commit", "-m", "delete the blocklist")
			} else if err := os.Remove(filepath.Join(dir, ".noslop-blocklist")); err != nil {
				t.Fatalf("remove blocklist: %v", err)
			}

			code, output := runGateIn(t, dir)
			if code == 0 {
				t.Fatalf("deleting the blocklist passed a blocklisted identity:\n%s", output)
			}
			if !strings.Contains(output, "private name matches the configured identity blocklist") {
				t.Fatalf("deleting the head copy disarmed the identity scan:\n%s", output)
			}
			if !strings.Contains(output, "gate-config-drift") {
				t.Fatalf("deleting the blocklist at head was not reported as drift:\n%s", output)
			}
			if !strings.Contains(output, ".noslop-blocklist") {
				t.Fatalf("the drift finding does not name the file that drifted:\n%s", output)
			}
			if strings.Contains(output, blockedName) {
				t.Fatalf("the run printed the private name it was told to protect:\n%s", output)
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

// TestABlocklistOutsideTheWorktreeIsReadFromDisk is the other side of the
// boundary ruling. A path that resolves outside the worktree names a file the
// change under test cannot edit, so it is operator-owned already and there is
// no base-ref copy of it to prefer. Routing it through git turned a shape that
// worked - one list shared by sibling clones - into a hard gate abort naming
// `ls-tree` rather than the config key.
func TestABlocklistOutsideTheWorktreeIsReadFromDisk(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	shared := filepath.Join(parent, "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatalf("create shared dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(shared, ".noslop-blocklist"), []byte(blockedName+"\n"), 0o644); err != nil {
		t.Fatalf("write shared blocklist: %v", err)
	}
	dir := filepath.Join(parent, "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create repo dir: %v", err)
	}

	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	writeFile(t, dir, ".no-slop.yaml", strings.Replace(blocklistGateConfig, "blocklist_file: .noslop-blocklist", "blocklist_file: ../shared/.noslop-blocklist", 1))
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/notes")
	writeFile(t, dir, "docs/notes.txt", "the "+blockedName+" rollout is next week\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "notes")

	code, output := runGateIn(t, dir)
	if code != 1 {
		t.Fatalf("exit = %d, want 1: a blocklist outside the worktree still scans\n%s", code, output)
	}
	if !strings.Contains(output, "private name matches the configured identity blocklist") {
		t.Fatalf("the shared blocklist was not honored:\n%s", output)
	}
	if !strings.Contains(output, "resolves outside the repository worktree") {
		t.Fatalf("the run does not say where the blocklist content came from:\n%s", output)
	}
	if strings.Contains(output, "read listing for") {
		t.Fatalf("the run asked git for a base-ref copy of a path outside the tree:\n%s", output)
	}
}

// TestASubdirectoryInvocationReadsTheSameBaseConfig is the entry-point half of
// the boundary. Every base-ref read is a git pathspec relative to the directory
// the gate was pointed at, and a pathspec above the cwd matches nothing while
// exiting 0, so running from a subdirectory read no base config at all: the
// operator's blocklist configuration vanished, the run fell to built-in
// defaults, and nothing said so.
func TestASubdirectoryInvocationReadsTheSameBaseConfig(t *testing.T) {
	t.Parallel()

	dir := trackedBlocklistRepo(t)

	code, output := runGateIn(t, filepath.Join(dir, "docs"))
	if code != 1 {
		t.Fatalf("exit = %d, want 1: a subdirectory invocation gates exactly as the root does\n%s", code, output)
	}
	if !strings.Contains(output, "private name matches the configured identity blocklist") {
		t.Fatalf("the base-ref blocklist was lost from a subdirectory:\n%s", output)
	}
	if !strings.Contains(output, "at the base ref") {
		t.Fatalf("the blocklist content did not come from the base ref:\n%s", output)
	}
}

// TestTheBlocklistFlagLineSaysWhereItActuallyRead is the override's half of the
// boundary ruling. A path that resolves outside the worktree is one the change
// under test cannot edit, and where the added names came from is the claim a
// reviewer uses to size how much of the identity list the change could have
// written, so the line reports it rather than asserting the worktree.
func TestTheBlocklistFlagLineSaysWhereItActuallyRead(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	shared := filepath.Join(parent, "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatalf("create shared dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(shared, "extra-names"), []byte(blockedName+"\n"), 0o644); err != nil {
		t.Fatalf("write shared names: %v", err)
	}
	dir := filepath.Join(parent, "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create repo dir: %v", err)
	}

	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	writeFile(t, dir, ".no-slop.yaml", "slop:\n  risk:\n    single_review_threshold: 90\n    full_adversarial_threshold: 99\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/notes")
	writeFile(t, dir, "docs/notes.txt", "the "+blockedName+" rollout is next week\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "notes")

	code, output := runGateIn(t, dir, "--blocklist", "../shared/extra-names")
	if code != 1 {
		t.Fatalf("exit = %d, want 1: the override names are honored from outside the worktree\n%s", code, output)
	}
	if !strings.Contains(output, "private name matches the configured identity blocklist") {
		t.Fatalf("the override names were not honored:\n%s", output)
	}
	if !strings.Contains(output, "--blocklist added 1 private names read from ../shared/extra-names outside the repository worktree") {
		t.Fatalf("the override line does not say where it read the names from:\n%s", output)
	}
	if strings.Contains(output, "../shared/extra-names in the head worktree") {
		t.Fatalf("the override called a file outside the worktree a worktree copy:\n%s", output)
	}
}

// TestAnUnreadableBlocklistDriftDoesNotRecordAnAbsolutePath keeps the run from
// writing the shape its own identity scan blocks. The head copy is unreadable,
// which is drift, and the drift becomes a finding that stdout prints and the
// append-only provenance record keeps forever. The filesystem error names the
// absolute path it was opening, which on any ordinary machine is a personal
// home path, and `leakscan` calls that a leak.
func TestAnUnreadableBlocklistDriftDoesNotRecordAnAbsolutePath(t *testing.T) {
	t.Parallel()

	dir := trackedBlocklistRepo(t)
	// A directory in the file's place is unreadable for every user, including
	// root, so the probe does not depend on who runs the suite.
	if err := os.Remove(filepath.Join(dir, ".noslop-blocklist")); err != nil {
		t.Fatalf("remove blocklist: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".noslop-blocklist"), 0o755); err != nil {
		t.Fatalf("replace blocklist with a directory: %v", err)
	}

	store := &recordingStore{}
	var stdout, stderr bytes.Buffer
	code := slopcli.Run(context.Background(), []string{"gate", "--repo", dir}, &stdout, &stderr, slopcli.Options{
		ReviewerFactory: noReviewer,
		ProvenanceStore: store,
	})
	output := stdout.String() + stderr.String()
	if code == 0 {
		t.Fatalf("exit = 0: an unreadable head copy of a gate-strength input is drift\n%s", output)
	}
	if !strings.Contains(output, "gate-config-drift") {
		t.Fatalf("an unreadable head copy was not reported as drift:\n%s", output)
	}
	if !strings.Contains(output, ".noslop-blocklist") {
		t.Fatalf("the drift finding does not name the configured path:\n%s", output)
	}
	if strings.Contains(output, dir) {
		t.Fatalf("the run printed the absolute path of the repository:\n%s", output)
	}
	recorded, err := json.Marshal(store.records)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.records) == 0 {
		t.Fatalf("the run recorded nothing, so this test cannot see what provenance would hold:\n%s", output)
	}
	if strings.Contains(string(recorded), dir) {
		t.Fatalf("the absolute path reached the append-only provenance record: %s", recorded)
	}
}

// TestAMissingInTreeBlocklistNamesTheBaseRef keeps the failure legible. An
// operator who configured a blocklist inside the repository and has it nowhere
// gets an error that names the requirement the boundary added, not one that
// names git plumbing.
func TestAMissingInTreeBlocklistNamesTheBaseRef(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	writeFile(t, dir, ".no-slop.yaml", blocklistGateConfig)
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	attachRemote(t, dir)
	runGit(t, dir, "switch", "-c", "docs/notes")
	writeFile(t, dir, "docs/notes.txt", "notes\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "notes")

	code, output := runGateIn(t, dir)
	if code != 2 {
		t.Fatalf("exit = %d, want 2: a configured blocklist that exists nowhere fails closed\n%s", code, output)
	}
	if !strings.Contains(output, "must be tracked at the base ref") {
		t.Fatalf("the error does not tell the operator where an in-tree blocklist has to live:\n%s", output)
	}
}
