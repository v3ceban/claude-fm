// Command claude-fm plays the Claude FM YouTube stream inside an iTerm2 terminal.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"runtime/debug"
	"time"
	"unicode/utf8"

	"github.com/v3ceban/claude-fm/internal/pipeline"
	"github.com/v3ceban/claude-fm/internal/render"
	"github.com/v3ceban/claude-fm/internal/stream"
	"github.com/v3ceban/claude-fm/internal/tty"
)

const streamURL = "https://clau.de/radio"

const clearScreen = "\x1b[0m\x1b[2J"

type source struct{ w, h, quality int }

var sources = []source{{1280, 720, 720}, {854, 480, 480}, {640, 360, 480}, {426, 240, 240}}

type app struct {
	input  string
	volume int
	fps    float64
	log    *log.Logger

	cookies    string
	useCookies bool
	exitMsg    string

	tty        *tty.Terminal
	gfx        tty.Graphics
	cols, rows int
	src        source

	png     *render.PNGFrame
	imgX    int
	imgY    int
	imgCols int
	imgRows int
	panes   *tty.PaneWatcher
	pane    tty.PaneState
	shown   bool
	cleared bool
	status  string
	img     tty.ImageWriter

	quit    bool
	frames  int
	nextOut time.Time
	sess    *pipeline.Session
	late    float64
}

func main() {
	a := &app{}
	flag.StringVar(&a.input, "input", "", "play a local file or direct media URL instead of Claude FM")
	flag.IntVar(&a.volume, "volume", 100, "volume 0-130")
	flag.Float64Var(&a.fps, "fps", 30, "max frames per second to draw")
	flag.StringVar(&a.cookies, "cookies", "", "if YouTube asks for a bot check, retry with this `browser`'s cookies (chrome, firefox, safari, …)")
	cellPx := flag.String("cell-px", "", "terminal cell size in pixels as WxH, overrides detection")
	logPath := flag.String("log", "", "debug log file")
	flag.Parse()

	a.log = log.New(io.Discard, "", 0)
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			defer f.Close()
			a.log = log.New(f, "", log.Ltime|log.Lmicroseconds)
		}
	}
	t, err := tty.Open(a.log.Printf)
	if err != nil {
		fmt.Fprintln(os.Stderr, "need a terminal:", err)
		os.Exit(1)
	}
	a.tty, a.gfx = t, t.Gfx
	if !a.gfx.Kitty && os.Getenv("CLAUDE_FM_FORCE_GRAPHICS") == "" {
		t.Restore()
		fmt.Fprintf(os.Stderr, "claude-fm needs a terminal with kitty graphics support (kitty, ghostty, WezTerm, iTerm2 3.5+); %q did not answer the graphics query\n", a.gfx.Name)
		if os.Getenv("NVIM") != "" {
			fmt.Fprintln(os.Stderr, "Neovim's built-in terminal sits in between and does not pass graphics through; run it in a plain terminal or tmux pane")
		}
		os.Exit(1)
	}
	if *cellPx != "" {
		fmt.Sscanf(*cellPx, "%dx%d", &a.gfx.CellW, &a.gfx.CellH)
	}
	defer func() {
		a.deleteImages()
		t.Restore()
		tty.RefreshClient()
		if a.exitMsg != "" {
			fmt.Fprintln(os.Stderr, a.exitMsg)
		}
	}()
	defer tty.EnableFocusEvents()()
	a.panes = tty.WatchPane(200 * time.Millisecond)
	defer a.panes.Stop()
	a.cols, a.rows = t.Size()
	a.pane = a.panes.Get()
	a.log.Printf("terminal %q, cell %dx%d px, pane %dx%d", a.gfx.Name, a.gfx.CellW, a.gfx.CellH, a.cols, a.rows)
	a.setSource(a.chooseSource())
	a.relayout()
	a.run()
}

func (a *app) fitImage() {
	rows := a.rows
	if a.pane.LastRow {
		rows--
	}
	pw, ph := float64(a.cols*a.gfx.CellW), float64(rows*a.gfx.CellH)
	if pw/ph > 16.0/9 {
		a.imgRows = rows
		a.imgCols = int(ph*16/9/float64(a.gfx.CellW) + 0.5)
	} else {
		a.imgCols = a.cols
		a.imgRows = int(pw*9/16/float64(a.gfx.CellH) + 0.5)
	}
	a.imgCols, a.imgRows = min(max(a.imgCols, 1), a.cols), min(max(a.imgRows, 1), rows)
	a.imgX, a.imgY = (a.cols-a.imgCols)/2, (rows-a.imgRows)/2
}

