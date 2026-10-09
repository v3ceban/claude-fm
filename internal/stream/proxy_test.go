package stream

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fakeUpstream(t *testing.T, head *atomic.Int64, oldestServed int64) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/seg/") {
			var sq int64
			fmt.Sscanf(r.URL.Path, "/seg/x/sq/%d/", &sq)
			if sq < oldestServed {
				http.NotFound(w, r)
				return
			}
			w.Write([]byte("data"))
			return
		}
		h := head.Load()
		fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:%d\n#EXT-X-DISCONTINUITY-SEQUENCE:7\n", h-2)
		for sq := h - 2; sq <= h; sq++ {
			fmt.Fprintf(w, "#EXTINF:2.0,\n%s/seg/x/sq/%d/tail\n", srv.URL, sq)
		}
	}))
	return srv
}

func fetch(t *testing.T, u string) string {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestProxyExtendsHistory(t *testing.T) {
	var head atomic.Int64
	head.Store(1000)
	up := fakeUpstream(t, &head, 950)
	defer up.Close()
	p, err := StartProxy([]string{up.URL + "/v.m3u8"}, 200)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for p.Edge(0).Oldest != 950 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	e := p.Edge(0)
	if !e.Extended || e.Newest != 1000 || e.Oldest != 950 || e.SegmentSeconds != 2 {
		t.Fatalf("edge = %+v", e)
	}
	body := fetch(t, p.URLs()[0])
	if !strings.Contains(body, "#EXT-X-MEDIA-SEQUENCE:950\n") || strings.Count(body, "#EXTINF:") != 51 ||
		!strings.Contains(body, "/sq/950/tail") || !strings.Contains(body, "/sq/1000/tail") || strings.Contains(body, "DISCONTINUITY") {
		t.Fatalf("served playlist:\n%s", body)
	}
	head.Store(1003)
	for p.Edge(0).Newest != 1003 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if body := fetch(t, p.URLs()[0]); !strings.Contains(body, "/sq/1003/tail") || strings.Contains(body, "#EXT-X-ENDLIST") {
		t.Fatalf("playlist should follow the live edge:\n%s", body)
	}
	if h := p.Edge(0).HistorySeconds(); h != 108 {
		t.Fatalf("history = %v", h)
	}
}

func TestProxyPassthroughWithoutTemplate(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-MEDIA-SEQUENCE:10\n#EXTINF:4.0,\nhttp://x/a10.ts\n#EXTINF:4.0,\nhttp://x/a11.ts\n")
	}))
	defer up.Close()
	p, err := StartProxy([]string{up.URL}, 3600)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if dead, err := StartProxy([]string{"http://127.0.0.1:1/none.m3u8"}, 10); err == nil {
		defer dead.Close()
		if dead.Wait(ctx) == nil {
			t.Fatal("waiting on a dead upstream should fail")
		}
	}
	e := p.Edge(0)
	if e.Extended || e.Newest != 11 || e.Oldest != 10 || e.SegmentSeconds != 4 {
		t.Fatalf("edge = %+v", e)
	}
	if body := fetch(t, p.URLs()[0]); !strings.Contains(body, "a11.ts") {
		t.Fatalf("passthrough body:\n%s", body)
	}
}

func TestParsePlaylistTemplate(t *testing.T) {
	p := parsePlaylist("#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:5\n#EXTINF:2.0,\nhttps://h/v/sq/5/x%20y\n#EXTINF:2.0,\nhttps://h/v/sq/6/x%20y\n")
	if p.count != 2 || p.firstSQ != 5 || p.pre != "https://h/v/sq/" || p.suf != "/x%20y" || p.segDur != 2 {
		t.Fatalf("parsed = %+v", p)
	}
}
