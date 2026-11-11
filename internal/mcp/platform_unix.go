//go:build !windows

package mcp

import (
	"os"
	"syscall"
)

const noFollow = syscall.O_NOFOLLOW

func ownedByMe(st os.FileInfo) bool {
	sys, ok := st.Sys().(*syscall.Stat_t)
	return ok && int(sys.Uid) == os.Getuid()
}
