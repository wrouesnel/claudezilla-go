// Command claudezilla-host is the native messaging host Firefox starts for
// the Claudezilla extension. It bridges the extension (stdin/stdout, native
// messaging framing) and local clients such as claudezilla-mcp (a Unix
// socket, one JSON line per request, authenticated by a token that is
// regenerated every time the host starts).
package main

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wrouesnel/claudezilla-go/internal/debuglog"
	"github.com/wrouesnel/claudezilla-go/internal/ipc"
	"github.com/wrouesnel/claudezilla-go/internal/nativemsg"
	"github.com/wrouesnel/claudezilla-go/internal/version"
)

const (
	maxBufferSize  = 10 * 1024 * 1024
	maxConnections = 10
	idleTimeout    = 60 * time.Second
)

var allowedCommands = map[string]bool{}

func init() {
	for _, c := range strings.Fields(`ping version navigate getActiveTab getContent click type screenshot
		getTabs listAllTabs attachTab detachTab closeTab createWindow closeWindow getWindows resizeWindow
		setViewport getConsoleLogs getNetworkRequests scroll waitFor evaluate getElementInfo getPageState
		getAccessibilitySnapshot find pressKey startLoop stopLoop getLoopState incrementLoopIteration
		requestTabSpace grantTabSpace getSlotRequests cleanupOrphanedTabs goodbye quitBrowser
		handleConsent getWindowMode`) {
		allowedCommands[c] = true
	}
}

type response = map[string]any

type host struct {
	paths ipc.Paths
	token string
	ext   *nativemsg.Writer

	mu      sync.Mutex
	pending map[string]func(response)

	loops *loopStore
}

func (h *host) log(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(os.Stderr, "[claudezilla-host]", msg)
	if h.paths.DebugLog != "" {
		_ = debuglog.Append(h.paths.DebugLog,
			fmt.Sprintf("[%s] [claudezilla-host] %s\n", time.Now().UTC().Format(time.RFC3339Nano), msg),
			debuglog.DefaultMaxBytes)
	}
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// handleClient dispatches one authenticated socket request. reply is called
// exactly once, unless the client disconnects first.
func (h *host) handleClient(command string, params map[string]any, authToken string, reply func(response), owned *ownedIDs) {
	if len(authToken) != len(h.token) || subtle.ConstantTimeCompare([]byte(authToken), []byte(h.token)) != 1 {
		reply(response{"success": false, "error": "Invalid or missing auth token"})
		return
	}
	if !allowedCommands[command] {
		reply(response{"success": false, "error": "Command not allowed: " + command})
		return
	}
	if command == "quitBrowser" && !ipc.QuitBrowserAllowed() {
		reply(response{"success": false, "error": "quitBrowser is disabled. Set CLAUDEZILLA_ALLOW_QUIT=1 in the environment of the browser process (the host inherits it from Firefox, not from the CLI) and restart that browser."})
		return
	}
	switch command {
	case "startLoop", "stopLoop", "getLoopState", "incrementLoopIteration":
		reply(h.loops.handle(command, params, h.log))
		return
	}

	id := newID()
	timeoutMs := ipc.CommandTimeoutMs(params)
	var once sync.Once
	settle := func(r response) { once.Do(func() { reply(r) }) }

	h.mu.Lock()
	h.pending[id] = settle
	h.mu.Unlock()
	owned.add(id)

	time.AfterFunc(time.Duration(timeoutMs)*time.Millisecond, func() {
		if h.take(id) != nil {
			owned.remove(id)
			settle(response{"success": false, "error": fmt.Sprintf("Request timed out after %dms (command: %s)", timeoutMs, command), "command": command, "timeoutMs": timeoutMs})
		}
	})

	h.log("Forwarding CLI command to extension: %s", command)
	if err := h.ext.Write(map[string]any{"id": id, "type": "command", "command": command, "params": params}); err != nil {
		if h.take(id) != nil {
			owned.remove(id)
			settle(response{"success": false, "error": "Could not send to extension: " + err.Error()})
		}
	}
}

func (h *host) take(id string) func(response) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cb := h.pending[id]
	delete(h.pending, id)
	return cb
}

