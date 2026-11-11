// Package ipc resolves where the native host and the MCP server meet: the
// Unix socket, the auth token file and the host debug log, plus the shared
// command-timeout rules. Both binaries must resolve the same paths, so this
// is the only place they are computed.
package ipc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	DefaultCommandTimeoutMs = 150000
	MinCommandTimeoutMs     = 5000
	MaxCommandTimeoutMs     = 300000
	// SocketTimeoutGraceMs is added on top of a command's own timeout for the
	// socket, so the host's "timed out" reply arrives before the socket gives up.
	SocketTimeoutGraceMs = 5000

	SocketName   = "claudezilla.sock"
	TokenName    = "claudezilla-auth.token"
	DebugLogName = "claudezilla-debug.log"
)

// CommandTimeoutMs returns params._timeout when it is in range, else the default.
func CommandTimeoutMs(params map[string]any) int {
	if params != nil {
		if v, ok := params["_timeout"].(float64); ok && v >= MinCommandTimeoutMs && v <= MaxCommandTimeoutMs {
			return int(v)
		}
		if v, ok := params["_timeout"].(int); ok && v >= MinCommandTimeoutMs && v <= MaxCommandTimeoutMs {
			return v
		}
	}
	return DefaultCommandTimeoutMs
}

// SocketTimeoutMs is the socket idle timeout for a command.
func SocketTimeoutMs(params map[string]any) int {
	return CommandTimeoutMs(params) + SocketTimeoutGraceMs
}

// QuitBrowserAllowed reports whether CLAUDEZILLA_ALLOW_QUIT is set in the
// environment of the process (the host inherits it from Firefox).
func QuitBrowserAllowed() bool {
	v := os.Getenv("CLAUDEZILLA_ALLOW_QUIT")
	return v == "1" || strings.EqualFold(v, "true")
}

// ValidatePath rejects empty paths, NUL bytes and "..".
func ValidatePath(p, context string) error {
	switch {
	case p == "":
		return fmt.Errorf("%s is empty or invalid", context)
	case strings.ContainsRune(p, 0):
		return fmt.Errorf("%s contains null byte", context)
	case strings.Contains(p, ".."):
		return fmt.Errorf("%s contains path traversal", context)
	case runtime.GOOS == "windows" && strings.HasPrefix(p, `\\`):
		return fmt.Errorf("%s is a UNC network path", context)
	}
	return nil
}

func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// Dir returns the private directory that holds the socket, token and log.
//
// Order: CLAUDEZILLA_IPC_DIR, then $XDG_RUNTIME_DIR, then ~/.claudezilla
// (%LOCALAPPDATA%\claudezilla on Windows). Never a shared temp directory,
// where another local user could pre-create or symlink the files.
func Dir() (string, error) {
	if d := os.Getenv("CLAUDEZILLA_IPC_DIR"); d != "" {
		if err := ValidatePath(d, "CLAUDEZILLA_IPC_DIR"); err != nil {
			fmt.Fprintf(os.Stderr, "[claudezilla] Skipping invalid CLAUDEZILLA_IPC_DIR: %v\n", err)
		} else if err := ensurePrivateDir(d); err != nil {
			fmt.Fprintf(os.Stderr, "[claudezilla] Skipping invalid CLAUDEZILLA_IPC_DIR: %v\n", err)
		} else {
			return d, nil
		}
	}
	if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local")
		}
		d := filepath.Join(base, "claudezilla")
		if err := ValidatePath(d, "LOCALAPPDATA"); err != nil {
			return "", err
		}
		return d, os.MkdirAll(d, 0o700)
	}
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
		if err := ValidatePath(x, "XDG_RUNTIME_DIR"); err == nil {
			if st, err := os.Stat(x); err == nil && st.IsDir() {
				return x, nil
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		d := filepath.Join(home, ".claudezilla")
		if err := ensurePrivateDir(d); err == nil {
			return d, nil
		}
	}
	return "", errors.New("no private IPC directory: set XDG_RUNTIME_DIR or CLAUDEZILLA_IPC_DIR, or make ~/.claudezilla writable")
}

// Paths holds the resolved IPC file locations.
type Paths struct {
	Dir, Socket, Token, DebugLog string
}

// Resolve returns the IPC paths for this process's environment.
func Resolve() (Paths, error) {
	d, err := Dir()
	if err != nil {
		return Paths{}, err
	}
	return Paths{
		Dir:      d,
		Socket:   filepath.Join(d, SocketName),
		Token:    filepath.Join(d, TokenName),
		DebugLog: filepath.Join(d, DebugLogName),
	}, nil
}
