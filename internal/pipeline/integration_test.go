package pipeline

import (
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPipelineLocalClip(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	for _, bin := range []string{"ffmpeg", "mpv"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	dir := t.TempDir()
	clip := filepath.Join(dir, "clip.mp4")
	gen := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100",
		"-t", "4", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", clip)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate clip: %v: %s", err, out)
	}
	logger := log.New(io.Discard, "", 0)
	if testing.Verbose() {
		logger = log.New(os.Stderr, "", log.Ltime)
	}
	s, err := Start(Opts{URLs: []string{clip}, Local: true, SrcW: 320, SrcH: 180,
		AO: "null", Volume: 50, QueueCap: 60, Log: logger})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	deadline := time.Now().Add(15 * time.Second)
	var got []float64
	start := time.Time{}
	for time.Now().Before(deadline) && len(got) < 45 {
		clk, ok := s.Clock()
		if !ok {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if start.IsZero() {
			start = time.Now()
		}
		if f, _, _ := s.Frames.TakeDue(clk + 0.008); f != nil {
			got = append(got, f.PTS)
			s.Frames.Put(f.Buf)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(got) < 45 {
		t.Fatalf("only %d frames presented in time (session err: %v)", len(got), s.Err())
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("frames out of order at %d: %v", i, got[i-2:i+1])
		}
	}
	span := got[len(got)-1] - got[0]
	wall := time.Since(start).Seconds()
	if span < 1.0 || wall < span*0.7 || wall > span*1.6 {
		t.Fatalf("playback not real-time: media span %.2fs over %.2fs wall", span, wall)
	}
	if s.Frames.Drops() > 5 {
		t.Fatalf("too many dropped frames: %d", s.Frames.Drops())
	}
}
