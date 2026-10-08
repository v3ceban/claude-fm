package term

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type Graphics struct {
	Kitty        bool
	CellW, CellH int
	Name         string
}

const probeTimeout = 2 * time.Second

func probeQuery() []byte {
	q := Passthrough(nil, []byte("\x1b_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\"))
	if !inTmux {
		q = append(q, "\x1b[16t\x1b[c"...)
	}
	return q
}

type probeReply struct {
	kitty        bool
	cellW, cellH int
	da           bool
}

// parseProbe reads the terminal's answers to probeQuery; complete reports that no more input is expected.
func parseProbe(b []byte) (r probeReply, complete bool) {
	r.kitty = bytes.Contains(b, []byte("\x1b_Gi=31;OK"))
	if i := bytes.Index(b, []byte("\x1b[6;")); i >= 0 {
		if j := bytes.IndexByte(b[i:], 't'); j > 0 {
			f := strings.Split(string(b[i+4:i+j]), ";")
			if len(f) == 2 {
				r.cellH, _ = strconv.Atoi(f[0])
				r.cellW, _ = strconv.Atoi(f[1])
			}
		}
	}
	if i := bytes.Index(b, []byte("\x1b[?")); i >= 0 {
		r.da = bytes.IndexByte(b[i:], 'c') > 0
	}
	if inTmux {
		return r, r.kitty
	}
	return r, r.da
}

func (t *Terminal) probe() probeReply {
	os.Stdout.Write(probeQuery())
	deadline := time.Now().Add(probeTimeout)
	var buf []byte
	tmp := make([]byte, 256)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		fds := []unix.PollFd{{Fd: int32(t.fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, int(left/time.Millisecond)+1)
		if err == unix.EINTR {
			continue
		}
		if n <= 0 {
			break
		}
		m, err := unix.Read(t.fd, tmp)
		if m <= 0 || err != nil {
			break
		}
		buf = append(buf, tmp[:m]...)
		if _, done := parseProbe(buf); done {
			break
		}
	}
	if t.logf != nil {
		t.logf("probe reply: %q", buf)
	}
	r, _ := parseProbe(buf)
	return r
}

func (t *Terminal) detect() Graphics {
	var g Graphics
	if inTmux {
		out, err := tmux("display", "-p", "#{client_cell_width}|#{client_cell_height}|#{client_termtype}").Output()
		if parts := strings.Split(strings.TrimSpace(string(out)), "|"); err == nil && len(parts) == 3 {
			g.CellW, _ = strconv.Atoi(parts[0])
			g.CellH, _ = strconv.Atoi(parts[1])
			g.Name = parts[2]
		}
	} else {
		g.Name = os.Getenv("TERM_PROGRAM")
		if ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ); err == nil && ws.Col > 0 && ws.Row > 0 && ws.Xpixel > 0 && ws.Ypixel > 0 {
			g.CellW, g.CellH = int(ws.Xpixel)/int(ws.Col), int(ws.Ypixel)/int(ws.Row)
		}
	}
	if g.Name == "" {
		g.Name = os.Getenv("TERM")
	}
	r := t.probe()
	g.Kitty = r.kitty
	if g.CellW == 0 || g.CellH == 0 {
		g.CellW, g.CellH = r.cellW, r.cellH
	}
	if g.CellW == 0 || g.CellH == 0 {
		g.CellW, g.CellH = 10, 20
	}
	return g
}
