package client

import (
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	DefaultAgentTimeout = 10 * time.Minute
	CleanupInterval     = time.Minute
)

// ResolveAgentTimeout reads CLAUDEZILLA_AGENT_TIMEOUT_MS; 0 disables reaping.
func ResolveAgentTimeout() (disabled bool, timeout time.Duration) {
	raw := os.Getenv("CLAUDEZILLA_AGENT_TIMEOUT_MS")
	n, err := strconv.ParseFloat(raw, 64)
	if raw != "" && err == nil && n == 0 {
		return true, 0
	}
	if err == nil && n > 0 {
		return false, time.Duration(n * float64(time.Millisecond))
	}
	return false, DefaultAgentTimeout
}

// Reaper closes the tabs of agents that have been silent too long.
type Reaper struct {
	Timeout   time.Duration
	Disabled  bool
	Interval  time.Duration
	OnCleanup func(agentID string, idle time.Duration) error
	Now       func() time.Time

	mu         sync.Mutex
	heartbeats map[string]time.Time
	stop       chan struct{}
}

func (r *Reaper) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Touch records that an agent is alive.
func (r *Reaper) Touch(agentID string) {
	if agentID == "" || agentID == "unknown" {
		return
	}
	r.mu.Lock()
	if r.heartbeats == nil {
		r.heartbeats = map[string]time.Time{}
	}
	r.heartbeats[agentID] = r.now()
	r.mu.Unlock()
}

// Sweep cleans up idle agents and returns their IDs. An agent is forgotten
// only after its cleanup succeeded, so failures are retried next sweep.
func (r *Reaper) Sweep() []string {
	if r.Disabled {
		return nil
	}
	now := r.now()
	type idle struct {
		id   string
		for_ time.Duration
	}
	var orphaned []idle
	r.mu.Lock()
	for id, seen := range r.heartbeats {
		if d := now.Sub(seen); d > r.Timeout {
			orphaned = append(orphaned, idle{id, d})
		}
	}
	r.mu.Unlock()
	var ids []string
	for _, o := range orphaned {
		ids = append(ids, o.id)
		if err := r.OnCleanup(o.id, o.for_); err == nil {
			r.mu.Lock()
			delete(r.heartbeats, o.id)
			r.mu.Unlock()
		}
	}
	return ids
}

// Start runs Sweep periodically until Stop. A disabled reaper never sweeps.
func (r *Reaper) Start() {
	if r.Disabled || r.stop != nil {
		return
	}
	interval := r.Interval
	if interval <= 0 {
		interval = CleanupInterval
	}
	r.stop = make(chan struct{})
	go func(stop chan struct{}) {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				r.Sweep()
			case <-stop:
				return
			}
		}
	}(r.stop)
}

// Stop ends periodic sweeps.
func (r *Reaper) Stop() {
	if r.stop != nil {
		close(r.stop)
		r.stop = nil
	}
}
