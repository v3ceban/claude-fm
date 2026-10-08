// Package pipeline drives ffmpeg and mpv and keeps audio and video in sync.
package pipeline

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"math"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Opts struct {
	URLs       []string
	Local      bool
	SrcW, SrcH int
	AO         string
	Volume     int
	QueueCap   int
	Log        *log.Logger
}

type clockBP struct{ mpvT, pts float64 }

type Session struct {
	opts   Opts
	procs  []*exec.Cmd
	mpv    *MPV
	Frames *FrameQueue
	audio  *AudioBuffer

	mu             sync.Mutex
	firstV, firstA float64
	haveV, haveA   bool
	t0             float64
	aligned        bool
	alignedCh      chan struct{}

	offs    [12]float64
	offN    int
	offUsed float64
	tpOK    bool
	paused  bool
	epoch   time.Time
	bps     []clockBP
	sent    int64

	vring, aring *ptsRing
	lastV, lastA time.Time
	alignedAt    time.Time
	lastClock    float64
	lastAdvance  time.Time
	aheadSince   time.Time

	Done     chan struct{}
	err      error
	lastLine string
	closed   bool
}

const (
	stallGrace  = 10 * time.Second
	clockStall  = 6 * time.Second
	feedStall   = 8 * time.Second
	aheadLimit  = 5.0
	aheadStall  = 5 * time.Second
	audioChunk  = 1024
	audioChunkB = audioChunk * BytesPerFrame
)

func FrameBytes(w, h int) int { return w * h * 3 / 2 }

func (s *Session) inputArgs(u string) []string {
	if s.opts.Local {
		return []string{"-re", "-i", u}
	}
	return []string{"-rw_timeout", "8000000", "-i", u}
}

func (s *Session) spawn(args []string) (*exec.Cmd, io.ReadCloser, io.ReadCloser, error) {
	s.opts.Log.Printf("ffmpeg %s", strings.Join(args, " "))
	cmd := exec.Command("ffmpeg", append([]string{"-hide_banner", "-nostats", "-loglevel", "info", "-nostdin", "-copyts"}, args...)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, fmt.Errorf("start ffmpeg: %w", err)
	}
	s.procs = append(s.procs, cmd)
	return cmd, stdout, stderr, nil
}

func (s *Session) supervise(cmd *exec.Cmd, onEOF func(), readers ...func()) {
	var wg sync.WaitGroup
	for _, r := range readers {
		wg.Go(r)
	}
	go func() {
		wg.Wait()
		err := cmd.Wait()
		if err == nil {
			onEOF()
			return
		}
		s.mu.Lock()
		last := s.lastLine
		s.mu.Unlock()
		s.fail(fmt.Errorf("ffmpeg exited: %v (%s)", err, last))
	}()
}

func Start(o Opts) (*Session, error) {
	if o.Log == nil {
		o.Log = log.New(io.Discard, "", 0)
	}
	if o.QueueCap == 0 {
		const budget = 160 << 20
		o.QueueCap = min(max(budget/FrameBytes(o.SrcW, o.SrcH), 20), 60)
	}
	s := &Session{opts: o, Done: make(chan struct{}), alignedCh: make(chan struct{}), vring: newPTSRing(), aring: newPTSRing()}
	s.Frames = NewFrameQueue(o.QueueCap, FrameBytes(o.SrcW, o.SrcH))
	s.audio = NewAudioBuffer(30)
	m, err := startMPV(o.AO, o.Volume)
	if err != nil {
		return nil, err
	}
	s.mpv = m
	vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease:flags=area,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,showinfo=checksum=0", o.SrcW, o.SrcH, o.SrcW, o.SrcH)
	vargs := append(s.inputArgs(o.URLs[0]), "-map", "0:v:0", "-vf", vf,
		"-fps_mode", "passthrough", "-pix_fmt", "yuv420p", "-f", "rawvideo", "pipe:1")
	vcmd, vout, verr, err := s.spawn(vargs)
	if err != nil {
		s.Stop()
		return nil, err
	}
	parseV := func() { s.parseStderr(verr, s.vring) }
	s.supervise(vcmd, func() {}, parseV, func() { s.readVideo(vout) })
	af := fmt.Sprintf("aresample=%d,asetnsamples=%d,ashowinfo", SampleRate, audioChunk)
	aargs := append(s.inputArgs(o.URLs[len(o.URLs)-1]), "-map", "0:a:0", "-af", af, "-ac", "2", "-f", "s16le", "pipe:1")
	acmd, aout, aerr, err := s.spawn(aargs)
	if err != nil {
		s.Stop()
		return nil, err
	}
	parseA := func() { s.parseStderr(aerr, s.aring) }
	s.supervise(acmd, s.audio.Close, parseA, func() { s.readAudio(aout) })
	go s.forwardAudio()
	go s.pollClock()
	return s, nil
}