// extMessage is a message from the extension. Result and details stay raw,
// so the extension's key order reaches the agent unchanged.
type extMessage struct {
	ID      any             `json:"id"`
	Command string          `json:"command"`
	Success *bool           `json:"success"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
	Details json.RawMessage `json:"details"`
}

func (h *host) handleExtensionMessage(raw json.RawMessage) {
	var m extMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		h.log("Invalid message from extension: %v", err)
		return
	}
	id, _ := m.ID.(string)
	state := "event"
	if m.Success != nil {
		state = "error"
		if *m.Success {
			state = "ok"
		}
	}
	h.log("Received from extension: %s %s %s", orDash(id), m.Command, state)

	if m.Success != nil && id != "" {
		if cb := h.take(id); cb != nil {
			r := response{"success": *m.Success}
			if len(m.Result) > 0 {
				r["result"] = m.Result
			}
			if len(m.Error) > 0 {
				r["error"] = m.Error
			}
			if len(m.Details) > 0 {
				r["details"] = m.Details
			}
			cb(r)
			return
		}
	}
	switch m.Command {
	case "ping":
		_ = h.ext.Write(map[string]any{"id": m.ID, "success": true, "result": map[string]any{"pong": true, "timestamp": time.Now().UnixMilli()}})
	case "version":
		_ = h.ext.Write(map[string]any{"id": m.ID, "success": true, "result": map[string]any{
			"host":     version.Version,
			"runtime":  runtime.Version(),
			"platform": runtime.GOOS,
			"features": []string{"security-hardened", "focus-loop", "auto-retry", "task-detection", "expression-validation", "windows-support", "session-scoped-loops", "activation-recovery", "go"},
		}})
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ownedIDs tracks the requests a connection is waiting on, so they can be
// dropped when it disconnects.
type ownedIDs struct {
	mu  sync.Mutex
	ids map[string]bool
}

func (o *ownedIDs) add(id string)    { o.mu.Lock(); o.ids[id] = true; o.mu.Unlock() }
func (o *ownedIDs) remove(id string) { o.mu.Lock(); delete(o.ids, id); o.mu.Unlock() }

func (h *host) serveConn(conn net.Conn) {
	defer conn.Close()
	h.log("CLI client connected")
	owned := &ownedIDs{ids: map[string]bool{}}
	defer func() {
		owned.mu.Lock()
		for id := range owned.ids {
			h.take(id)
		}
		owned.mu.Unlock()
		h.log("CLI client disconnected")
	}()

	var writeMu sync.Mutex
	write := func(r response) {
		b, _ := json.Marshal(r)
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		_, _ = conn.Write(append(b, '\n'))
	}

	timeout := idleTimeout
	reader := bufio.NewReaderSize(conn, 64*1024)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
		line, err := readLine(reader, maxBufferSize)
		if errors.Is(err, errTooLarge) {
			h.log("Buffer overflow attempt - disconnecting client")
			write(response{"success": false, "error": "Message too large"})
			return
		}
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				h.log("Socket idle timeout (%s) - closing", timeout)
			}
			return
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		var req struct {
			Command   any            `json:"command"`
			Params    map[string]any `json:"params"`
			AuthToken string         `json:"authToken"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			h.log("Invalid CLI message: %v", err)
			write(response{"success": false, "error": "Invalid JSON"})
			continue
		}
		command, ok := req.Command.(string)
		if !ok || command == "" {
			write(response{"success": false, "error": "Invalid or missing command"})
			continue
		}
		if req.Params == nil {
			req.Params = map[string]any{}
		}
		h.log("CLI command: %s", command)
		// Cover the command's own timeout, so the host's timeout reply is sent
		// before the socket's idle limit closes the connection.
		timeout = time.Duration(ipc.SocketTimeoutMs(req.Params)) * time.Millisecond
		h.handleClient(command, req.Params, req.AuthToken, write, owned)
	}
}

var errTooLarge = errors.New("line too large")

func readLine(r *bufio.Reader, max int) (string, error) {
	var sb strings.Builder
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return "", err
		}
		if sb.Len()+len(chunk) > max {
			return "", errTooLarge
		}
		sb.Write(chunk)
		if !isPrefix {
			return sb.String(), nil
		}
	}
}

func (h *host) listen() (net.Listener, error) {
	_ = os.Remove(h.paths.Socket)
	ln, err := net.Listen("unix", h.paths.Socket)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(h.paths.Socket, 0o600); err != nil {
		h.log("Warning: could not chmod socket: %v", err)
	}
	h.log("Socket server listening on %s", h.paths.Socket)

	_ = os.Remove(h.paths.Token)
	f, err := os.OpenFile(h.paths.Token, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		h.log("Warning: could not write auth token file: %v", err)
	} else {
		_, _ = f.WriteString(h.token)
		_ = f.Close()
		h.log("Auth token written to %s", h.paths.Token)
	}

	go func() {
		sem := make(chan struct{}, maxConnections)
		for {
			conn, err := ln.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				h.log("Server error: %v", err)
				continue
			}
			select {
			case sem <- struct{}{}:
				go func() { defer func() { <-sem }(); h.serveConn(conn) }()
			default:
				h.log("Too many connections - refusing client")
				conn.Close()
			}
		}
	}()
	return ln, nil
}

func (h *host) cleanup() {
	_ = os.Remove(h.paths.Socket)
	_ = os.Remove(h.paths.Token)
}

func main() {
	paths, err := ipc.Resolve()
	if err != nil {
		fmt.Fprintln(os.Stderr, "[claudezilla-host]", err)
		os.Exit(1)
	}
	var tok [32]byte
	if _, err := rand.Read(tok[:]); err != nil {
		fmt.Fprintln(os.Stderr, "[claudezilla-host] no randomness:", err)
		os.Exit(1)
	}
	h := &host{
		paths:   paths,
		token:   hex.EncodeToString(tok[:]),
		ext:     nativemsg.NewWriter(os.Stdout),
		pending: map[string]func(response){},
		loops:   newLoopStore(),
	}
	h.log("Host %s started (%s)", version.Version, runtime.Version())

	ln, err := h.listen()
	if err != nil {
		h.log("Could not listen on %s: %v", paths.Socket, err)
		os.Exit(1)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		s := <-sig
		h.log("Received %s", s)
		ln.Close()
		h.cleanup()
		os.Exit(0)
	}()

	h.log("Starting native messaging loop")
	in := bufio.NewReaderSize(os.Stdin, 64*1024)
	for {
		m, err := nativemsg.ReadRaw(in)
		if err == io.EOF {
			h.log("Extension disconnected (EOF)")
			break
		}
		if err != nil {
			h.log("Native messaging error: %v", err)
			break
		}
		h.handleExtensionMessage(m)
	}
	h.log("Host exiting")
	ln.Close()
	h.cleanup()
}
