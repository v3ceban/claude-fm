package render

import (
	"bytes"
	"image/png"
	"testing"
)

func TestLabel(t *testing.T) {
	w, h := LabelSize("⏸ Paused")
	w, h = w*3, h*3
	if w == 0 || h != 7*3+2*6 {
		t.Fatalf("size = %dx%d", w, h)
	}
	out := LabelPNG("⏸ Paused", 3, w+10, h+10)
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Fatal("canvas margin should be transparent")
	}
	if _, _, _, a := img.At(w/2+5, h/2+5).RGBA(); a == 0 {
		t.Fatal("label box should be opaque")
	}
	if LabelPNG("", 3, 10, 10) != nil || LabelPNG("日本", 3, 10, 10) != nil {
		t.Fatal("unknown text should render nothing")
	}
	w1, _ := LabelSize("🔊)")
	w3, _ := LabelSize("🔊)))")
	if w3-w1 != 6 {
		t.Fatalf("each wave should add 3 px, got %d", w3-w1)
	}
}
