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
		for _, r := range rows {
			work := p.WorktreeDir(r.RepoID, r.ID)
			if cwd == work || strings.HasPrefix(cwd, work+string(os.PathSeparator)) {
				owners[r.ID] = fmt.Sprintf("live worker cwd (pid %d)", pid)
			}
		}
	}
	return owners, nil
}

func phantomStoreIdentity(info os.FileInfo) string {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("dev=%d ino=%d", stat.Dev, stat.Ino)
	}
	return fmt.Sprintf("size=%d mtime=%s", info.Size(), info.ModTime().UTC())
}
