package term

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func noTmux(t *testing.T) {
	t.Helper()
	was := inTmux
	inTmux = false
	t.Cleanup(func() { inTmux = was })
}

func TestImageWriterFrames(t *testing.T) {
	noTmux(t)
	var w ImageWriter
	png := []byte("tiny")
	b64 := base64.StdEncoding.EncodeToString(png)
	got := string(w.Frame(5, 7, 40, 10, png))
	want := "\x1b7\x1b[?2026h\x1b[5;7H\x1b_Ga=T,f=100,i=1,q=2,c=40,r=10,z=-1000000000,m=0;" + b64 + "\x1b\\" +
		"\x1b_Ga=d,d=I,i=2,q=2\x1b\\\x1b[?2026l\x1b8"
	if got != want {
		t.Fatalf("first frame\n got %q\nwant %q", got, want)
	}
	got = string(w.Frame(1, 1, 40, 10, png))
	if !strings.Contains(got, "i=2,q=2,c=40,r=10,z=-999999999,m=0;") || !strings.HasSuffix(got, "\x1b_Ga=d,d=I,i=1,q=2\x1b\\\x1b[?2026l\x1b8") {
		t.Fatalf("second frame should use id 2 and delete id 1: %q", got)
	}
}

func TestImageWriterChunks(t *testing.T) {
	noTmux(t)
	var w ImageWriter
	png := bytes.Repeat([]byte{0xAB}, 6000)
	out := w.Frame(1, 1, 1, 1, png)
	if n := bytes.Count(out, []byte("\x1b_G")); n != 3 {
		t.Fatalf("expected 2 image chunks + 1 delete, got %d", n)
	}
	if bytes.Count(out, []byte(",m=1;")) != 1 || bytes.Count(out, []byte("\x1b_Gm=0,q=2;")) != 1 {
		t.Fatalf("chunk continuation flags wrong: %q", out[:200])
	}
}

func TestPassthroughWrapsInTmux(t *testing.T) {
	was := inTmux
	t.Cleanup(func() { inTmux = was })
	inTmux = true
	got := string(Passthrough(nil, []byte("a\x1bb")))
	if got != "\x1bPtmux;a\x1b\x1bb\x1b\\" {
		t.Fatalf("passthrough = %q", got)
	}
	inTmux = false
	if got := string(Passthrough(nil, []byte("a\x1bb"))); got != "a\x1bb" {
		t.Fatalf("plain = %q", got)
	}
	if got := string(DeleteImages()); got != "\x1b_Ga=d,d=A,q=2\x1b\\" {
		t.Fatalf("delete = %q", got)
	}
}
