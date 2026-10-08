package pipeline

import (
	"sync"
	"time"
)

const ringSize = 4096

type ptsRing struct {
	mu  sync.Mutex
	seq [ringSize]int64
	pts [ringSize]float64
}

func newPTSRing() *ptsRing {
	r := &ptsRing{}
	for i := range r.seq {
		r.seq[i] = -1
	}
	return r
}

func (r *ptsRing) set(n int64, pts float64) {
	r.mu.Lock()
	r.seq[n%ringSize], r.pts[n%ringSize] = n, pts
	r.mu.Unlock()
}

func (r *ptsRing) get(n int64) (float64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seq[n%ringSize] != n {
		return 0, false
	}
	return r.pts[n%ringSize], true
}

func (r *ptsRing) wait(n int64, d time.Duration) (float64, bool) {
	deadline := time.Now().Add(d)
	for {
		if p, ok := r.get(n); ok {
			return p, true
		}
		if time.Now().After(deadline) {
			return 0, false
		}
		time.Sleep(2 * time.Millisecond)
	}
}
