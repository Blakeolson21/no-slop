//go:build linux

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/paths"
	"github.com/Blakeolson21/no-slop/internal/proctree"
)

// PID NULL is not proof of no worker: a crash may leave an orphaned subprocess.
// Inspect same-user process cwd without reading argv/environment or signalling
// anything. The daemon registry covers workers without a cwd in the worktree.
func phantomWorkerOwners(p *paths.Paths, rows []db.PhantomRun) (map[string]string, error) {
	owners := map[string]string{}
	worktrees := make(map[string]string, len(rows))
	for _, r := range rows {
		work, err := canonicalProcessPath(p.WorktreeDir(r.RepoID, r.ID))
		if err != nil {
			return nil, fmt.Errorf("canonicalize worktree %s: %w", r.ID, err)
		}
		worktrees[r.ID] = work
	}
	processes, err := proctree.Snapshot()
	if err != nil {
		return nil, err
	}
	started := map[int]int64{}
	for _, process := range processes {
		started[process.PID] = process.Started.Unix()
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		root := filepath.Join("/proc", entry.Name())
		st, err := os.Stat(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		stat, ok := st.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, fmt.Errorf("cannot inspect process owner")
		}
		if int(stat.Uid) != os.Getuid() {
			continue
		}
		cwd, err := os.Readlink(filepath.Join(root, "cwd"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			// A process born before a run cannot be that run's spawned
			// worker. Shared long-lived executors are fenced by RunManager.
			// Otherwise unreadable cwd is uncertain ownership, never absence.
			for _, r := range rows {
				if started[pid] == 0 || started[pid]+2 >= r.CreatedAt {
					owners[r.ID] = fmt.Sprintf("unreadable possible worker (pid %d)", pid)
				}
			}
			continue
		}
		cwd = strings.TrimSuffix(cwd, " (deleted)")
		cwd, err = canonicalProcessPath(cwd)
		if err != nil {
			return nil, fmt.Errorf("canonicalize worker cwd for pid %d: %w", pid, err)
		}
		for _, r := range rows {
			work := worktrees[r.ID]
			if cwd == work || strings.HasPrefix(cwd, work+string(os.PathSeparator)) {
				owners[r.ID] = fmt.Sprintf("live worker cwd (pid %d)", pid)
			}
		}
	}
	return owners, nil
}

func canonicalProcessPath(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	for candidate := abs; ; candidate = filepath.Dir(candidate) {
		resolved, resolveErr := filepath.EvalSymlinks(candidate)
		if resolveErr == nil {
			rel, relErr := filepath.Rel(candidate, abs)
			if relErr != nil {
				return "", relErr
			}
			if rel == "." {
				return filepath.Clean(resolved), nil
			}
			return filepath.Clean(filepath.Join(resolved, rel)), nil
		}
		if !os.IsNotExist(resolveErr) {
			return "", resolveErr
		}
		if _, statErr := os.Lstat(candidate); statErr == nil {
			return "", resolveErr
		} else if !os.IsNotExist(statErr) {
			return "", statErr
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", resolveErr
		}
	}
}

func phantomStoreIdentity(info os.FileInfo) string {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("dev=%d ino=%d", stat.Dev, stat.Ino)
	}
	return fmt.Sprintf("size=%d mtime=%s", info.Size(), info.ModTime().UTC())
}
