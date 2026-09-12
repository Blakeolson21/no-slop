//go:build unix

package cli

import (
	"io/fs"
	"syscall"
)

func fileOwnerUID(info fs.FileInfo) (int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(stat.Uid), true
}
