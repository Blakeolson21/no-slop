//go:build !linux

package daemon

import (
	"fmt"
	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/paths"
	"os"
)

func phantomWorkerOwners(_ *paths.Paths, _ []db.PhantomRun) (map[string]string, error) {
	return nil, fmt.Errorf("phantom repair requires Linux worker inspection")
}

func phantomStoreIdentity(info os.FileInfo) string {
	return fmt.Sprintf("size=%d mtime=%s", info.Size(), info.ModTime().UTC())
}
