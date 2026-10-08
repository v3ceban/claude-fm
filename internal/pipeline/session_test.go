package pipeline

import (
	"testing"
	"time"
)

func TestParseIntField(t *testing.T) {
	v := "[Parsed_showinfo_3 @ 0x7bc2c2de00] n:   0 pts:  14031 pts_time:467.7 duration:      1"
	if p, ok := parseIntField(v, " pts:"); !ok || p != 14031 {
		t.Fatalf("video pts = %d %v", p, ok)
	}
	a := "[Parsed_ashowinfo_3 @ 0x1] n:1 pts:1024 pts_time:0.0213333 fmt:s16 channels:2 chlayout:stereo rate:48000 nb_samples:1024 checksum:D28B0B3A"
	if p, ok := parseIntField(a, " pts:"); !ok || p != 1024 {
		t.Fatalf("audio pts = %d %v", p, ok)
	}
	if n, ok := parseIntField(a, "nb_samples:"); !ok || n != 1024 {
		t.Fatalf("nb_samples = %d %v", n, ok)
	}
	if _, ok := parseIntField(a, "missing:"); ok {
		t.Fatal("missing key should not parse")
	}
}

func TestFrameQueueDropsBeforeAlignmentAndBlocksAfter(t *testing.T) {
	q := NewFrameQueue(2, 3)
	for i := range 3 {
		q.Push(&Frame{PTS: float64(i), Buf: q.Get()})
	}
	if q.Len() != 2 || q.Drops() != 0 || q.PeekFirst().PTS != 1 {
		t.Fatalf("pre-alignment: len=%d dropped=%d first=%v", q.Len(), q.Drops(), q.PeekFirst())
	}
	q.SetAligned(2)
	if q.Len() != 1 {
		t.Fatalf("after align len=%d", q.Len())
	}
	q.Push(&Frame{PTS: 3, Buf: q.Get()})
	pushed := make(chan bool)
	go func() { pushed <- q.Push(&Frame{PTS: 4, Buf: q.Get()}) }()
	select {
	case <-pushed:
		t.Fatal("push should block while the queue is full after alignment")
	case <-time.After(50 * time.Millisecond):
	}
	f, next, has := q.TakeDue(2.5)
	if f == nil || f.PTS != 2 || !has || next != 3 {
		t.Fatalf("TakeDue: %+v next=%v has=%v", f, next, has)
	}
	if ok := <-pushed; !ok {
		t.Fatal("blocked push should complete once space frees up")
	}
	f, _, _ = q.TakeDue(10)
	if f == nil || f.PTS != 4 || q.Drops() != 1 {
		t.Fatalf("newest-due: %+v dropped=%d", f, q.Drops())
	}
	q.Close()
	if q.Push(&Frame{PTS: 6, Buf: q.Get()}) {
		t.Fatal("push after close should fail")
	}
}

func TestAudioBufferTrimsAndPops(t *testing.T) {
	a := NewAudioBuffer(0.001)
	a.Push(AudioChunk{PTS: 0, Data: make([]byte, 100)})
	a.Push(AudioChunk{PTS: 1, Data: make([]byte, 100)})
	c, ok := a.Pop()
	if !ok || c.PTS != 1 || a.dropped != 1 {
		t.Fatalf("pop = %+v %v", c, ok)
	}
	a.Close()
	if _, ok := a.Pop(); ok {
		t.Fatal("pop after close should fail")
	}
}
