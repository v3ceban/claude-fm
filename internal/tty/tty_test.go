package tty

import (
	"slices"
	"testing"
)

func TestParseKeys(t *testing.T) {
	cases := map[string][]Key{
		"q": {KeyQuit}, "Q": {KeyQuit}, "\x03": {KeyQuit}, "\x04": {KeyQuit}, "\x1b": {KeyQuit}, "xq": {KeyQuit},
		" ": {KeyPause}, "p": {KeyPause}, "l": {KeyLive}, "\x1b[A": {KeyUp}, "\x1bOB": {KeyDown}, "\x1b[C\x1b[D": nil,
		"\x1b[O": nil, "\x1b[1;5A": nil, "x": nil, "": nil, "\x1b[Aq": {KeyUp, KeyQuit},
	}
	for in, want := range cases {
		if got := parseKeys([]byte(in)); !slices.Equal(got, want) {
			t.Errorf("parseKeys(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestFocusEvents(t *testing.T) {
	ev := focusEvents([]byte("\x1b[Ox\x1b[I"))
	if len(ev) != 2 || ev[0] || !ev[1] {
		t.Fatalf("focus events = %v", ev)
	}
}

func TestParsePane(t *testing.T) {
	cases := []struct {
		out  string
		want PaneState
	}{
		{"0|0|49|50|51|on|bottom|1|1|0|0\n", PaneState{Visible: true}},
		{"3|40|49|50|51|on|top|1|1|0|1", PaneState{PaneGeometry: PaneGeometry{Top: 4, Left: 40}, Visible: true}},
		{"0|0|49|50|50|off|bottom|1|1|0|1", PaneState{PaneGeometry: PaneGeometry{LastRow: true}, Visible: true}},
		{"0|0|49|50|51|on|bottom|0|1|0|1", PaneState{}},
		{"0|0|49|50|51|on|bottom|1|0|0|1", PaneState{}},
		{"0|0|49|50|51|on|bottom|1|1|1|0", PaneState{}},
		{"garbage", PaneState{}},
		{"0|0|39|40||on|bottom|1|0|0|1", PaneState{}},
	}
	for _, c := range cases {
		if got := parsePane(c.out); got != c.want {
			t.Errorf("parsePane(%q) = %+v, want %+v", c.out, got, c.want)
		}
	}
}
