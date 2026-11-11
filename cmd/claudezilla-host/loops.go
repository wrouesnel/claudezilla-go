package main

import (
	"fmt"
	"math"
	"regexp"
	"sync"
	"time"
)

const (
	defaultSession          = "__default__"
	maxTrackedSessions      = 64
	maxIterationsLimit      = 50 // hardened: hard cap (upstream 10000)
	defaultMaxIterations    = 10 // hardened: no unlimited loops
	maxLoopDuration         = time.Hour
	maxCompletionPromiseLen = 1000
)

var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9_\-:.]+$`)

type loopState struct {
	Active            bool    `json:"active"`
	Prompt            string  `json:"prompt"`
	Iteration         int     `json:"iteration"`
	MaxIterations     int     `json:"maxIterations"`
	CompletionPromise *string `json:"completionPromise"`
	StartedAt         *string `json:"startedAt"`
}

// loopStore keeps focus-loop state per session; the Stop hook reads it.
type loopStore struct {
	mu    sync.Mutex
	order []string
	byKey map[string]*loopState
}

func newLoopStore() *loopStore { return &loopStore{byKey: map[string]*loopState{}} }

func normalizeSession(v any) string {
	s, _ := v.(string)
	if s == "" || len(s) > 128 || !sessionIDRe.MatchString(s) {
		return defaultSession
	}
	return s
}

func (l *loopStore) get(key string) *loopState {
	st, ok := l.byKey[key]
	if !ok {
		st = &loopState{}
		l.byKey[key] = st
		l.order = append(l.order, key)
		if len(l.order) > maxTrackedSessions {
			oldest := l.order[0]
			l.order = l.order[1:]
			if oldest != key {
				delete(l.byKey, oldest)
			}
		}
	}
	return st
}

func timedOut(st *loopState) bool {
	if !st.Active || st.StartedAt == nil {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, *st.StartedAt)
	return err == nil && time.Since(t) > maxLoopDuration
}

func stateMap(st *loopState, extra map[string]any) map[string]any {
	m := map[string]any{
		"active": st.Active, "prompt": st.Prompt, "iteration": st.Iteration,
		"maxIterations": st.MaxIterations, "completionPromise": st.CompletionPromise, "startedAt": st.StartedAt,
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func fail(msg string) response { return response{"success": false, "error": msg} }

func (l *loopStore) handle(command string, params map[string]any, log func(string, ...any)) response {
	l.mu.Lock()
	defer l.mu.Unlock()
	sid := normalizeSession(params["sessionId"])
	st := l.get(sid)
	if st.Active && timedOut(st) {
		log("Loop auto-stopped (session=%s): exceeded %v timeout", sid, maxLoopDuration)
		*st = loopState{}
	}
	switch command {
	case "startLoop":
		if st.Active {
			return fail("Loop already active. Stop current loop first.")
		}
		prompt, ok := params["prompt"].(string)
		if !ok || prompt == "" {
			return fail("Prompt is required and must be a string")
		}
		maxIter := defaultMaxIterations
		if v, present := params["maxIterations"]; present && v != nil {
			f, isNum := v.(float64)
			switch {
			case !isNum:
				return fail(fmt.Sprintf("maxIterations must be 1-%d", maxIterationsLimit))
			case f == 0:
				// Number(0) || default in the original: zero means "use the default".
			case f != math.Trunc(f) || f < 1 || f > maxIterationsLimit:
				return fail(fmt.Sprintf("maxIterations must be 1-%d", maxIterationsLimit))
			default:
				maxIter = int(f)
			}
		}
		var promise *string
		if v, present := params["completionPromise"]; present && v != nil {
			p, ok := v.(string)
			if !ok {
				return fail("completionPromise must be a string or null")
			}
			if len([]rune(p)) > maxCompletionPromiseLen {
				return fail(fmt.Sprintf("completionPromise exceeds %d character limit", maxCompletionPromiseLen))
			}
			if p != "" {
				promise = &p
			}
		}
		now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
		*st = loopState{Active: true, Prompt: prompt, MaxIterations: maxIter, CompletionPromise: promise, StartedAt: &now}
		short := prompt
		if r := []rune(short); len(r) > 50 {
			short = string(r[:50])
		}
		log("Loop started (session=%s): %q max=%d", sid, short, maxIter)
		return response{"success": true, "result": stateMap(st, map[string]any{"sessionId": sid})}
	case "stopLoop":
		was := st.Active
		*st = loopState{}
		log("Loop stopped (session=%s)", sid)
		return response{"success": true, "result": map[string]any{"stopped": was, "sessionId": sid}}
	case "getLoopState":
		return response{"success": true, "result": stateMap(st, map[string]any{"timedOut": timedOut(st), "sessionId": sid})}
	case "incrementLoopIteration":
		if st.Active {
			st.Iteration++
			log("Loop iteration (session=%s): %d", sid, st.Iteration)
		}
		return response{"success": true, "result": map[string]any{"iteration": st.Iteration, "sessionId": sid}}
	}
	return fail("Unknown loop command: " + command)
}
