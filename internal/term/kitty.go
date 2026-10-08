package term

import (
	"encoding/base64"
	"fmt"
)

const chunkSize = 4096

type ImageWriter struct {
	enc, payload, out []byte
	n                 int
}

// Frame returns the bytes that show png at row/col (1-based screen cells) over cols x rows and drop the previous frame.
func (w *ImageWriter) Frame(row, col, cols, rows int, png []byte) []byte {
	w.enc = base64.StdEncoding.AppendEncode(w.enc[:0], png)
	id := 1 + w.n%2
	p := fmt.Appendf(w.payload[:0], "\x1b7\x1b[?2026h\x1b[%d;%dH", row, col)
	for i := 0; i < len(w.enc); i += chunkSize {
		end := min(i+chunkSize, len(w.enc))
		more := 1
		if end == len(w.enc) {
			more = 0
		}
		if i == 0 {
			p = fmt.Appendf(p, "\x1b_Ga=T,f=100,i=%d,q=2,c=%d,r=%d,z=%d,m=%d;", id, cols, rows, -1000000000+w.n, more)
		} else {
			p = fmt.Appendf(p, "\x1b_Gm=%d,q=2;", more)
		}
		p = append(p, w.enc[i:end]...)
		p = append(p, "\x1b\\"...)
	}
	p = fmt.Appendf(p, "\x1b_Ga=d,d=I,i=%d,q=2\x1b\\\x1b[?2026l\x1b8", 3-id)
	w.payload = p
	w.n++
	w.out = Passthrough(w.out[:0], p)
	return w.out
}

func DeleteImages() []byte {
	return Passthrough(nil, []byte("\x1b_Ga=d,d=A,q=2\x1b\\"))
}
