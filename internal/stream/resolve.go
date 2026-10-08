// Package stream resolves the Claude FM page to playable media URLs.
package stream

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func Resolve(ctx context.Context, page string, maxHeight int) ([]string, error) {
	f := fmt.Sprintf("bv*[height<=%d][protocol^=m3u8]+ba[protocol^=m3u8]/bv*[height<=%d]+ba/b[height<=%d]/b", maxHeight, maxHeight, maxHeight)
	cmd := exec.CommandContext(ctx, "yt-dlp", "--no-warnings", "--no-playlist", "-f", f, "--print", "urls", page)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if i := strings.LastIndex(msg, "ERROR:"); i >= 0 {
			msg = msg[i:]
		}
		return nil, fmt.Errorf("yt-dlp: %v: %s", err, msg)
	}
	urls := strings.Fields(out.String())
	if len(urls) == 0 {
		return nil, fmt.Errorf("yt-dlp returned no stream URLs")
	}
	return urls, nil
}
