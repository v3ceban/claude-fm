package render

import (
	"bytes"
	"image/png"
	"testing"
)

func TestPNGFrameEncodes(t *testing.T) {
	const w, h = 64, 36
	frame := yuvFrame(w, h, 81, 90, 240)
	p := NewPNGFrame(w, h)
	out := p.Encode(frame)
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := img.At(10, 10).RGBA()
	if r>>8 < 230 || g>>8 > 60 || b>>8 > 60 {
		t.Fatalf("expected red, got %d %d %d", r>>8, g>>8, b>>8)
	}
	if again := p.Encode(frame); !bytes.Equal(again, out) {
		t.Fatal("encoding should be deterministic")
	}
	if len(p.Encode(twoColourFrame(w, h))) == 0 {
		t.Fatal("two-colour frame should encode")
	}
}

func BenchmarkPNGFrame720p(b *testing.B) {
	frame := yuvFrame(1280, 720, 100, 128, 128)
	for i := range frame[:1280*720] {
		if (i/7+i/1280)%3 == 0 {
			frame[i] = 220
		}
	}
	p := NewPNGFrame(1280, 720)
	for b.Loop() {
		out := p.Encode(frame)
		b.ReportMetric(float64(len(out))/1024, "KB")
	}
}
