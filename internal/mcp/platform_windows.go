//go:build windows

package mcp

import "os"

const noFollow = 0

func ownedByMe(os.FileInfo) bool { return true }
