package render

import "testing"

func yuvFrame(w, h int, y, u, v uint8) []byte {
	buf := make([]byte, w*h*3/2)
	for i := range w * h {
		buf[i] = y
	}
	for i := w * h; i < w*h+w*h/4; i++ {
		buf[i] = u
	}
	for i := w*h + w*h/4; i < len(buf); i++ {
		buf[i] = v
	}
	return buf
}

func twoColourFrame(w, h int) []byte {
	frame := yuvFrame(w, h, 81, 90, 240)
	for y := range h {
		for x := w / 2; x < w; x++ {
			frame[y*w+x] = 145
		}
	}
	for i := w * h; i < w*h+w*h/4; i++ {
		if i%(w/2) >= w/4 {
			frame[i] = 54
		}
	}
	for i := w*h + w*h/4; i < len(frame); i++ {
		if i%(w/2) >= w/4 {
			frame[i] = 34
		}
	}
	return frame
}

func (q *Quantizer) rgbOut() []uint8 {
	out := make([]uint8, len(q.Idx)*3)
	for i, c := range q.Idx {
		p := q.Palette[c]
		out[i*3], out[i*3+1], out[i*3+2] = p[0], p[1], p[2]
	}
	return out
}

func TestQuantizerKeepsExactColours(t *testing.T) {
	const w, h = 32, 8
	q := NewQuantizer(w, h)
	q.Quantize(twoColourFrame(w, h))
	if len(q.Palette) != 2 {
		t.Fatalf("expected 2 colours, got %d: %v", len(q.Palette), q.Palette)
	}
	rgb := q.rgbOut()
	if rgb[0] < 230 || rgb[1] > 40 || rgb[2] > 40 {
		t.Fatalf("left half should be red, got %v", rgb[:3])
	}
	i := (w - 1) * 3
	if rgb[i+1] < 200 || rgb[i] > 60 || rgb[i+2] > 60 {
		t.Fatalf("right half should be green, got %v", rgb[i:i+3])
	}
}

func TestQuantizerCapsPaletteAt256(t *testing.T) {
	const w, h = 64, 64
	frame := yuvFrame(w, h, 128, 128, 128)
	for i := range w * h {
		frame[i] = uint8(16 + (i*7)%220)
	}
	for i := w * h; i < len(frame); i++ {
		frame[i] = uint8(40 + (i*13)%170)
	}
	q := NewQuantizer(w, h)
	q.Quantize(frame)
	if len(q.Palette) == 0 || len(q.Palette) > maxColours {
		t.Fatalf("palette size %d", len(q.Palette))
	}
	for _, idx := range q.Idx {
		if int(idx) >= len(q.Palette) {
			t.Fatalf("index %d out of palette range", idx)
		}
	}
}
