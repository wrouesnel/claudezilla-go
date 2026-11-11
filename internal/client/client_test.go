package client

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wrouesnel/claudezilla-go/internal/ipc"
)

// fakeHost serves one behaviour per connection on a socket in a temp dir.
func fakeHost(t *testing.T, serve func(net.Conn)) ipc.Paths {
	t.Helper()
	dir, err := os.MkdirTemp("", "czt") // short: sun_path is 108 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	p := ipc.Paths{Dir: dir, Socket: filepath.Join(dir, "s"), Token: filepath.Join(dir, "t")}
	ln, err := net.Listen("unix", p.Socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(c)
		}
	}()
	return p
}

func TestRequestForwardsAndResolves(t *testing.T) {
	var got map[string]any
	p := fakeHost(t, func(c net.Conn) {
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadBytes('\n')
		_ = json.Unmarshal(line, &got)
		c.Write([]byte(`{"success":true,"result":{"pong":true}}` + "\n"))
	})
	resp, err := Request(p, "tok", "ping", map[string]any{"a": 1.0}, time.Second)
	if err != nil || !resp.Success || string(resp.Result) != `{"pong":true}` {
		t.Fatalf("%+v %v", resp, err)
	}
	if got["command"] != "ping" || got["authToken"] != "tok" || got["params"].(map[string]any)["a"] != 1.0 {
		t.Fatalf("host saw %v", got)
	}
}

func TestRequestHostClosed(t *testing.T) {
	p := fakeHost(t, func(c net.Conn) { bufio.NewReader(c).ReadBytes('\n'); c.Close() })
	_, err := Request(p, "tok", "ping", nil, time.Second)
	var ce *Error
	if !errors.As(err, &ce) || ce.Code != CodeHostClosed {
		t.Fatalf("want HOST_CLOSED, got %v", err)
	}
}

func TestRequestResponseFlushedOnClose(t *testing.T) {
	p := fakeHost(t, func(c net.Conn) {
		bufio.NewReader(c).ReadBytes('\n')
		c.Write([]byte(`{"success":true}`))
		c.Close()
	})
	resp, err := Request(p, "tok", "ping", nil, time.Second)
	if err != nil || !resp.Success {
		t.Fatalf("%+v %v", resp, err)
	}
}

func TestRequestConnectionErrorIsRetryable(t *testing.T) {
	p := ipc.Paths{Socket: filepath.Join(t.TempDir(), "missing.sock"), Token: "/nonexistent"}
	_, err := Request(p, "tok", "ping", nil, time.Second)
	if !Retryable(err) || !strings.Contains(err.Error(), "socket not found") {
		t.Fatalf("want retryable ENOENT, got %v", err)
	}
}

func TestRequestIdleTimeout(t *testing.T) {
	p := fakeHost(t, func(c net.Conn) { time.Sleep(2 * time.Second); c.Close() })
	start := time.Now()
	_, err := Request(p, "tok", "ping", nil, 200*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") || time.Since(start) > time.Second {
		t.Fatalf("want timeout, got %v after %s", err, time.Since(start))
	}
}

func TestParseAutostartCommand(t *testing.T) {
	argv, err := ParseAutostartCommand(`["systemctl","--user","start","a b"]`)
	if err != nil || len(argv) != 4 || argv[3] != "a b" {
		t.Fatalf("%q %v", argv, err)
	}
	argv, _ = ParseAutostartCommand("  firefox  --headless ; rm -rf ~ ")
	if strings.Join(argv, "|") != "firefox|--headless|;|rm|-rf|~" {
		t.Fatalf("shell syntax must stay literal: %q", argv)
	}
	if _, err := ParseAutostartCommand(`[1,`); err == nil {
		t.Fatal("malformed JSON must fail")
	}
	if argv, _ := ParseAutostartCommand("  "); argv != nil {
		t.Fatal("empty must be nil")
	}
}

func TestProbe(t *testing.T) {
	p := fakeHost(t, func(c net.Conn) { c.Close() })
	if !Probe(p.Socket, time.Second) {
		t.Fatal("listening socket reported down")
	}
	if Probe(p.Socket+"x", time.Second) {
		t.Fatal("missing socket reported up")
	}
}

func newTestAutostart(up *atomic.Bool, runs *atomic.Int32, run func() (int, string, error)) *Autostart {
	return &Autostart{
		Socket: "s", Configured: `["start-browser"]`,
		Wait: 300 * time.Millisecond, LaunchTimeout: time.Second, Cooldown: time.Hour,
		Run:   func([]string, time.Duration) (int, string, error) { runs.Add(1); return run() },
		Probe: func(string) bool { return up.Load() },
		Log:   func(string) {},
	}
}

