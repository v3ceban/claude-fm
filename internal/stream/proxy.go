package stream

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var sqRe = regexp.MustCompile(`/sq/(\d+)/`)

type Proxy struct {
	srv    *http.Server
	ln     net.Listener
	relays []*relay
	cancel context.CancelFunc
}

type relay struct {
	upstream string
	history  float64
	client   *http.Client

	mu       sync.Mutex
	header   []string
	pre, suf string
	newest   int64
	oldest   int64
	segDur   float64
	ok       bool
	probed   bool
	body     string
}

func StartProxy(upstreams []string, historySeconds float64) (*Proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Proxy{ln: ln, cancel: cancel}
	mux := http.NewServeMux()
	client := &http.Client{Timeout: 10 * time.Second}
	for i, u := range upstreams {
		r := &relay{upstream: u, history: historySeconds, client: client, segDur: 2}
		p.relays = append(p.relays, r)
		mux.HandleFunc(fmt.Sprintf("/%d.m3u8", i), r.serve)
		go r.loop(ctx)
	}
	p.srv = &http.Server{Handler: mux}
	go p.srv.Serve(ln)
	return p, nil
}

func (p *Proxy) Wait(ctx context.Context) error {
	for _, r := range p.relays {
		for !r.ready() {
			select {
			case <-ctx.Done():
				return fmt.Errorf("upstream playlist did not load")
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	return nil
}

func (p *Proxy) URLs() []string {
	out := make([]string, len(p.relays))
	for i := range p.relays {
		out[i] = fmt.Sprintf("http://%s/%d.m3u8", p.ln.Addr(), i)
	}
	return out
}

func (p *Proxy) Close() {
	p.cancel()
	p.srv.Close()
}

type Edge struct {
	Newest         int64
	Oldest         int64
	SegmentSeconds float64
	Extended       bool
}

func (p *Proxy) Edge(i int) Edge {
	r := p.relays[i]
	r.mu.Lock()
	defer r.mu.Unlock()
	return Edge{Newest: r.newest, Oldest: r.oldest, SegmentSeconds: r.segDur, Extended: r.pre != ""}
}

func (e Edge) HistorySeconds() float64 { return float64(e.Newest-e.Oldest+1) * e.SegmentSeconds }

func (r *relay) ready() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ok
}

func (r *relay) loop(ctx context.Context) {
	for {
		r.refresh(ctx)
		r.mu.Lock()
		d := max(r.segDur/2, 0.5)
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(d * float64(time.Second))):
		}
	}
}

func (r *relay) refresh(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.upstream, nil)
	if err != nil {
		return
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		return
	}
	pl := parsePlaylist(absolutize(r.upstream, string(body)))
	if pl.count == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.header, r.segDur, r.pre, r.suf = pl.header, pl.segDur, pl.pre, pl.suf
	if pl.pre == "" {
		r.newest, r.oldest, r.ok, r.body = pl.firstSeq+int64(pl.count)-1, pl.firstSeq, true, pl.raw
		return
	}
	r.newest = pl.firstSQ + int64(pl.count) - 1
	want := max(r.floor(), 0)
	if !r.ok {
		r.oldest = pl.firstSQ
	}
	r.oldest = max(r.oldest, want)
	if r.oldest > want && !r.probed {
		r.probed = true
		go r.probe(ctx, want, r.oldest)
	}
	r.ok = true
	r.render()
}

func (r *relay) floor() int64 { return r.newest - int64(r.history/r.segDur) + 1 }

func (r *relay) segment(sq int64) string { return r.pre + strconv.FormatInt(sq, 10) + r.suf }

func (r *relay) probe(ctx context.Context, lo, hi int64) {
	r.mu.Lock()
	pre, suf := r.pre, r.suf
	r.mu.Unlock()
	available := func(sq int64) bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pre+strconv.FormatInt(sq, 10)+suf, nil)
		if err != nil {
			return false
		}
		req.Header.Set("Range", "bytes=0-0")
		resp, err := r.client.Do(req)
		if err != nil {
			return false
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		return resp.StatusCode < 300
	}
	oldest := lo + int64(sort.Search(int(hi-lo), func(i int) bool { return available(lo + int64(i)) }))
	r.mu.Lock()
	r.oldest = max(oldest, r.floor())
	r.render()
	r.mu.Unlock()
}

func (r *relay) serve(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	r.mu.Lock()
	body := r.body
	r.mu.Unlock()
	io.WriteString(w, body)
}

func (r *relay) render() {
	var b strings.Builder
	for _, h := range r.header {
		b.WriteString(h)
		b.WriteByte('\n')
	}
	start := max(r.oldest, 0)
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", start)
	extinf := fmt.Sprintf("#EXTINF:%.3f,\n", r.segDur)
	for sq := start; sq <= r.newest; sq++ {
		b.WriteString(extinf)
		b.WriteString(r.segment(sq))
		b.WriteByte('\n')
	}
	r.body = b.String()
}

func absolutize(base, body string) string {
	b, err := url.Parse(base)
	if err != nil {
		return body
	}
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if u, err := url.Parse(t); err == nil && !u.IsAbs() {
			lines[i] = b.ResolveReference(u).String()
		}
	}
	return strings.Join(lines, "\n")
}

type playlist struct {
	raw      string
	header   []string
	pre, suf string
	firstSQ  int64
	firstSeq int64
	count    int
	segDur   float64
}

func parsePlaylist(body string) playlist {
	p := playlist{raw: body}
	total := 0.0
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "#EXTINF:"):
			d, _, _ := strings.Cut(line[len("#EXTINF:"):], ",")
			if v, err := strconv.ParseFloat(d, 64); err == nil {
				total += v
			}
			p.count++
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			p.firstSeq, _ = strconv.ParseInt(line[len("#EXT-X-MEDIA-SEQUENCE:"):], 10, 64)
		case strings.HasPrefix(line, "#EXT-X-VERSION") || strings.HasPrefix(line, "#EXTM3U") || strings.HasPrefix(line, "#EXT-X-TARGETDURATION") || strings.HasPrefix(line, "#EXT-X-INDEPENDENT-SEGMENTS"):
			p.header = append(p.header, line)
		case strings.HasPrefix(line, "#"):
		case p.count == 1 && p.pre == "":
			if m := sqRe.FindStringSubmatchIndex(line); m != nil {
				p.firstSQ, _ = strconv.ParseInt(line[m[2]:m[3]], 10, 64)
				p.pre, p.suf = line[:m[2]], line[m[3]:]
			}
		}
	}
	if p.count > 0 {
		p.segDur = total / float64(p.count)
	}
	if p.segDur <= 0 {
		p.segDur = 2
	}
	return p
}
