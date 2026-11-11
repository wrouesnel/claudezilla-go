// Command claudezilla-mcp is the Claudezilla MCP server for Claude Code and
// other MCP clients (stdio). Each client session runs its own process, which
// is one agent with its own tabs.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wrouesnel/claudezilla-go/internal/client"
	"github.com/wrouesnel/claudezilla-go/internal/ipc"
	"github.com/wrouesnel/claudezilla-go/internal/mcp"
	"github.com/wrouesnel/claudezilla-go/internal/version"
)

// call sends one host command and prints the response, for supervisors and
// scripts: claudezilla-mcp call quitBrowser, claudezilla-mcp call ping.
func call(args []string) int {
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: claudezilla-mcp call <command> ['<json params>']")
		return 2
	}
	params := map[string]any{}
	if len(args) == 2 {
		if err := json.Unmarshal([]byte(args[1]), &params); err != nil {
			fmt.Fprintln(os.Stderr, "params must be a JSON object:", err)
			return 2
		}
	}
	paths, err := ipc.Resolve()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	token := client.LoadToken(paths)
	if token == "" {
		fmt.Fprintln(os.Stderr, client.Describe(client.CodeNoToken, paths))
		return 1
	}
	resp, err := client.Request(paths, token, args[0], params, 30*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	out, _ := json.Marshal(resp)
	fmt.Println(string(out))
	if !resp.Success {
		return 1
	}
	return 0
}

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "call" {
		os.Exit(call(args[1:]))
	}
	for _, a := range args {
		if a == "--version" || a == "-v" {
			fmt.Println("claudezilla-mcp", version.Version)
			return
		}
	}
	s, err := mcp.New(mcp.Options{Args: args})
	if err != nil {
		fmt.Fprintln(os.Stderr, "Fatal error:", err)
		os.Exit(1)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		s.Shutdown((<-sig).String())
		os.Exit(0)
	}()
	_ = s.Serve(os.Stdin, os.Stdout)
	// The client closed stdin: say goodbye (closing this agent's tabs) and exit.
	s.Shutdown("stdin end")
}
