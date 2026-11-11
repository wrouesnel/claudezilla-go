// Package client talks to the native host over its socket: one request per
// connection, authenticated with the token the host wrote at startup.
package client

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/wrouesnel/claudezilla-go/internal/ipc"
)

// Response is the host's reply.
type Response struct {
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   string          `json:"error,omitempty"`
	Details map[string]any  `json:"details,omitempty"`
}

// Error codes worth retrying: the host is (re)starting.
const (
	CodeNoToken    = "NO_AUTH_TOKEN"
	CodeNotFound   = "ENOENT"
	CodeRefused    = "ECONNREFUSED"
	CodeReset      = "ECONNRESET"
	CodeHostClosed = "HOST_CLOSED"
)

// Error carries a code alongside a human message.
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// Retryable reports whether err is a transient connection failure.
func Retryable(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	return e.Code == CodeNotFound || e.Code == CodeRefused || e.Code == CodeReset
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// Describe builds the actionable message for a connection error code.
func Describe(code string, paths ipc.Paths) string {
	diag := fmt.Sprintf("[Socket: %s, Auth: %s]", existsWord(paths.Socket), existsWord(paths.Token))
	switch code {
	case CodeNoToken:
		return "Claudezilla extension not connected. " + diag + "\n\nTo fix: Click the Claudezilla extension icon in Firefox to reconnect.\nIf not visible: Firefox menu → Add-ons → Extensions → Enable Claudezilla"
	case CodeNotFound:
		return "Claudezilla socket not found. " + diag + "\n\nTo fix: Click the Claudezilla extension icon in Firefox to reconnect.\nIf Firefox isn't running: Open Firefox first, then click the extension icon."
	case CodeRefused:
		return "Claudezilla connection refused. " + diag + "\n\nThe socket exists but the host isn't accepting connections.\nTo fix: Reload the Claudezilla extension (about:addons → Claudezilla → Reload)"
	case CodeReset:
		return "Claudezilla connection reset. " + diag + "\n\nThe host disconnected unexpectedly.\nTo fix: Click the Claudezilla extension icon in Firefox to reconnect."
	}
	return fmt.Sprintf("Claudezilla error: %s. %s", code, diag)
}

func existsWord(p string) string {
	if exists(p) {
		return "exists"
	}
	return "missing"
}

// LoadToken reads the host's token; "" when the host isn't running.
func LoadToken(paths ipc.Paths) string {
	b, err := os.ReadFile(paths.Token)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func codeOf(err error) string {
	switch {
	case errors.Is(err, syscall.ENOENT), errors.Is(err, os.ErrNotExist):
		return CodeNotFound
	case errors.Is(err, syscall.ECONNREFUSED):
		return CodeRefused
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE):
		return CodeReset
	}
	return err.Error()
}

// Request sends one command and waits for the first response line. timeout
// is the socket idle limit; 0 means the command's own timeout plus grace.
func Request(paths ipc.Paths, token, command string, params map[string]any, timeout time.Duration) (*Response, error) {
	if timeout <= 0 {
		timeout = time.Duration(ipc.SocketTimeoutMs(params)) * time.Millisecond
	}
	conn, err := net.DialTimeout("unix", paths.Socket, 5*time.Second)
	if err != nil {
		code := codeOf(err)
		return nil, &Error{Code: code, Msg: Describe(code, paths)}
	}
	defer conn.Close()

	if params == nil {
		params = map[string]any{}
	}
	msg, err := json.Marshal(map[string]any{"command": command, "params": params, "authToken": token})
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(append(msg, '\n')); err != nil {
		code := codeOf(err)
		return nil, &Error{Code: code, Msg: Describe(code, paths)}
	}
	r := bufio.NewReaderSize(conn, 64*1024)
	var line []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		line = append(line, chunk...)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return nil, fmt.Errorf("Connection timed out after %dms (command: %s)", timeout.Milliseconds(), command)
			}
			if len(line) > 0 {
				break // closed after a response without a trailing newline
			}
			if code := codeOf(err); code == CodeReset {
				return nil, &Error{Code: code, Msg: Describe(code, paths)}
			}
			return nil, &Error{Code: CodeHostClosed, Msg: fmt.Sprintf("Claudezilla host closed the connection without responding (command: %s). The extension may never have answered — check the host debug log.", command)}
		}
		if !isPrefix {
			break
		}
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("Invalid response from Claudezilla host: %v", err)
	}
	return &resp, nil
}

// Probe reports whether something accepts connections on the socket.
func Probe(socket string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("unix", socket, timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
