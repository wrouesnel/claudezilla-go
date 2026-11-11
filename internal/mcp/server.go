// Package mcp is the Claudezilla MCP server: JSON-RPC 2.0 over stdio
// (newline-delimited), exposing the firefox_* tools and forwarding them to
// the native host. It has no dependencies beyond the standard library.
package mcp

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wrouesnel/claudezilla-go/internal/client"
	"github.com/wrouesnel/claudezilla-go/internal/ipc"
	"github.com/wrouesnel/claudezilla-go/internal/mcp/catalog"
	"github.com/wrouesnel/claudezilla-go/internal/version"
)

// Protocol versions this server speaks, newest first.
var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05", "2024-10-07"}

var (
	sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9_\-:.]+$`)
	staleTabRe  = regexp.MustCompile(`(?i)TAB_UNAVAILABLE|Tab \d+ not found|Invalid tab ID`)
)

var (
	tabDefaultExcluded = []string{"firefox_close_tab", "firefox_attach_tab", "firefox_detach_tab", "firefox_create_window"}
	textBodyTools      = []string{"firefox_get_content", "firefox_get_accessibility_snapshot", "firefox_find"}
	loopTools          = []string{"firefox_start_loop", "firefox_stop_loop", "firefox_loop_status"}
)

const (
	retryMax       = 3
	retryInitial   = 100 * time.Millisecond
	retryMaxDelay  = time.Second
	goodbyeTimeout = 2 * time.Second
)

// Options configure a Server.
type Options struct {
	Args []string // command-line arguments (for --tools, --resume, --session-id)
	Log  func(string)
}

type agentConfig struct {
	AllowedDomains json.RawMessage `json:"allowedDomains,omitempty"`
	HandleConsent  json.RawMessage `json:"handleConsent,omitempty"`
}

// Server is one MCP server process, i.e. one agent.
type Server struct {
	paths     ipc.Paths
	agentID   string
	sessionID string
	lazy      bool
	log       func(string)

	tools       []catalog.Tool
	byName      map[string]catalog.Tool
	tabDefault  map[string]bool
	autostart   *client.Autostart
	reaper      *client.Reaper
	screenshots screenshotFiles

	outMu sync.Mutex
	out   io.Writer

	mu          sync.Mutex
	activated   map[string]bool
	isActivated bool
	currentTab  *float64
	config      *agentConfig

	shuttingDown atomic.Bool
	shutdownOnce sync.Once
}

func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func validSession(v string) bool { return v != "" && len(v) <= 128 && sessionIDRe.MatchString(v) }

// resolveSession: --resume, --session-id, then CLAUDEZILLA_SESSION_ID,
// OMP_SESSION_ID, PI_SESSION_ID, HERMES_SESSION_ID, CLAUDE_CODE_SESSION_ID.
func resolveSession(args []string) string {
	for _, f := range []string{"--resume", "--session-id"} {
		if v := argValue(args, f); validSession(v) {
			return v
		}
	}
	for _, e := range []string{"CLAUDEZILLA_SESSION_ID", "OMP_SESSION_ID", "PI_SESSION_ID", "HERMES_SESSION_ID", "CLAUDE_CODE_SESSION_ID"} {
		if v := os.Getenv(e); validSession(v) {
			return v
		}
	}
	return ""
}

// New builds a server for this process.
func New(opts Options) (*Server, error) {
	paths, err := ipc.Resolve()
	if err != nil {
		return nil, err
	}
	logf := opts.Log
	if logf == nil {
		logf = func(m string) { fmt.Fprintln(os.Stderr, m) }
	}
	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, err
	}
	s := &Server{
		paths:      paths,
		agentID:    fmt.Sprintf("agent_%s_%d", hex.EncodeToString(rnd[:]), os.Getpid()),
		sessionID:  resolveSession(opts.Args),
		log:        logf,
		tools:      catalog.Tools(),
		byName:     map[string]catalog.Tool{},
		tabDefault: map[string]bool{},
		autostart:  client.NewAutostart(paths.Socket),
		activated:  map[string]bool{},
	}
	s.screenshots = screenshotFiles{enabled: os.Getenv("CLAUDEZILLA_SCREENSHOT_FILES") == "1", agentID: s.agentID}

	switch mode := argValue(opts.Args, "--tools"); {
	case mode == "lazy":
		s.lazy = true
	case mode == "all":
	case strings.EqualFold(os.Getenv("CLAUDEZILLA_TOOL_MODE"), "lazy"):
		s.lazy = true
	}
	if !s.lazy {
		for _, c := range catalog.CategoryGroups["all"] {
			s.activated[c] = true
		}
		s.isActivated = true
	}

	for _, t := range s.tools {
		s.byName[t.Name] = t
		if _, has := t.InputSchema.Properties["tabId"]; has &&
			!slices.Contains(t.InputSchema.Required, "tabId") && !slices.Contains(tabDefaultExcluded, t.Name) {
			s.tabDefault[t.Name] = true
		}
	}

	disabled, timeout := client.ResolveAgentTimeout()
	s.reaper = &client.Reaper{
		Timeout:  timeout,
		Disabled: disabled,
		OnCleanup: func(agentID string, idle time.Duration) error {
			s.log(fmt.Sprintf("[claudezilla] Cleaning up orphaned agent: %s (last seen %ds ago)", truncateAgentID(agentID), int(idle.Seconds()+0.5)))
			resp, err := s.sendCommand("cleanupOrphanedTabs", map[string]any{"agentId": agentID}, true, 0)
			if err != nil {
				s.log("[claudezilla] Error cleaning up orphaned agent " + agentID + ": " + err.Error())
				return err
			}
			if resp.Success {
				var r struct {
					TabsClosed int `json:"tabsClosed"`
				}
				_ = json.Unmarshal(resp.Result, &r)
				if r.TabsClosed > 0 {
					s.log(fmt.Sprintf("[claudezilla] Closed %d orphaned tab(s) from %s", r.TabsClosed, truncateAgentID(agentID)))
				}
			}
			return nil
		},
	}
	return s, nil
}

// AgentID is this server's agent identity.
func (s *Server) AgentID() string { return s.agentID }

func truncateAgentID(id string) string {
	if id == "" || id == "unknown" {
		return "unknown"
	}
	if len(id) > 15 {
		return id[:12] + "..."
	}
	return id
}

// ---------------------------------------------------------------------------
// Host communication

func (s *Server) sendCommandOnce(command string, params map[string]any, autostart bool, timeout time.Duration) (*client.Response, error) {
	var startErr error
	if autostart {
		startErr = s.autostart.Ensure()
	}
	withDetail := func(err error) error {
		if startErr == nil {
			return err
		}
		if ce, ok := err.(*client.Error); ok {
			return &client.Error{Code: ce.Code, Msg: ce.Msg + "\n\n" + startErr.Error()}
		}
		return fmt.Errorf("%v\n\n%v", err, startErr)
	}
	token := client.LoadToken(s.paths)
	if token == "" {
		return nil, withDetail(&client.Error{Code: client.CodeNoToken, Msg: client.Describe(client.CodeNoToken, s.paths)})
	}
	resp, err := client.Request(s.paths, token, command, params, timeout)
	if err != nil {
		if client.Retryable(err) {
			s.autostart.MarkHostDown()
		}
		return nil, withDetail(err)
	}
	return resp, nil
}

func (s *Server) sendCommand(command string, params map[string]any, autostart bool, timeout time.Duration) (*client.Response, error) {
	delay := retryInitial
	var last error
	for attempt := 0; attempt <= retryMax; attempt++ {
		resp, err := s.sendCommandOnce(command, params, autostart, timeout)
		if err == nil {
			return resp, nil
		}
		last = err
		if !client.Retryable(err) || attempt == retryMax {
			break
		}
		code := err.(*client.Error).Code
		s.log(fmt.Sprintf("[claudezilla] Connection failed (%s), retrying in %dms... (attempt %d/%d)", code, delay.Milliseconds(), attempt+1, retryMax))
		time.Sleep(delay)
		delay = min(delay*2, retryMaxDelay)
	}
	return nil, last
}

// ---------------------------------------------------------------------------
// JSON-RPC

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) write(v any) {
	b := marshal(v)
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_, _ = io.WriteString(s.out, b+"\n")
}

func (s *Server) reply(id json.RawMessage, result any) {
	s.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *Server) replyError(id json.RawMessage, code int, msg string) {
	s.write(map[string]any{"jsonrpc": "2.0", "id": id, "error": rpcError{code, msg}})
}

func (s *Server) notifyToolsChanged() {
	s.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
}

// Serve reads requests from in until EOF, answering on out. Tool calls run
// concurrently, as clients may issue several at once. It returns at EOF
// without waiting for calls still in flight.
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	s.out = out
	s.reaper.Start()
	s.log(fmt.Sprintf("Claudezilla MCP server running (agent: %s)", truncateAgentID(s.agentID)))

	r := bufio.NewReaderSize(in, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var msg rpcMessage
			if jerr := json.Unmarshal(line, &msg); jerr != nil {
				s.replyError(json.RawMessage("null"), -32700, "Parse error")
			} else if msg.Method != "" {
				if msg.Method == "tools/call" {
					go s.dispatch(msg) // may take minutes (wait_for, slow pages)
				} else {
					s.dispatch(msg) // initialize, tools/list, ping: answered in order
				}
			}
		}
		if err != nil {
			break
		}
	}
	// In-flight calls are abandoned: the client is gone (shutdown follows).
	return nil
}

func (s *Server) dispatch(msg rpcMessage) {
	isRequest := len(msg.ID) > 0 && string(msg.ID) != "null"
	switch msg.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		pv := protocolVersions[0]
		if slices.Contains(protocolVersions, p.ProtocolVersion) {
			pv = p.ProtocolVersion
		}
		instr := catalog.Instructions
		if s.lazy {
			instr = "Call firefox_activate first to load the tools. " + instr
		}
		s.reply(msg.ID, map[string]any{
			"protocolVersion": pv,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
			"serverInfo":      map[string]any{"name": "claudezilla", "version": version.Version},
			"instructions":    instr,
		})
	case "ping":
		s.reply(msg.ID, map[string]any{})
	case "tools/list":
		s.reply(msg.ID, map[string]any{"tools": s.listTools()})
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			s.replyError(msg.ID, -32602, "Invalid params: "+err.Error())
			return
		}
		s.reply(msg.ID, s.callTool(p.Name, p.Arguments))
	default:
		if strings.HasPrefix(msg.Method, "notifications/") || !isRequest {
			return
		}
		s.replyError(msg.ID, -32601, "Method not found")
	}
}

func (s *Server) listTools() []json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isActivated {
		return []json.RawMessage{catalog.Gateway().Raw}
	}
	var out []json.RawMessage
	for _, t := range s.tools {
		if s.activated[catalog.CategoryOf(t.Name)] {
			out = append(out, t.Raw)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Tool calls

type content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

type toolResult struct {
	Content []content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

func textResult(t string) toolResult { return toolResult{Content: []content{{Type: "text", Text: t}}} }
func errorResult(t string) toolResult {
	return toolResult{Content: []content{{Type: "text", Text: t}}, IsError: true}
}

func (s *Server) activate(groups []string) {
	for _, c := range groups {
		s.activated[c] = true
	}
	s.isActivated = true
}

func (s *Server) callTool(name string, args map[string]any) toolResult {
	if args == nil {
		args = map[string]any{}
	}
	if name == "firefox_activate" {
		return s.callActivate(args)
	}

	_, known := s.byName[name]
	category := catalog.CategoryOf(name)
	s.mu.Lock()
	changed := false
	if name != "firefox_diagnose" && !s.isActivated {
		if !known {
			s.mu.Unlock()
			return errorResult("ACTIVATION_REQUIRED: Call firefox_activate first.")
		}
		s.activate(groupFor(category))
		changed = true
		s.log(fmt.Sprintf("[claudezilla] Activation recovery: auto-activated category %q for tool %q", category, name))
	}
	if known && !s.activated[category] {
		s.activate(groupFor(category))
		changed = true
		s.log(fmt.Sprintf("[claudezilla] Category recovery: auto-activated %q for tool %q", category, name))
	}
	s.mu.Unlock()
	if changed {
		s.notifyToolsChanged()
	}

	switch name {
	case "firefox_set_config":
		s.mu.Lock()
		if s.config == nil {
			s.config = &agentConfig{}
		}
		if v, ok := args["allowedDomains"]; ok {
			s.config.AllowedDomains = json.RawMessage(marshal(v))
		}
		if v, ok := args["handleConsent"]; ok {
			s.config.HandleConsent = json.RawMessage(marshal(v))
		}
		cfg := marshal(s.config)
		s.mu.Unlock()
		return textResult(`{"success":true,"config":` + cfg + `}`)
	case "firefox_diff_page_state":
		before, b := args["before"]
		after, a := args["after"]
		if !b || !a || before == nil || after == nil {
			return errorResult(`Diff error: Both "before" and "after" snapshots are required`)
		}
		return textResult(diffPageState(before, after))
	case "firefox_diagnose":
		return textResult(marshal(s.diagnose()))
	}

	command, ok := catalog.ToolToCommand[name]
	if !ok {
		return errorResult("Unknown tool: " + name)
	}
	return s.forward(name, command, args)
}

func groupFor(category string) []string {
	if g, ok := catalog.CategoryGroups[category]; ok {
		return g
	}
	return []string{category}
}

func (s *Server) callActivate(args map[string]any) toolResult {
	category, _ := args["category"].(string)
	if category == "" {
		category = "core"
	}
	groups, ok := catalog.CategoryGroups[category]
	if !ok {
		return errorResult(fmt.Sprintf("Unknown category: %q. Valid: %s", category, strings.Join(catalog.GroupNames, ", ")))
	}
	s.mu.Lock()
	s.activate(groups)
	var cats, tools []string
	for _, c := range []string{"core", "inspection", "devtools", "multiagent", "loop", "config", "diagnose"} {
		if s.activated[c] {
			cats = append(cats, c)
		}
	}
	for _, t := range s.tools {
		if s.activated[catalog.CategoryOf(t.Name)] {
			tools = append(tools, t.Name)
		}
	}
	s.mu.Unlock()
	s.notifyToolsChanged()

	var mode struct {
		PrivateWindowsAvailable any `json:"privateWindowsAvailable"`
		CurrentWindowMode       any `json:"currentWindowMode"`
	}
	if resp, err := s.sendCommand("getWindowMode", map[string]any{}, true, 0); err == nil && resp.Success {
		_ = json.Unmarshal(resp.Result, &mode)
	}
	return textResult(marshal(struct {
		Activated               bool     `json:"activated"`
		Categories              []string `json:"categories"`
		Tools                   []string `json:"tools"`
		Count                   int      `json:"count"`
		PrivateWindowsAvailable any      `json:"privateWindowsAvailable"`
		CurrentWindowMode       any      `json:"currentWindowMode"`
	}{true, cats, tools, len(tools), mode.PrivateWindowsAvailable, mode.CurrentWindowMode}))
}

func clampTimeout(ms float64) float64 {
	return max(float64(ipc.MinCommandTimeoutMs), min(float64(ipc.MaxCommandTimeoutMs), ms))
}

func (s *Server) forward(name, command string, args map[string]any) toolResult {
	params := make(map[string]any, len(args)+2)
	for k, v := range args {
		params[k] = v
	}

	if name == "firefox_wait_for" {
		wait := 10000.0
		if v, ok := params["timeout"].(float64); ok {
			wait = v
		}
		params["_timeout"] = clampTimeout(wait + 5000)
	} else if v, ok := params["timeout"].(float64); ok && v != 0 {
		params["_timeout"] = clampTimeout(v)
		delete(params, "timeout")
	}

	savePath, _ := args["savePath"].(string)
	_, hasSavePath := args["savePath"]
	delete(params, "savePath")

	// Hardened: every command carries this server's own agent ID; a caller-
	// supplied agentId is never forwarded, so one agent can't act as another.
	params["agentId"] = s.agentID
	s.reaper.Touch(s.agentID)

	if slices.Contains(loopTools, name) && s.sessionID != "" {
		params["sessionId"] = s.sessionID
	}

	if name == "firefox_navigate" || name == "firefox_create_window" {
		if u, ok := params["url"].(string); ok && u != "" {
			if blocked := s.checkDomain(u); blocked != "" {
				return errorResult(blocked)
			}
		}
	}

	s.mu.Lock()
	var injected *float64
	if _, has := params["tabId"]; !has && s.currentTab != nil && s.tabDefault[name] {
		v := *s.currentTab
		params["tabId"] = v
		injected = &v
	}
	s.mu.Unlock()

	s.reaper.Touch(s.agentID)
	resp, err := s.sendCommand(command, params, true, 0)
	if err != nil {
		return errorResult("Error: " + err.Error())
	}

	if resp.Success && (name == "firefox_navigate" || name == "firefox_create_window") {
		s.maybeHandleConsent(resp.Result)
	}

	staleNote := ""
	if resp.Success {
		s.trackCurrentTab(name, args, resp.Result)
	} else if injected != nil && staleTabRe.MatchString(resp.Error) {
		s.mu.Lock()
		s.currentTab = nil
		s.mu.Unlock()
		staleNote = fmt.Sprintf(" (your current tab %v is gone: call firefox_get_tabs or firefox_create_window)", *injected)
	}

	if !resp.Success {
		return errorResult(formatError(resp) + staleNote)
	}

	if name == "firefox_screenshot" {
		if r, ok := s.screenshotResult(resp.Result, savePath, hasSavePath); ok {
			return r
		}
	}
	if len(resp.Result) == 0 {
		return textResult(`{ "success": true }`)
	}
	if slices.Contains(textBodyTools, name) {
		var body struct {
			Text *string `json:"text"`
		}
		if json.Unmarshal(resp.Result, &body) == nil && body.Text != nil {
			if head, err := withoutKeys(resp.Result, "text"); err == nil {
				return textResult(string(head) + "\n\n" + *body.Text)
			}
		}
	}
	return textResult(string(compact(resp.Result)))
}

func formatError(resp *client.Response) string {
	msg := resp.Error
	if msg == "" {
		msg = "Unknown error"
	}
	d := resp.Details
	get := func(k string) string {
		if v, ok := d[k]; ok && v != nil {
			return fmt.Sprint(v)
		}
		return ""
	}
	switch get("code") {
	case "MUTEX_BUSY":
		pool := get("tabPool")
		if pool == "" {
			pool = "unknown"
		}
		return fmt.Sprintf("MUTEX_BUSY: Screenshot mutex held by another agent.\n  Holder: %s\n  Held for: %sms\n  Tab pool: %s\n  Retry after: %sms\n  Hint: %s",
			get("holder"), get("heldForMs"), pool, get("retryAfterMs"), get("hint"))
	case "POOL_FULL":
		return fmt.Sprintf("POOL_FULL: Tab pool is full and you have no tabs to evict.\n  Tab pool: %s\n  Owner breakdown: %s\n  Hint: %s",
			get("tabPool"), get("ownerBreakdown"), get("hint"))
	case "MODE_MISMATCH":
		return fmt.Sprintf("MODE_MISMATCH: %s\n  Current: %s\n  Requested: %s\n  Hint: %s",
			get("message"), get("currentMode"), get("requestedMode"), get("hint"))
	}
	return "Error: " + msg
}

func (s *Server) trackCurrentTab(name string, args map[string]any, result json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch name {
	case "firefox_close_window":
		s.currentTab = nil
		return
	case "firefox_close_tab", "firefox_detach_tab":
		if v, ok := args["tabId"].(float64); ok && s.currentTab != nil && v == *s.currentTab {
			s.currentTab = nil
		}
		return
	}
	var r struct {
		TabID *float64 `json:"tabId"`
	}
	if json.Unmarshal(result, &r) == nil && r.TabID != nil {
		v := *r.TabID
		s.currentTab = &v
	}
}

func (s *Server) maybeHandleConsent(result json.RawMessage) {
	s.mu.Lock()
	enabled := s.config != nil && string(s.config.HandleConsent) == "true"
	s.mu.Unlock()
	if !enabled {
		return
	}
	var r struct {
		TabID any `json:"tabId"`
	}
	_ = json.Unmarshal(result, &r)
	time.AfterFunc(600*time.Millisecond, func() {
		if s.shuttingDown.Load() {
			return
		}
		if _, err := s.sendCommand("handleConsent", map[string]any{"agentId": s.agentID, "tabId": r.TabID}, true, 0); err != nil {
			s.log("[claudezilla] Auto-trigger handleConsent failed: " + err.Error())
		}
	})
}

var domainPatternRe = regexp.MustCompile(`^[a-zA-Z0-9*.\-]+$`)

// checkDomain enforces firefox_set_config's allowedDomains; "" means allowed.
func (s *Server) checkDomain(rawURL string) string {
	s.mu.Lock()
	var domains []string
	if s.config != nil && len(s.config.AllowedDomains) > 0 {
		_ = json.Unmarshal(s.config.AllowedDomains, &domains)
	}
	s.mu.Unlock()
	if len(domains) == 0 {
		return ""
	}
	host, ok := hostnameOf(rawURL)
	if !ok || strings.HasPrefix(rawURL, "about:") {
		return ""
	}
	for _, p := range domains {
		if len(p) > 255 || !domainPatternRe.MatchString(p) {
			return fmt.Sprintf("DOMAIN_BLOCKED: Invalid domain pattern %q", p)
		}
	}
	for _, p := range domains {
		if strings.HasPrefix(p, "*.") {
			base := p[2:]
			if host == base || (strings.HasSuffix(host, "."+base) && !strings.Contains(strings.TrimSuffix(host, "."+base), ".")) {
				return ""
			}
		} else if host == p {
			return ""
		}
	}
	quoted := make([]string, len(domains))
	for i, d := range domains {
		quoted[i] = fmt.Sprintf("%q", d)
	}
	return fmt.Sprintf("DOMAIN_BLOCKED: Navigation to %q not allowed. Allowed: [%s]", host, strings.Join(quoted, ", "))
}

// diffPageState compares two getPageState snapshots by element.
func diffPageState(before, after any) string {
	keys := []string{"headings", "links", "buttons", "inputs"}
	added, removed := []field{}, []field{}
	items := func(snap any, k string) []json.RawMessage {
		m, _ := snap.(map[string]any)
		arr, _ := m[k].([]any)
		out := make([]json.RawMessage, len(arr))
		for i, v := range arr {
			out[i] = json.RawMessage(marshal(v))
		}
		return out
	}
	for _, k := range keys {
		b, a := items(before, k), items(after, k)
		bset, aset := map[string]bool{}, map[string]bool{}
		for _, x := range b {
			bset[canonical(x)] = true
		}
		for _, x := range a {
			aset[canonical(x)] = true
		}
		var add, rem []json.RawMessage
		for _, x := range a {
			if !bset[canonical(x)] {
				add = append(add, x)
			}
		}
		for _, x := range b {
			if !aset[canonical(x)] {
				rem = append(rem, x)
			}
		}
		if len(add) > 0 {
			added = append(added, field{k, json.RawMessage(marshal(add))})
		}
		if len(rem) > 0 {
			removed = append(removed, field{k, json.RawMessage(marshal(rem))})
		}
	}
	return string(encodeFields([]field{
		{"added", encodeFields(added)},
		{"removed", encodeFields(removed)},
		{"changed", json.RawMessage("{}")},
	}))
}

// ---------------------------------------------------------------------------
// Shutdown

// Shutdown tells the host this agent is leaving (closing its tabs), without
// ever starting the browser. Safe to call more than once.
func (s *Server) Shutdown(reason string) {
	s.shutdownOnce.Do(func() {
		s.shuttingDown.Store(true)
		s.reaper.Stop()
		s.log(fmt.Sprintf("[claudezilla] Received %s, cleaning up...", reason))
		done := make(chan struct{})
		go func() {
			defer close(done)
			if _, err := s.sendCommandOnce("goodbye", map[string]any{"agentId": s.agentID}, false, goodbyeTimeout); err != nil {
				s.log("[claudezilla] Goodbye failed (expected if Firefox closed): " + err.Error())
			} else {
				s.log("[claudezilla] Sent goodbye for " + truncateAgentID(s.agentID))
			}
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	})
}
