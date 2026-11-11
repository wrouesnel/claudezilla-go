// Package catalog holds the MCP tool definitions as data: tools.json (the
// exact tools/list entries), gateway.json (firefox_activate for lazy mode)
// and instructions.txt (sent once in the initialize response). Keeping them
// as data means tool changes from upstream can be merged as JSON edits.
package catalog

import (
	_ "embed"
	"encoding/json"
)

//go:embed tools.json
var toolsJSON []byte

//go:embed gateway.json
var gatewayJSON []byte

//go:embed instructions.txt
var Instructions string

// Tool is one tools/list entry, kept as raw JSON plus the fields the server
// needs to reason about.
type Tool struct {
	Name        string
	Raw         json.RawMessage
	InputSchema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
}

func parse(raw json.RawMessage) Tool {
	var t Tool
	t.Raw = raw
	var head struct {
		Name        string          `json:"name"`
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		panic("catalog: " + err.Error())
	}
	t.Name = head.Name
	if err := json.Unmarshal(head.InputSchema, &t.InputSchema); err != nil {
		panic("catalog: " + head.Name + ": " + err.Error())
	}
	return t
}

// Tools returns the full catalog in order.
func Tools() []Tool {
	var raws []json.RawMessage
	if err := json.Unmarshal(toolsJSON, &raws); err != nil {
		panic("catalog: " + err.Error())
	}
	out := make([]Tool, len(raws))
	for i, r := range raws {
		out[i] = parse(r)
	}
	return out
}

// Gateway returns the firefox_activate tool.
func Gateway() Tool { return parse(gatewayJSON) }

// Categories assigns each tool to a lazy-loading category.
var Categories = map[string]string{
	"firefox_create_window": "core", "firefox_navigate": "core", "firefox_click": "core",
	"firefox_type": "core", "firefox_press_key": "core", "firefox_scroll": "core",
	"firefox_screenshot": "core", "firefox_get_page_state": "core", "firefox_get_tabs": "core",
	"firefox_list_all_tabs": "core", "firefox_attach_tab": "core", "firefox_detach_tab": "core",
	"firefox_close_tab": "core", "firefox_get_accessibility_snapshot": "core", "firefox_find": "core",
	"firefox_get_content": "inspection", "firefox_get_element": "inspection",
	"firefox_wait_for": "inspection", "firefox_diff_page_state": "inspection",
	"firefox_get_console": "devtools", "firefox_get_network": "devtools", "firefox_evaluate": "devtools",
	"firefox_request_tab_space": "multiagent", "firefox_grant_tab_space": "multiagent",
	"firefox_get_slot_requests": "multiagent",
	"firefox_start_loop":        "loop", "firefox_stop_loop": "loop", "firefox_loop_status": "loop",
	"firefox_set_config": "config", "firefox_set_viewport": "config", "firefox_handle_consent": "config",
	"firefox_close_window": "config", "firefox_resize_window": "config",
	"firefox_diagnose": "diagnose",
}

// CategoryOf returns a tool's category ("core" when unlisted).
func CategoryOf(name string) string {
	if c, ok := Categories[name]; ok {
		return c
	}
	return "core"
}

// CategoryGroups expands an activation request into categories.
var CategoryGroups = map[string][]string{
	"core":       {"core"},
	"devtools":   {"core", "devtools"},
	"inspection": {"core", "inspection"},
	"multiagent": {"core", "multiagent"},
	"loop":       {"core", "loop"},
	"config":     {"core", "config"},
	"diagnose":   {"diagnose"},
	"all":        {"core", "inspection", "devtools", "multiagent", "loop", "config", "diagnose"},
}

// GroupNames lists CategoryGroups keys in a stable order for messages.
var GroupNames = []string{"core", "devtools", "inspection", "multiagent", "loop", "config", "diagnose", "all"}

// ToolToCommand maps MCP tools to native host commands.
var ToolToCommand = map[string]string{
	"firefox_create_window": "createWindow", "firefox_navigate": "navigate",
	"firefox_get_content": "getContent", "firefox_click": "click", "firefox_type": "type",
	"firefox_screenshot": "screenshot", "firefox_get_tabs": "getTabs",
	"firefox_list_all_tabs": "listAllTabs", "firefox_attach_tab": "attachTab",
	"firefox_detach_tab": "detachTab", "firefox_close_tab": "closeTab",
	"firefox_close_window": "closeWindow", "firefox_resize_window": "resizeWindow",
	"firefox_set_viewport": "setViewport", "firefox_get_console": "getConsoleLogs",
	"firefox_get_network": "getNetworkRequests", "firefox_evaluate": "evaluate",
	"firefox_wait_for": "waitFor", "firefox_scroll": "scroll", "firefox_get_element": "getElementInfo",
	"firefox_get_page_state": "getPageState", "firefox_get_accessibility_snapshot": "getAccessibilitySnapshot",
	"firefox_find": "find", "firefox_press_key": "pressKey", "firefox_start_loop": "startLoop",
	"firefox_stop_loop": "stopLoop", "firefox_loop_status": "getLoopState",
	"firefox_request_tab_space": "requestTabSpace", "firefox_grant_tab_space": "grantTabSpace",
	"firefox_get_slot_requests": "getSlotRequests", "firefox_handle_consent": "handleConsent",
	"firefox_set_config": "setConfig", "firefox_diff_page_state": "diffPageState",
}
