package pipeline

import "sync"

type Frame struct {
	PTS float64
	Buf []byte
}

type FrameQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	items   []*Frame
	cap     int
	closed  bool
	aligned bool
	pool    *sync.Pool
	dropped int
	notify  chan struct{}
}

func NewFrameQueue(capacity, frameBytes int) *FrameQueue {
	q := &FrameQueue{cap: capacity, notify: make(chan struct{}, 1)}
	q.cond = sync.NewCond(&q.mu)
	q.pool = &sync.Pool{New: func() any { b := make([]byte, frameBytes); return &b }}
	return q
}

func (q *FrameQueue) Get() []byte             { return *(q.pool.Get().(*[]byte)) }
func (q *FrameQueue) Put(b []byte)            { q.pool.Put(&b) }
func (q *FrameQueue) Notify() <-chan struct{} { return q.notify }

func (q *FrameQueue) Push(f *Frame) bool {
	q.mu.Lock()
	for len(q.items) >= q.cap && q.aligned && !q.closed {
		q.cond.Wait()
	}
	if q.closed {
		q.mu.Unlock()
		q.Put(f.Buf)
		return false
	}
	if len(q.items) >= q.cap {
		q.Put(q.items[0].Buf)
		q.items = q.items[1:]
		if q.aligned {
			q.dropped++
		}
	}
	q.items = append(q.items, f)
	q.mu.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:
	}
	return true
}

func (q *FrameQueue) TakeDue(target float64) (f *Frame, next float64, hasNext bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	idx := -1
	for i, it := range q.items {
		if it.PTS <= target {
			idx = i
		} else {
			break
		}
	}
	if idx >= 0 {
		for i := range idx {
			q.Put(q.items[i].Buf)
			q.dropped++
		}
		f = q.items[idx]
		q.items = q.items[idx+1:]
		q.cond.Broadcast()
	}
	if len(q.items) > 0 {
		next, hasNext = q.items[0].PTS, true
	}
	return
}

func (q *FrameQueue) PeekFirst() *Frame {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return nil
	}
	f := q.items[0]
	b := q.Get()[:len(f.Buf)]
	copy(b, f.Buf)
	return &Frame{PTS: f.PTS, Buf: b}
}

func (q *FrameQueue) Drops() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dropped
}

func (q *FrameQueue) SetAligned(t0 float64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for n < len(q.items) && q.items[n].PTS < t0-1e-6 {
		q.Put(q.items[n].Buf)
		n++
	}
	q.items = q.items[n:]
	q.aligned = true
	q.cond.Broadcast()
}

func (q *FrameQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

func (q *FrameQueue) Close() {
	q.mu.Lock()
	q.closed = true
	for _, it := range q.items {
		q.Put(it.Buf)
	}
	q.items = nil
	q.cond.Broadcast()
	q.mu.Unlock()
}

type AudioChunk struct {
	PTS  float64
	Data []byte
}

// End returns the presentation time just past the chunk's last sample.
func (c AudioChunk) End() float64 { return c.PTS + float64(len(c.Data))/AudioBytesPerSec }

const (
	SampleRate       = 48000
	BytesPerFrame    = 2 * 2 // stereo s16le
	AudioBytesPerSec = SampleRate * BytesPerFrame
)

type AudioBuffer struct {
	mu      sync.Mutex
	cond    *sync.Cond
	items   []AudioChunk
	bytes   int
	limit   int
	closed  bool
	dropped int
}

func NewAudioBuffer(capSecs float64) *AudioBuffer {
	a := &AudioBuffer{limit: int(capSecs * AudioBytesPerSec)}
	a.cond = sync.NewCond(&a.mu)
	return a
}

func (a *AudioBuffer) Push(c AudioChunk) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return false
	}
	for a.bytes+len(c.Data) > a.limit && len(a.items) > 0 {
		a.bytes -= len(a.items[0].Data)
		a.items = a.items[1:]
		a.dropped++
	}
	a.items = append(a.items, c)
	a.bytes += len(c.Data)
	a.cond.Broadcast()
	return true
}

func (a *AudioBuffer) Pop() (AudioChunk, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for len(a.items) == 0 && !a.closed {
		a.cond.Wait()
	}
	if len(a.items) == 0 {
		return AudioChunk{}, false
	}
	c := a.items[0]
	a.items = a.items[1:]
	a.bytes -= len(c.Data)
	return c, true
}

func (a *AudioBuffer) Close() {
	a.mu.Lock()
	a.closed = true
	a.cond.Broadcast()
	a.mu.Unlock()
}

func (a *AudioBuffer) Seconds() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return float64(a.bytes) / AudioBytesPerSec
}
