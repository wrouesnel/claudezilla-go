package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultAutostartWait = 45 * time.Second
	DefaultLaunchTimeout = 20 * time.Second
	DefaultRetryCooldown = 60 * time.Second
	probeInterval        = 500 * time.Millisecond
	probeTimeout         = time.Second
)

// ParseAutostartCommand parses CLAUDEZILLA_AUTOSTART_CMD: a JSON array of
// strings, or a plain string split on whitespace. It is never run by a shell.
func ParseAutostartCommand(v string) ([]string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	if strings.HasPrefix(v, "[") {
		var argv []string
		if err := json.Unmarshal([]byte(v), &argv); err != nil {
			return nil, fmt.Errorf("CLAUDEZILLA_AUTOSTART_CMD is not valid JSON: %v", err)
		}
		if len(argv) == 0 {
			return nil, errors.New("CLAUDEZILLA_AUTOSTART_CMD must be a non-empty JSON array of strings")
		}
		return argv, nil
	}
	return strings.Fields(v), nil
}

func envDuration(name string, def time.Duration, allowZero bool) time.Duration {
	n, err := strconv.ParseFloat(os.Getenv(name), 64)
	if err != nil || n < 0 || (n == 0 && !allowZero) {
		return def
	}
	return time.Duration(n * float64(time.Millisecond))
}

// Autostart starts the browser on demand when the host isn't reachable.
type Autostart struct {
	Socket        string
	Configured    string
	Wait          time.Duration
	LaunchTimeout time.Duration
	Cooldown      time.Duration
	Run           func(argv []string, timeout time.Duration) (int, string, error)
	Probe         func(socket string) bool
	Log           func(string)

	mu          sync.Mutex
	hostUp      *bool
	launching   chan struct{}
	launchErr   error
	lastFailure time.Time
}

// NewAutostart configures autostart from the environment.
func NewAutostart(socket string) *Autostart {
	return &Autostart{
		Socket:        socket,
		Configured:    os.Getenv("CLAUDEZILLA_AUTOSTART_CMD"),
		Wait:          envDuration("CLAUDEZILLA_AUTOSTART_WAIT_MS", DefaultAutostartWait, false),
		LaunchTimeout: envDuration("CLAUDEZILLA_AUTOSTART_LAUNCH_TIMEOUT_MS", DefaultLaunchTimeout, false),
		Cooldown:      envDuration("CLAUDEZILLA_AUTOSTART_RETRY_COOLDOWN_MS", DefaultRetryCooldown, true),
		Run:           runArgv,
		Probe:         func(s string) bool { return Probe(s, probeTimeout) },
		Log:           func(m string) { fmt.Fprintln(os.Stderr, m) },
	}
}

func runArgv(argv []string, timeout time.Duration) (int, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), stderr.String(), nil
	}
	if err != nil {
		return -1, stderr.String(), err
	}
	return 0, stderr.String(), nil
}

// MarkHostDown makes the next Ensure re-probe and, if needed, relaunch.
func (a *Autostart) MarkHostDown() {
	a.mu.Lock()
	f := false
	a.hostUp = &f
	a.mu.Unlock()
}

// Ensure makes sure the host is up, launching the browser if configured.
// It returns an error only when a launch was attempted (or skipped by the
// cooldown) and failed; that error is appended to the connection error.
func (a *Autostart) Ensure() error {
	if strings.TrimSpace(a.Configured) == "" {
		return nil
	}
	a.mu.Lock()
	if a.hostUp != nil && *a.hostUp {
		a.mu.Unlock()
		return nil
	}
	if a.hostUp == nil {
		a.mu.Unlock()
		up := a.Probe(a.Socket)
		a.mu.Lock()
		a.hostUp = &up
		if up {
			a.mu.Unlock()
			return nil
		}
	}
	if ch := a.launching; ch != nil {
		a.mu.Unlock()
		<-ch
		return nil
	}
	if !a.lastFailure.IsZero() && time.Since(a.lastFailure) < a.Cooldown {
		a.mu.Unlock()
		return errors.New("Autostart skipped: the previous start failed and the retry cooldown has not elapsed.")
	}
	ch := make(chan struct{})
	a.launching = ch
	a.mu.Unlock()

	err := a.launch()

	a.mu.Lock()
	a.launching = nil
	if err != nil {
		a.lastFailure = time.Now()
	}
	a.mu.Unlock()
	close(ch)
	return err
}

func (a *Autostart) launch() error {
	argv, err := ParseAutostartCommand(a.Configured)
	if err != nil {
		return err
	}
	if len(argv) == 0 {
		return errors.New("CLAUDEZILLA_AUTOSTART_CMD is not set")
	}
	a.Log(fmt.Sprintf("[claudezilla] No host at %s; starting browser: %s", a.Socket, strings.Join(argv, " ")))
	code, stderr, err := a.Run(argv, a.LaunchTimeout)
	if err != nil {
		return fmt.Errorf("Autostart command could not be run: %v", err)
	}
	if code != 0 {
		msg := fmt.Sprintf("Autostart command failed (exit %d)", code)
		if s := strings.TrimSpace(stderr); s != "" {
			msg += ": " + s
		}
		return errors.New(msg)
	}
	deadline := time.Now().Add(a.Wait)
	for time.Now().Before(deadline) {
		if a.Probe(a.Socket) {
			a.mu.Lock()
			t := true
			a.hostUp = &t
			a.mu.Unlock()
			return nil
		}
		time.Sleep(probeInterval)
	}
	return fmt.Errorf("Browser did not come up within %d ms", a.Wait.Milliseconds())
}