func TestAutostart(t *testing.T) {
	t.Run("unset does nothing", func(t *testing.T) {
		a := &Autostart{Probe: func(string) bool { t.Fatal("probed"); return false }}
		if err := a.Ensure(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("concurrent calls share one launch, and no relaunch after success", func(t *testing.T) {
		var up atomic.Bool
		var runs atomic.Int32
		a := newTestAutostart(&up, &runs, func() (int, string, error) {
			time.Sleep(50 * time.Millisecond)
			up.Store(true)
			return 0, "", nil
		})
		var wg sync.WaitGroup
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _ = a.Ensure() }()
		}
		wg.Wait()
		_ = a.Ensure()
		if runs.Load() != 1 {
			t.Fatalf("launched %d times", runs.Load())
		}
	})
	t.Run("failure surfaces stderr and respects cooldown", func(t *testing.T) {
		var up atomic.Bool
		var runs atomic.Int32
		a := newTestAutostart(&up, &runs, func() (int, string, error) { return 3, "no display\n", nil })
		if err := a.Ensure(); err == nil || !strings.Contains(err.Error(), "exit 3): no display") {
			t.Fatalf("got %v", err)
		}
		if err := a.Ensure(); err == nil || !strings.Contains(err.Error(), "cooldown") {
			t.Fatalf("got %v", err)
		}
		if runs.Load() != 1 {
			t.Fatalf("respawned within cooldown: %d", runs.Load())
		}
	})
	t.Run("gives up after the wait window", func(t *testing.T) {
		var up atomic.Bool
		var runs atomic.Int32
		a := newTestAutostart(&up, &runs, func() (int, string, error) { return 0, "", nil })
		if err := a.Ensure(); err == nil || !strings.Contains(err.Error(), "did not come up") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("re-probes after the host goes down", func(t *testing.T) {
		var up atomic.Bool
		var runs atomic.Int32
		a := newTestAutostart(&up, &runs, func() (int, string, error) { up.Store(true); return 0, "", nil })
		up.Store(true)
		_ = a.Ensure()
		up.Store(false)
		a.MarkHostDown()
		_ = a.Ensure()
		if runs.Load() != 1 {
			t.Fatalf("want one relaunch after host went down, got %d", runs.Load())
		}
	})
}

func TestResolveAgentTimeout(t *testing.T) {
	t.Setenv("CLAUDEZILLA_AGENT_TIMEOUT_MS", "")
	if d, to := ResolveAgentTimeout(); d || to != DefaultAgentTimeout {
		t.Fatal("unset must reap at the default")
	}
	t.Setenv("CLAUDEZILLA_AGENT_TIMEOUT_MS", "1500")
	if d, to := ResolveAgentTimeout(); d || to != 1500*time.Millisecond {
		t.Fatal("positive value must override")
	}
	t.Setenv("CLAUDEZILLA_AGENT_TIMEOUT_MS", "0")
	if d, _ := ResolveAgentTimeout(); !d {
		t.Fatal("0 must disable")
	}
}

func TestReaper(t *testing.T) {
	now := time.Unix(1000, 0)
	var cleaned []string
	fail := true
	r := &Reaper{Timeout: 10 * time.Minute, Now: func() time.Time { return now },
		OnCleanup: func(id string, _ time.Duration) error {
			cleaned = append(cleaned, id)
			if fail {
				fail = false
				return errors.New("host down")
			}
			return nil
		}}
	r.Touch("a")
	r.Touch("b")
	now = now.Add(5 * time.Minute)
	r.Touch("b") // heartbeat keeps b alive
	now = now.Add(6 * time.Minute)
	if got := r.Sweep(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("swept %v", got)
	}
	if got := r.Sweep(); len(got) != 1 {
		t.Fatalf("a failed cleanup must be retried, swept %v", got)
	}
	if got := r.Sweep(); len(got) != 0 {
		t.Fatalf("a successful cleanup must forget the agent, swept %v", got)
	}
	d := &Reaper{Disabled: true, Timeout: time.Nanosecond, OnCleanup: func(string, time.Duration) error { t.Fatal("disabled reaper cleaned"); return nil }}
	d.Touch("x")
	time.Sleep(time.Millisecond)
	d.Sweep()
}
