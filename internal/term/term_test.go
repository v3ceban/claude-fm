package term

import "testing"

func TestIsQuit(t *testing.T) {
	for _, in := range []string{"q", "Q", "\x03", "\x04", "\x1b", "xq"} {
		if !IsQuit([]byte(in)) {
			t.Errorf("IsQuit(%q) = false", in)
		}
	}
	for _, in := range []string{"x", " ", "\x1b[A", ""} {
		if IsQuit([]byte(in)) {
			t.Errorf("IsQuit(%q) = true", in)
		}
	}
}

func TestFocusEvents(t *testing.T) {
	ev := focusEvents([]byte("\x1b[Ox\x1b[I"))
	if len(ev) != 2 || ev[0] || !ev[1] {
		t.Fatalf("focus events = %v", ev)
	}
	if IsQuit([]byte("\x1b[O")) {
		t.Fatal("focus event must not quit")
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
