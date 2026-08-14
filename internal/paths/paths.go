package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/identity"
)

// Paths provides access to all no-slop filesystem locations.
// The root defaults to ~/.no-mistakes but can be overridden via NS_HOME or
// its NM_HOME compatibility alias
// or by using WithRoot (for testing).
type Paths struct {
	root string
}

// New returns Paths rooted at NS_HOME, NM_HOME, or ~/.no-mistakes.
func New() (*Paths, error) {
	env, err := LookupHomeEnv()
	if err != nil {
		return nil, err
	}
	if env != "" {
		return &Paths{root: env}, nil
	}
	allowDefault, err := identity.EnvEnabled(identity.AllowDefaultRootInTestsEnv, identity.LegacyAllowDefaultRootInTestsEnv)
	if err != nil {
		return nil, err
	}
	if testing.Testing() && !allowDefault {
		return nil, fmt.Errorf("NS_HOME or NM_HOME must be set under go test to avoid touching the real no-slop daemon root")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Paths{root: filepath.Join(home, identity.DefaultStateDir)}, nil
}

func LookupHomeEnv() (string, error) {
	canonicalValue, canonicalSet := os.LookupEnv(identity.HomeEnv)
	legacyValue, legacySet := os.LookupEnv(identity.LegacyHomeEnv)
	if canonicalSet && legacySet && canonicalHomeRoot(canonicalValue) != canonicalHomeRoot(legacyValue) {
		return "", fmt.Errorf("%s and legacy alias %s configure the same setting with different values", identity.HomeEnv, identity.LegacyHomeEnv)
	}
	if canonicalSet {
		return canonicalValue, nil
	}
	return legacyValue, nil
}

func canonicalHomeRoot(root string) string {
	if root == "" {
		return ""
	}
	if !filepath.IsAbs(root) {
		if abs, err := filepath.Abs(root); err == nil {
			root = abs
		}
	}
	root = filepath.Clean(root)
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	} else {
		root = canonicalExistingPrefix(root)
	}
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
	}
	return root
}

func canonicalExistingPrefix(root string) string {
	for candidate := root; ; candidate = filepath.Dir(candidate) {
		if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
			rel, relErr := filepath.Rel(candidate, root)
			if relErr != nil || rel == "." {
				return resolved
			}
			return filepath.Join(resolved, rel)
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return root
		}
	}
}

// WithRoot returns Paths rooted at a custom directory (for testing).
func WithRoot(root string) *Paths {
	return &Paths{root: root}
}

func (p *Paths) Root() string       { return p.root }
func (p *Paths) DB() string         { return filepath.Join(p.root, "state.sqlite") }
func (p *Paths) Socket() string     { return filepath.Join(p.root, "socket") }
func (p *Paths) PIDFile() string    { return filepath.Join(p.root, "daemon.pid") }
func (p *Paths) ConfigFile() string { return filepath.Join(p.root, "config.yaml") }

// LockFile is the OS-level advisory lock used to enforce a single live daemon
// per NS_HOME (see the singleton lock in internal/daemon). Distinct from
// PIDFile, which is an informational record a live daemon writes for
// CLI/status consumers: LockFile is what actually prevents two daemons from
// ever running startup recovery or binding the socket concurrently for the
// same root.
func (p *Paths) LockFile() string { return filepath.Join(p.root, "daemon.lock") }
func (p *Paths) UpdateCheckFile() string {
	return filepath.Join(p.root, "update-check.json")
}

// TelemetryGateFile persists the read-surface telemetry dedupe state so
// high-frequency status polling stays rate-limited across CLI processes.
func (p *Paths) TelemetryGateFile() string {
	return filepath.Join(p.root, "telemetry-gate.json")
}

// LaneHealthFile persists which configured agent lanes are quota-exhausted and
// when each recovers, so concurrent runs and later runs skip a dead lane
// instead of each paying an agent spawn to rediscover it.
func (p *Paths) LaneHealthFile() string {
	return filepath.Join(p.root, "lane-health.json")
}

func (p *Paths) ReposDir() string { return filepath.Join(p.root, "repos") }
func (p *Paths) RepoDir(repoID string) string {
	return filepath.Join(p.root, "repos", repoID+".git")
}

func (p *Paths) WorktreesDir() string { return filepath.Join(p.root, "worktrees") }
func (p *Paths) WorktreeDir(repoID, runID string) string {
	return filepath.Join(p.root, "worktrees", repoID, runID)
}

func (p *Paths) LogsDir() string { return filepath.Join(p.root, "logs") }
func (p *Paths) RunLogDir(runID string) string {
	return filepath.Join(p.root, "logs", runID)
}
func (p *Paths) DaemonLog() string { return filepath.Join(p.root, "logs", "daemon.log") }

// DaemonBootstrapLog captures service-manager output before the daemon logger
// is ready, plus crash diagnostics written directly to stdout or stderr.
func (p *Paths) DaemonBootstrapLog() string {
	return filepath.Join(p.root, "logs", "daemon-bootstrap.log")
}

// ManagedServerLog holds raw output from daemon-managed agent servers.
func (p *Paths) ManagedServerLog() string {
	return filepath.Join(p.root, "logs", "managed-server.log")
}
func (p *Paths) CLILog() string { return filepath.Join(p.root, "logs", "cli.log") }

// ServerPIDsDir holds PID-tracking files for managed agent servers
// (opencode, rovodev) so a freshly started daemon can reap orphans left
// behind by a crashed predecessor.
func (p *Paths) ServerPIDsDir() string { return filepath.Join(p.root, "servers") }

// ProcTreesDir holds one record per live command leader so a freshly started
// daemon can reap process trees a crashed predecessor left running.
//
// This is the step/agent-subprocess counterpart to ServerPIDsDir, which only
// covers long-lived managed servers that write a PID file of their own.
func (p *Paths) ProcTreesDir() string { return filepath.Join(p.root, "proctrees") }

// EnsureDirs creates all required directories under root.
func (p *Paths) EnsureDirs() error {
	dirs := []string{
		p.root,
		p.ReposDir(),
		p.WorktreesDir(),
		p.LogsDir(),
		p.ServerPIDsDir(),
		p.ProcTreesDir(),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
