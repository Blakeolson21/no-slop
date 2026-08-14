package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWithRoot(t *testing.T) {
	root := filepath.Join("tmp", "ns-test")
	p := WithRoot(root)

	if got := p.Root(); got != root {
		t.Errorf("Root() = %q, want %q", got, root)
	}
	if got := p.DB(); got != filepath.Join(root, "state.sqlite") {
		t.Errorf("DB() = %q, want %q", got, filepath.Join(root, "state.sqlite"))
	}
	if got := p.Socket(); got != filepath.Join(root, "socket") {
		t.Errorf("Socket() = %q, want %q", got, filepath.Join(root, "socket"))
	}
	if got := p.PIDFile(); got != filepath.Join(root, "daemon.pid") {
		t.Errorf("PIDFile() = %q, want %q", got, filepath.Join(root, "daemon.pid"))
	}
	if got := p.ConfigFile(); got != filepath.Join(root, "config.yaml") {
		t.Errorf("ConfigFile() = %q, want %q", got, filepath.Join(root, "config.yaml"))
	}
}

func TestRepoPaths(t *testing.T) {
	root := filepath.Join("tmp", "ns-test")
	p := WithRoot(root)

	if got := p.ReposDir(); got != filepath.Join(root, "repos") {
		t.Errorf("ReposDir() = %q", got)
	}
	if got := p.RepoDir("abc123"); got != filepath.Join(root, "repos", "abc123.git") {
		t.Errorf("RepoDir() = %q", got)
	}
}

func TestWorktreePaths(t *testing.T) {
	root := filepath.Join("tmp", "ns-test")
	p := WithRoot(root)

	if got := p.WorktreesDir(); got != filepath.Join(root, "worktrees") {
		t.Errorf("WorktreesDir() = %q", got)
	}
	if got := p.WorktreeDir("repo1", "run1"); got != filepath.Join(root, "worktrees", "repo1", "run1") {
		t.Errorf("WorktreeDir() = %q", got)
	}
}

func TestLogPaths(t *testing.T) {
	root := filepath.Join("tmp", "ns-test")
	p := WithRoot(root)

	if got := p.LogsDir(); got != filepath.Join(root, "logs") {
		t.Errorf("LogsDir() = %q", got)
	}
	if got := p.RunLogDir("run1"); got != filepath.Join(root, "logs", "run1") {
		t.Errorf("RunLogDir() = %q", got)
	}
	if got := p.DaemonLog(); got != filepath.Join(root, "logs", "daemon.log") {
		t.Errorf("DaemonLog() = %q", got)
	}
	if got := p.DaemonBootstrapLog(); got != filepath.Join(root, "logs", "daemon-bootstrap.log") {
		t.Errorf("DaemonBootstrapLog() = %q", got)
	}
	if got := p.ManagedServerLog(); got != filepath.Join(root, "logs", "managed-server.log") {
		t.Errorf("ManagedServerLog() = %q", got)
	}
}

func TestNewWithEnvOverride(t *testing.T) {
	dir := t.TempDir()
	unsetEnv(t, "NS_HOME")
	t.Setenv("NM_HOME", dir)

	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if p.Root() != dir {
		t.Errorf("Root() = %q, want %q", p.Root(), dir)
	}
}

func TestNewWithCanonicalEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NS_HOME", dir)
	unsetEnv(t, "NM_HOME")

	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if p.Root() != dir {
		t.Errorf("Root() = %q, want %q", p.Root(), dir)
	}
}

func TestNewTreatsRootEnvAliasesAsOneSetting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NS_HOME", dir)
	t.Setenv("NM_HOME", dir)

	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if p.Root() != dir {
		t.Errorf("Root() = %q, want %q", p.Root(), dir)
	}

	t.Setenv("NM_HOME", filepath.Join(dir, "different"))
	if _, err := New(); err == nil {
		t.Fatal("New() should reject conflicting NS_HOME and NM_HOME values")
	}
}

func TestNewTreatsEquivalentRootEnvAliasesAsOneSetting(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real-root")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasRoot := filepath.Join(base, "alias-root")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Skipf("symlink setup unavailable: %v", err)
	}

	t.Setenv("NS_HOME", realRoot)
	t.Setenv("NM_HOME", aliasRoot)
	p, err := New()
	if err != nil {
		t.Fatalf("New() with canonical real root and legacy alias root: %v", err)
	}
	if p.Root() != realRoot {
		t.Fatalf("Root() = %q, want canonical env value %q", p.Root(), realRoot)
	}

	t.Setenv("NS_HOME", aliasRoot)
	t.Setenv("NM_HOME", realRoot)
	p, err = New()
	if err != nil {
		t.Fatalf("New() with canonical alias root and legacy real root: %v", err)
	}
	if p.Root() != aliasRoot {
		t.Fatalf("Root() = %q, want canonical env value %q", p.Root(), aliasRoot)
	}
}

func TestNewRejectsEmptyRootEnvAliasConflict(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NS_HOME", "")
	t.Setenv("NM_HOME", dir)
	if _, err := New(); err == nil {
		t.Fatal("New() should reject empty canonical root conflicting with legacy root")
	}

	t.Setenv("NS_HOME", dir)
	t.Setenv("NM_HOME", "")
	if _, err := New(); err == nil {
		t.Fatal("New() should reject canonical root conflicting with empty legacy root")
	}
}

func TestNewRejectsDefaultRootInTests(t *testing.T) {
	t.Setenv("NS_HOME", "")
	t.Setenv("NM_HOME", "")
	t.Setenv("NS_ALLOW_DEFAULT_ROOT_IN_TESTS", "")
	t.Setenv("NO_MISTAKES_ALLOW_DEFAULT_ROOT_IN_TESTS", "")

	_, err := New()
	if err == nil {
		t.Fatal("New() should reject the default root under go test")
	}
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	oldValue, hadValue := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadValue {
			_ = os.Setenv(key, oldValue)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func TestNewDefault(t *testing.T) {
	t.Setenv("NS_HOME", "")
	t.Setenv("NM_HOME", "")
	t.Setenv("NS_ALLOW_DEFAULT_ROOT_IN_TESTS", "1")
	t.Setenv("NO_MISTAKES_ALLOW_DEFAULT_ROOT_IN_TESTS", "1")

	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".no-mistakes")
	if p.Root() != want {
		t.Errorf("Root() = %q, want %q", p.Root(), want)
	}
}

func TestEnsureDirs(t *testing.T) {
	dir := t.TempDir()
	p := WithRoot(filepath.Join(dir, "nm"))

	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	for _, d := range []string{p.Root(), p.ReposDir(), p.WorktreesDir(), p.LogsDir(), p.ServerPIDsDir()} {
		info, err := os.Stat(d)
		if err != nil {
			t.Errorf("expected dir %q to exist: %v", d, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("expected %q to be a directory", d)
		}
	}
}