func (s *Session) fail(err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.err = err
	s.mu.Unlock()
	s.Frames.Close()
	s.audio.Close()
	close(s.Done)
}

func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *Session) T0() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.t0
}

func (s *Session) Stop() {
	s.fail(fmt.Errorf("stopped"))
	for _, p := range s.procs {
		if p.Process != nil {
			p.Process.Kill()
		}
	}
	if s.mpv != nil {
		s.mpv.Stop()
	}
}

func parseField(line, key string) (string, bool) {
	_, rest, ok := strings.Cut(line, key)
	f := strings.Fields(rest)
	if !ok || len(f) == 0 {
		return "", false
	}
	return f[0], true
}

func parseIntField(line, key string) (int64, bool) {
	f, ok := parseField(line, key)
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseInt(f, 10, 64)
	return v, err == nil
}

func parseFloatField(line, key string) (float64, bool) {
	f, ok := parseField(line, key)
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseFloat(f, 64)
	return v, err == nil
}

func (s *Session) parseStderr(r io.Reader, ring *ptsRing) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "Parsed_showinfo_") || strings.Contains(line, "Parsed_ashowinfo_") {
			n, ok1 := parseIntField(line, "] n:")
			pts, ok2 := parseFloatField(line, "pts_time:")
			if ok1 && ok2 {
				ring.set(n, pts)
			}
			continue
		}
		s.opts.Log.Printf("ffmpeg: %s", line)
		s.mu.Lock()
		s.lastLine = line
		s.mu.Unlock()
	}
	if err := sc.Err(); err != nil {
		s.opts.Log.Printf("ffmpeg stderr: %v", err)
	}
}

func (s *Session) readVideo(r io.Reader) {
	br := bufio.NewReaderSize(r, 4<<20)
	size := FrameBytes(s.opts.SrcW, s.opts.SrcH)
	last, step := 0.0, 1.0/30
	for n := int64(0); ; n++ {
		buf := s.Frames.Get()
		if _, err := io.ReadFull(br, buf[:size]); err != nil {
			s.Frames.Put(buf)
			return
		}
		pts, ok := s.vring.wait(n, 500*time.Millisecond)
		if !ok {
			pts = last + step
		} else if n > 0 && pts > last && pts-last < 1 {
			step = pts - last
		}
		last = pts
		s.mu.Lock()
		s.lastV = time.Now()
		if !s.haveV {
			s.haveV, s.firstV = true, pts
			s.opts.Log.Printf("first video pts %.3f", pts)
		}
		s.mu.Unlock()
		s.maybeAlign()
		if !s.Frames.Push(&Frame{PTS: pts, Buf: buf[:size]}) {
			return
		}
	}
}

func (s *Session) readAudio(r io.ReadCloser) {
	defer r.Close()
	br := bufio.NewReaderSize(r, 1<<20)
	last, lastLen := 0.0, 0
	for n := int64(0); ; n++ {
		data := make([]byte, audioChunkB)
		got, err := io.ReadFull(br, data)
		if got == 0 || (err != nil && err != io.ErrUnexpectedEOF) {
			return
		}
		data = data[:got]
		pts, ok := s.aring.wait(n, 500*time.Millisecond)
		if !ok {
			pts = last + float64(lastLen)/AudioBytesPerSec
		}
		last, lastLen = pts, got
		s.mu.Lock()
		s.lastA = time.Now()
		if !s.haveA {
			s.haveA, s.firstA = true, pts
			s.opts.Log.Printf("first audio pts %.3f", pts)
		}
		s.mu.Unlock()
		s.maybeAlign()
		if !s.audio.Push(AudioChunk{PTS: pts, Data: data}) {
			return
		}
	}
}

func (s *Session) maybeAlign() {
	s.mu.Lock()
	if s.aligned || !s.haveV || !s.haveA {
		s.mu.Unlock()
		return
	}
	t0 := max(s.firstV, s.firstA)
	s.t0, s.aligned = t0, true
	s.alignedAt = time.Now()
	s.lastAdvance = s.alignedAt
	s.mu.Unlock()
	s.opts.Log.Printf("aligned at t0=%.3f (video %.3f audio %.3f)", t0, s.firstV, s.firstA)
	s.Frames.SetAligned(t0)
	close(s.alignedCh)
}