func (a *app) chooseSource() source {
	a.fitImage()
	pw, ph := a.imgCols*a.gfx.CellW, a.imgRows*a.gfx.CellH
	for _, s := range sources {
		if s.w <= pw && s.h <= ph {
			return s
		}
	}
	return sources[len(sources)-1]
}

func (a *app) setSource(s source) {
	a.src = s
	a.png = render.NewPNGFrame(s.w, s.h)
}

func (a *app) relayout() {
	a.fitImage()
	a.redraw()
}

func (a *app) write(s string) { os.Stdout.WriteString(s) }

func (a *app) redraw() {
	a.write(clearScreen)
	a.cleared = false
	if a.status != "" {
		x := max((a.cols-utf8.RuneCountInString(a.status))/2, 0)
		a.write(fmt.Sprintf("\x1b[%d;%dH\x1b[0m%s", a.rows/2+1, x+1, a.status))
	}
}

func (a *app) message(msg string) {
	a.status = msg
	a.redraw()
}

func (a *app) clearOnce() {
	if !a.cleared {
		a.cleared = true
		a.status = ""
		a.write(clearScreen)
	}
}

func (a *app) hideImage() {
	if a.shown {
		a.shown = false
		a.deleteImages()
	}
}

func (a *app) deleteImages() { os.Stdout.Write(tty.DeleteImages()) }

func (a *app) run() {
	backoff := time.Second
	retry := func(msg string) (quit bool) {
		a.message(msg)
		if a.wait(backoff) {
			return true
		}
		backoff = min(backoff*2, 30*time.Second)
		return false
	}
	for !a.quit {
		urls := []string{a.input}
		if a.input == "" {
			a.message("Resolving Claude FM stream…")
			u, err := a.resolve()
			if a.quit {
				return
			}
			if err != nil {
				a.log.Printf("resolve: %v", err)
				if errors.Is(err, stream.ErrBotCheck) {
					a.botCheck()
					return
				}
				if errors.Is(err, errCookies) {
					a.stop(fmt.Sprintf("Could not use %s cookies (q to quit)", a.cookies),
						"claude-fm: "+err.Error())
					return
				}
				if retry(fmt.Sprintf("Could not resolve stream (%v). Retrying…", shortErr(err))) {
					return
				}
				continue
			}
			urls = u
		}
		a.message("Connecting…")
		sess, err := pipeline.Start(pipeline.Opts{URLs: urls, Local: a.input != "", SrcW: a.src.w, SrcH: a.src.h,
			Volume: a.volume, Log: a.log})
		if err != nil {
			a.message(fmt.Sprintf("Failed to start pipeline: %v", shortErr(err)))
			if a.wait(2 * time.Second) {
				return
			}
			continue
		}
		start := time.Now()
		a.sess = sess
		restart := a.present(sess)
		sess.Stop()
		debug.FreeOSMemory()
		if a.quit {
			return
		}
		if time.Since(start) > 30*time.Second {
			backoff = time.Second
		}
		if !restart {
			a.log.Printf("session ended: %v", sess.Err())
			if a.input != "" {
				return
			}
			if retry(fmt.Sprintf("Stream ended (%v). Reconnecting…", shortErr(sess.Err()))) {
				return
			}
		}
	}
}

func shortErr(err error) string {
	if err == nil {
		return ""
	}
	s := []rune(err.Error())
	if len(s) > 80 {
		return string(s[:80]) + "…"
	}
	return string(s)
}

func (a *app) onResize() {
	a.cols, a.rows = a.tty.Size()
	a.pane = a.panes.Refresh()
	a.relayout()
}

func (a *app) growSource() bool {
	if s := a.chooseSource(); s.w > a.src.w || s.h > a.src.h {
		a.setSource(s)
		return true
	}
	return false
}

func (a *app) wait(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			return a.quit
		case <-a.tty.Quit:
			a.quit = true
			return true
		case <-a.tty.Winch:
			a.onResize()
			a.growSource()
		case <-a.tty.Focus:
		}
	}
}

var errCookies = errors.New("could not use browser cookies")

func (a *app) botCheck() {
	if a.cookies == "" {
		a.stop("YouTube wants a bot check: restart with -cookies chrome (q to quit)",
			"claude-fm: YouTube asked for a bot check. Restart with -cookies <browser>\n"+
				"(chrome, firefox, safari, brave, edge, …) naming a browser where you are signed in to YouTube.")
	} else {
		a.stop(fmt.Sprintf("YouTube wants a bot check even with %s cookies (q to quit)", a.cookies),
			fmt.Sprintf("claude-fm: YouTube asked for a bot check even with %s cookies.\n"+
				"Sign in to YouTube in %s, or try another browser, then restart.", a.cookies, a.cookies))
	}
}

