package tty

import "testing"

func TestParseProbe(t *testing.T) {
	noTmux(t)
	r, done := parseProbe([]byte("\x1b_Gi=31;OK\x1b\\\x1b[6;20;10t\x1b[?62;4;22c"))
	if !done || !r.kitty || r.cellW != 10 || r.cellH != 20 || !r.da {
		t.Fatalf("full reply = %+v done=%v", r, done)
	}
	r, done = parseProbe([]byte("\x1b_Gi=31;OK\x1b\\\x1b[6;20;10t"))
	if done || !r.kitty {
		t.Fatalf("without DA = %+v done=%v", r, done)
	}
	r, done = parseProbe([]byte("\x1b[?1;2c"))
	if !done || r.kitty || r.cellW != 0 {
		t.Fatalf("DA only = %+v done=%v", r, done)
	}
	r, done = parseProbe([]byte("\x1b_Gi=31;ENOTSUPPORTED:unknown action\x1b\\\x1b[?1;2c"))
	if !done || r.kitty {
		t.Fatalf("error reply = %+v done=%v", r, done)
	}
	inTmux = true
	r, done = parseProbe([]byte("\x1b_Gi=31;OK\x1b\\"))
	if !done || !r.kitty {
		t.Fatalf("tmux reply = %+v done=%v", r, done)
	}
	if _, done = parseProbe([]byte("\x1b[?1;2c")); done {
		t.Fatal("tmux must keep waiting after a DA reply")
	}
}

func TestProbeQuery(t *testing.T) {
	noTmux(t)
	if got := string(probeQuery()); got != "\x1b_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\\x1b[16t\x1b[c" {
		t.Fatalf("query = %q", got)
	}
	inTmux = true
	if got := string(probeQuery()); got != "\x1bPtmux;\x1b\x1b_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\x1b\\\x1b\\" {
		t.Fatalf("tmux query = %q", got)
	}
}
