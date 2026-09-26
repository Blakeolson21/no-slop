//go:build windows

package gatecontext

import (
	"context"
	"unsafe"

	"golang.org/x/sys/windows"
)

func processParents(ctx context.Context) (map[int]int, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, err
	}
	parents := make(map[int]int)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		parents[int(entry.ProcessID)] = int(entry.ParentProcessID)
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if err == windows.ERROR_NO_MORE_FILES {
				return parents, nil
			}
			return nil, err
		}
	}
}
