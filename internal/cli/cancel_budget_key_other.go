//go:build !unix

package cli

import "io/fs"

// Without POSIX ownership there is no operator-owned-path binding to verify, so
// the key must be pinned by the installed operator policy instead.
func fileOwnerUID(fs.FileInfo) (int, bool) { return 0, false }