func (a *app) stop(screen, exit string) {
	a.message(screen)
	a.exitMsg = exit
	for !a.wait(time.Hour) {
	}
}

func (a *app) resolve() ([]string, error) {
	if a.useCookies {
		return a.resolveWith(a.cookies)
	}
	urls, err := a.resolveWith("")
	if !errors.Is(err, stream.ErrBotCheck) || a.cookies == "" {
		return urls, err
	}
	a.log.Printf("resolve: %v; retrying with %s cookies", err, a.cookies)
	a.message(fmt.Sprintf("YouTube wants a bot check, retrying with %s cookies…", a.cookies))
	a.useCookies = true
	urls, err = a.resolveWith(a.cookies)
	if err != nil && !errors.Is(err, stream.ErrBotCheck) && !a.quit {
		return nil, fmt.Errorf("%w: %w", errCookies, err)
	}
	return urls, err
}

func (a *app) resolveWith(cookies string) ([]string, error) {
	type res struct {
		urls []string
		err  error
	}
	ch := make(chan res, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	quality := a.src.quality
	go func() {
		u, err := stream.Resolve(ctx, streamURL, quality, cookies)
		ch <- res{u, err}
	}()
	for {
		select {
		case r := <-ch:
			return r.urls, r.err
		case <-a.tty.Quit:
			a.quit = true
			cancel()
			return nil, fmt.Errorf("quit")
		case <-a.tty.Winch:
			a.onResize()
			a.growSource()
		case <-a.tty.Focus:
		}
	}
}

func (a *app) present(s *pipeline.Session) bool {
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	shownFirst := false
	var grow <-chan time.Time
	for {
		select {
		case <-s.Done:
			return false
		case <-timer.C:
		case <-s.Frames.Notify():
		case <-a.tty.Focus:
			a.pane = a.panes.Refresh()
			if !a.pane.Visible {
				a.hideImage()
			}
		case <-a.tty.Quit:
			a.quit = true
			return false
		case <-a.tty.Winch:
			a.onResize()
			if a.input == "" {
				grow = time.After(300 * time.Millisecond)
			}
		case <-grow:
			grow = nil
			if a.growSource() {
				return true
			}
		}

		clk, ok := s.Clock()
		if !ok {
			if !shownFirst {
				if f := s.Frames.PeekFirst(); f != nil {
					a.draw(f)
					s.Frames.Put(f.Buf)
					shownFirst = true
				}
			}
			timer.Reset(40 * time.Millisecond)
			continue
		}
		if wait := time.Until(a.nextOut); wait > 0 {
			timer.Reset(wait)
			continue
		}
		target := clk + 0.008
		f, next, hasNext := s.Frames.TakeDue(target)
		if f != nil {
			a.late = clk - f.PTS
			a.draw(f)
			s.Frames.Put(f.Buf)
		}
		if a.quit {
			return false
		}
		d := 20 * time.Millisecond
		if hasNext {
			d = time.Duration((next - target) * float64(time.Second))
			d = min(max(d, time.Millisecond), 50*time.Millisecond)
		}
		timer.Reset(d)
	}
}

func (a *app) draw(f *pipeline.Frame) {
	t := time.Now()
	a.pane = a.panes.Get()
	if !a.pane.Visible {
		a.hideImage()
		return
	}
	a.clearOnce()
	a.shown = true
	b := a.img.Frame(a.pane.Top+a.imgY+1, a.pane.Left+a.imgX+1, a.imgCols, a.imgRows, a.png.Encode(f.Buf))
	if _, err := os.Stdout.Write(b); err != nil {
		a.quit = true
	}
	a.frames++
	a.nextOut = a.nextOut.Add(time.Duration(float64(time.Second) / max(a.fps, 1)))
	if a.nextOut.Before(t) {
		a.nextOut = t
	}
	if a.frames%150 == 0 && a.log.Writer() != io.Discard {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		a.log.Printf("frame %d: %dx%d src, %d KB, render %s, heap %d MB, queue %d, drops %d, late %.0fms, audio buffered %.1fs",
			a.frames, a.src.w, a.src.h, len(b)/1024, time.Since(t).Round(100*time.Microsecond),
			m.HeapAlloc>>20, a.sess.Frames.Len(), a.sess.Frames.Drops(), a.late*1000, a.sess.AudioBuffered())
	}
}
