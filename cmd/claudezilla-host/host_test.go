package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wrouesnel/claudezilla-go/internal/client"
	"github.com/wrouesnel/claudezilla-go/internal/ipc"
	"github.com/wrouesnel/claudezilla-go/internal/nativemsg"
)

// startHost runs a host whose "extension" answers commands with answer().
func startHost(t *testing.T, answer func(cmd map[string]any) map[string]any) (*host, ipc.Paths) {
	t.Helper()
	dir, err := os.MkdirTemp("", "czh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	p := ipc.Paths{Dir: dir, Socket: filepath.Join(dir, "s"), Token: filepath.Join(dir, "t"), DebugLog: filepath.Join(dir, "log")}
	toExtR, toExtW := io.Pipe()
	h := &host{paths: p, token: strings.Repeat("ab", 32), ext: nativemsg.NewWriter(toExtW), pending: map[string]func(response){}, loops: newLoopStore()}
	ln, err := h.listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close(); toExtW.Close() })
	go func() {
		for {
			m, err := nativemsg.Read(toExtR)
			if err != nil {
				return
			}
			if r := answer(m); r != nil {
				r["id"] = m["id"]
				b, _ := json.Marshal(r)
				h.handleExtensionMessage(b)
			}
		}
	}()
	return h, p
}

func call(t *testing.T, p ipc.Paths, token, command string, params map[string]any) *client.Response {
	t.Helper()
	r, err := client.Request(p, token, command, params, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestHostAuthAndAllowlist(t *testing.T) {
	_, p := startHost(t, func(map[string]any) map[string]any { return map[string]any{"success": true} })
	tok, _ := os.ReadFile(p.Token)
	if st, _ := os.Stat(p.Token); st.Mode().Perm() != 0o600 {
		t.Fatalf("token mode %o", st.Mode().Perm())
	}
	if st, _ := os.Stat(p.Socket); st.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %o", st.Mode().Perm())
	}
	if r := call(t, p, "wrong", "ping", nil); r.Success || r.Error != "Invalid or missing auth token" {
		t.Fatalf("bad token accepted: %+v", r)
	}
	if r := call(t, p, string(tok), "setPrivateMode", nil); r.Success || !strings.Contains(r.Error, "not allowed") {
		t.Fatalf("non-allowlisted command: %+v", r)
	}
	t.Setenv("CLAUDEZILLA_ALLOW_QUIT", "")
	if r := call(t, p, string(tok), "quitBrowser", nil); r.Success || !strings.Contains(r.Error, "disabled") {
		t.Fatalf("quitBrowser without opt-in: %+v", r)
	}
}

func TestHostForwardsToExtension(t *testing.T) {
	_, p := startHost(t, func(m map[string]any) map[string]any {
		params := m["params"].(map[string]any)
		return map[string]any{"success": true, "result": map[string]any{"echo": m["command"], "url": params["url"]}}
	})
	tok, _ := os.ReadFile(p.Token)
	r := call(t, p, string(tok), "navigate", map[string]any{"url": "https://example.test/"})
	var res map[string]any
	_ = json.Unmarshal(r.Result, &res)
	if !r.Success || res["echo"] != "navigate" || res["url"] != "https://example.test/" {
		t.Fatalf("%+v %v", r, res)
	}
}

func TestHostTimesOutUnansweredCommand(t *testing.T) {
	_, p := startHost(t, func(map[string]any) map[string]any { return nil })
	tok, _ := os.ReadFile(p.Token)
	r := call(t, p, string(tok), "getContent", map[string]any{"_timeout": 5000.0})
	if r.Success || !strings.Contains(r.Error, "timed out after 5000ms") {
		t.Fatalf("%+v", r)
	}
}

func TestHostLoops(t *testing.T) {
	_, p := startHost(t, func(map[string]any) map[string]any { return nil })
	tok := func() string { b, _ := os.ReadFile(p.Token); return string(b) }()
	if r := call(t, p, tok, "startLoop", map[string]any{"prompt": "x", "maxIterations": 51.0}); r.Success {
		t.Fatal("over the 50 cap must fail")
	}
	r := call(t, p, tok, "startLoop", map[string]any{"prompt": "go", "sessionId": "s1"})
	var st map[string]any
	_ = json.Unmarshal(r.Result, &st)
	if !r.Success || st["maxIterations"] != 10.0 || st["sessionId"] != "s1" {
		t.Fatalf("%+v", st)
	}
	if r := call(t, p, tok, "startLoop", map[string]any{"prompt": "again", "sessionId": "s1"}); r.Success {
		t.Fatal("overlapping loop in one session must fail")
	}
	call(t, p, tok, "incrementLoopIteration", map[string]any{"sessionId": "s1"})
	r = call(t, p, tok, "getLoopState", map[string]any{"sessionId": "s1"})
	_ = json.Unmarshal(r.Result, &st)
	if st["iteration"] != 1.0 || st["active"] != true {
		t.Fatalf("%+v", st)
	}
	r = call(t, p, tok, "getLoopState", map[string]any{"sessionId": "other"})
	_ = json.Unmarshal(r.Result, &st)
	if st["active"] != false {
		t.Fatal("sessions must not share loop state")
	}
}