func (s *Session) forwardAudio() {
	select {
	case <-s.alignedCh:
	case <-s.Done:
		return
	}
	t0 := s.T0()
	expect := -1.0
	for {
		c, ok := s.audio.Pop()
		if !ok {
			s.mpv.Stdin.Close()
			return
		}
		if c.End() <= t0 {
			continue
		}
		if c.PTS < t0 {
			skip := int((t0-c.PTS)*SampleRate+0.5) * BytesPerFrame
			if skip >= len(c.Data) {
				continue
			}
			c.Data = c.Data[skip:]
			c.PTS = t0
		}
		s.mu.Lock()
		if expect < 0 || math.Abs(c.PTS-expect) > 0.002 {
			s.bps = append(s.bps, clockBP{mpvT: float64(s.sent) / AudioBytesPerSec, pts: c.PTS})
			if len(s.bps) > 64 {
				s.bps = s.bps[len(s.bps)-64:]
			}
		}
		s.mu.Unlock()
		if _, err := s.mpv.Stdin.Write(c.Data); err != nil {
			s.fail(fmt.Errorf("mpv audio pipe: %v", err))
			return
		}
		s.mu.Lock()
		s.sent += int64(len(c.Data))
		s.mu.Unlock()
		expect = c.End()
	}
}

func (s *Session) pollClock() {
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	n := 0
	for {
		select {
		case <-s.Done:
			return
		case <-tick.C:
		}
		tp, ok := s.mpv.GetFloat("time-pos")
		now := time.Now()
		var paused, pok bool
		if n%6 == 0 {
			paused, pok = s.mpv.GetBool("pause")
		}
		n++
		s.mu.Lock()
		if pok {
			s.paused = paused
		}
		if ok && s.epoch.IsZero() {
			s.epoch = now
		}
		if ok && !s.paused {
			off := tp - now.Sub(s.epoch).Seconds()
			s.offs[s.offN%len(s.offs)] = off
			s.offN++
			best := slices.Max(s.offs[:min(s.offN, len(s.offs))])
			if !s.tpOK {
				s.offUsed, s.tpOK = best, true
			} else {
				d := best - s.offUsed
				switch {
				case math.Abs(d) > 0.15:
					s.offUsed = best
				case d > 0.003:
					s.offUsed += 0.003
				case d < -0.003:
					s.offUsed -= 0.003
				default:
					s.offUsed = best
				}
			}
		} else if ok {
			s.offUsed = tp - now.Sub(s.epoch).Seconds()
			s.offs[0], s.offN = s.offUsed, 1
			s.tpOK = true
		}
		s.mu.Unlock()
		select {
		case <-s.mpv.done:
			s.fail(fmt.Errorf("mpv exited: %v", s.mpv.err))
			return
		default:
		}
		if err := s.stalled(now); err != nil {
			s.fail(err)
			return
		}
	}
}

func (s *Session) stalled(now time.Time) error {
	clk, ok := s.Clock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.aligned || now.Sub(s.alignedAt) < stallGrace {
		return nil
	}
	if ok && clk > s.lastClock+0.1 {
		s.lastClock, s.lastAdvance = clk, now
	} else if now.Sub(s.lastAdvance) > clockStall {
		return fmt.Errorf("playback clock stalled")
	}
	if now.Sub(s.lastA) > feedStall {
		return fmt.Errorf("no audio for %s", feedStall)
	}
	if now.Sub(s.lastV) > feedStall && s.Frames.Len() < s.opts.QueueCap/2 {
		return fmt.Errorf("no video for %s", feedStall)
	}
	if f := s.Frames.PeekPTS(); ok && f > clk+aheadLimit {
		if s.aheadSince.IsZero() {
			s.aheadSince = now
		} else if now.Sub(s.aheadSince) > aheadStall {
			return fmt.Errorf("video %.0fs ahead of audio", f-clk)
		}
	} else {
		s.aheadSince = time.Time{}
	}
	return nil
}

func (s *Session) Clock() (float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.aligned {
		return 0, false
	}
	if !s.tpOK {
		return 0, false
	}
	t := s.offUsed + time.Since(s.epoch).Seconds()
	pts := s.t0 + t
	for _, bp := range slices.Backward(s.bps) {
		if bp.mpvT <= t {
			pts = bp.pts + (t - bp.mpvT)
			break
		}
	}
	return pts, true
}

func (s *Session) AudioBuffered() float64 { return s.audio.Seconds() }
