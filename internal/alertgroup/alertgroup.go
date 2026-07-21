// Package alertgroup folds repeated notifications for the same failure
// signature into one, so a rollout that crash-loops hundreds of pods
// produces a single alert instead of hundreds.
package alertgroup

import (
	"sync"
	"time"
)

type bucket struct {
	windowStart time.Time
	suppressed  int
}

// Grouper suppresses duplicate Observe calls for the same key within a
// sliding window.
//
// ponytail: purely event-driven — a burst's trailing suppressed count is
// only reported on the NEXT Observe call for the same key (attached to
// that call's notification), or never, if the burst simply stops. Add a
// ticker-based flush if a burst's tail must always be reported.
type Grouper struct {
	window time.Duration

	mu    sync.Mutex
	state map[string]*bucket
}

// New returns a Grouper that allows at most one notification per key per
// window.
func New(window time.Duration) *Grouper {
	return &Grouper{window: window, state: make(map[string]*bucket)}
}

// Observe records one occurrence of key. If the caller should send a
// notification now, ok is true and suppressed is how many prior
// occurrences of this key (in the window that just closed) were folded
// into it. If ok is false, the caller should skip sending.
func (g *Grouper) Observe(key string) (ok bool, suppressed int) {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now()
	b, exists := g.state[key]
	if !exists || now.Sub(b.windowStart) >= g.window {
		prevSuppressed := 0
		if exists {
			prevSuppressed = b.suppressed
		}
		g.state[key] = &bucket{windowStart: now}
		return true, prevSuppressed
	}

	b.suppressed++
	return false, 0
}
