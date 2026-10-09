// Package tty handles raw terminal mode, tmux integration and key input.
package tty

import (
	"bytes"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	xterm "golang.org/x/term"
)

var inTmux = os.Getenv("TMUX") != ""

func tmux(args ...string) *exec.Cmd { return exec.Command("tmux", args...) }

type Key int

const (
	KeyQuit Key = iota + 1
	KeyUp
	KeyDown
	KeyPause
	KeyLive
)

type Terminal struct {
	fd    int
	state *xterm.State
	Quit  chan struct{}
	Winch chan os.Signal
	Focus chan bool
	Keys  chan Key
	Gfx   Graphics
	logf  func(string, ...any)
}

func Open(logf func(string, ...any)) (*Terminal, error) {
	t := &Terminal{fd: int(os.Stdin.Fd()), Quit: make(chan struct{}, 1), Winch: make(chan os.Signal, 1), Focus: make(chan bool, 4), Keys: make(chan Key, 16), logf: logf}
	st, err := xterm.MakeRaw(t.fd)
	if err != nil {
		return nil, err
	}
	t.state = st
	os.Stdout.WriteString("\x1b[?1049h\x1b[?25l\x1b[?7l\x1b[?1004h\x1b[0m\x1b[2J\x1b[H\x1b]2;Claude FM\x1b\\")
	if inTmux {
		tmux("set", "-p", "-t", os.Getenv("TMUX_PANE"), "allow-passthrough", "all").Run()
	}
	t.Gfx = t.detect()
	signal.Notify(t.Winch, syscall.SIGWINCH)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	go func() {
		for range stop {
			t.quit()
		}
	}()
	go t.readKeys()
	return t, nil
}

func (t *Terminal) quit() {
	select {
	case t.Quit <- struct{}{}:
	default:
	}
}

func (t *Terminal) Size() (int, int) {
	w, h, err := xterm.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return 80, 24
	}
	return w, h
}

func (t *Terminal) Restore() {
	os.Stdout.WriteString("\x1b[?1004l\x1b[0m\x1b[2J\x1b[?7h\x1b[?25h\x1b[?1049l")
	if t.state != nil {
		xterm.Restore(t.fd, t.state)
	}
}

func (t *Terminal) readKeys() {
	buf := make([]byte, 64)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			t.quit()
			return
		}
		if t.logf != nil {
			t.logf("stdin: %q", buf[:n])
		}
		for _, ev := range focusEvents(buf[:n]) {
			select {
			case t.Focus <- ev:
			default:
			}
		}
		for _, k := range parseKeys(buf[:n]) {
			if k == KeyQuit {
				t.quit()
				continue
			}
			select {
			case t.Keys <- k:
			default:
			}
		}
	}
}

func parseKeys(b []byte) []Key {
	var out []Key
	for i := 0; i < len(b); i++ {
		switch c := b[i]; {
		case c == 0x1b && i+2 < len(b) && (b[i+1] == '[' || b[i+1] == 'O'):
			switch b[i+2] {
			case 'A':
				out = append(out, KeyUp)
			case 'B':
				out = append(out, KeyDown)
			}
			i += 2
		case c == 0x1b:
			if len(b) == 1 {
				out = append(out, KeyQuit)
			}
		case c == 'q' || c == 'Q' || c == 3 || c == 4:
			out = append(out, KeyQuit)
		case c == ' ' || c == 'p' || c == 'P':
			out = append(out, KeyPause)
		case c == 'l' || c == 'L':
			out = append(out, KeyLive)
		}
	}
	return out
}

func focusEvents(b []byte) []bool {
	var out []bool
	for i := 0; i+2 < len(b); i++ {
		if b[i] == 0x1b && b[i+1] == '[' {
			switch b[i+2] {
			case 'I':
				out = append(out, true)
			case 'O':
				out = append(out, false)
			}
		}
	}
	return out
}

type PaneGeometry struct {
	Top, Left int
	LastRow   bool
}

type PaneState struct {
	PaneGeometry
	Visible bool
}

const paneFormat = "#{pane_top}|#{pane_left}|#{pane_bottom}|#{window_height}|#{client_height}|#{status}|#{status-position}" +
	"|#{window_active}|#{session_attached}|#{window_zoomed_flag}|#{pane_active}"

func QueryPane() PaneState {
	if !inTmux {
		return PaneState{Visible: true}
	}
	out, err := tmux("display", "-p", "-t", os.Getenv("TMUX_PANE"), paneFormat).Output()
	if err != nil {
		return PaneState{}
	}
	return parsePane(string(out))
}

func parsePane(out string) PaneState {
	var s PaneState
	f := strings.Split(strings.TrimSpace(out), "|")
	if len(f) != 11 {
		return s
	}
	top, _ := strconv.Atoi(f[0])
	left, _ := strconv.Atoi(f[1])
	bottom, _ := strconv.Atoi(f[2])
	wh, _ := strconv.Atoi(f[3])
	ch, _ := strconv.Atoi(f[4])
	statusTop := f[5] != "off" && f[6] == "top"
	s.Top, s.Left = top, left
	if statusTop {
		s.Top += ch - wh
	}
	s.LastRow = !statusTop && f[5] == "off" && bottom == wh-1
	s.Visible = f[7] == "1" && f[8] != "0" && (f[9] == "0" || f[10] == "1")
	return s
}

type PaneWatcher struct {
	mu   sync.Mutex
	cur  atomic.Pointer[PaneState]
	stop chan struct{}
	done chan struct{}
}

func WatchPane(interval time.Duration) *PaneWatcher {
	w := &PaneWatcher{stop: make(chan struct{}), done: make(chan struct{})}
	w.Refresh()
	if !inTmux {
		close(w.done)
		return w
	}
	go func() {
		defer close(w.done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-t.C:
				w.Refresh()
			}
		}
	}()
	return w
}

func (w *PaneWatcher) Get() PaneState { return *w.cur.Load() }

func (w *PaneWatcher) Refresh() PaneState {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := QueryPane()
	w.cur.Store(&s)
	return s
}

func (w *PaneWatcher) Stop() {
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
	<-w.done
}

func EnableFocusEvents() func() {
	if !inTmux {
		return func() {}
	}
	out, _ := tmux("show", "-gv", "focus-events").Output()
	if strings.TrimSpace(string(out)) == "on" {
		return func() {}
	}
	tmux("set", "-g", "focus-events", "on").Run()
	return func() { tmux("set", "-g", "focus-events", "off").Run() }
}

func RefreshClient() {
	if inTmux {
		tmux("refresh-client").Run()
	}
}

func Passthrough(dst, payload []byte) []byte {
	if !inTmux {
		return append(dst, payload...)
	}
	dst = append(dst, "\x1bPtmux;"...)
	for {
		i := bytes.IndexByte(payload, 0x1b)
		if i < 0 {
			break
		}
		dst = append(dst, payload[:i+1]...)
		dst = append(dst, 0x1b)
		payload = payload[i+1:]
	}
	dst = append(dst, payload...)
	return append(dst, "\x1b\\"...)
}
