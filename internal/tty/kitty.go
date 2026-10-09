package tty

import (
	"encoding/base64"
	"fmt"
)

const chunkSize = 4096

type ImageWriter struct {
	enc, payload, out []byte
	n                 int
}

const overlayID = 3

func (w *ImageWriter) Frame(row, col, cols, rows int, png []byte) []byte {
	id := 1 + w.n%2
	p := fmt.Appendf(w.payload[:0], "\x1b7\x1b[?2026h\x1b[%d;%dH", row, col)
	p = w.place(p, id, cols, rows, -1000000000+w.n, png)
	p = appendDelete(p, 3-id)
	w.n++
	return w.flush(append(p, "\x1b[?2026l\x1b8"...))
}

func (w *ImageWriter) Overlay(row, col, cols, rows int, png []byte) []byte {
	p := fmt.Appendf(w.payload[:0], "\x1b7\x1b[%d;%dH", row, col)
	p = appendDelete(p, overlayID)
	p = w.place(p, overlayID, cols, rows, 1, png)
	return w.flush(append(p, "\x1b8"...))
}

func (w *ImageWriter) flush(p []byte) []byte {
	w.payload = p
	w.out = Passthrough(w.out[:0], p)
	return w.out
}

func appendDelete(p []byte, id int) []byte {
	return fmt.Appendf(p, "\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", id)
}

func (w *ImageWriter) place(p []byte, id, cols, rows, z int, png []byte) []byte {
	w.enc = base64.StdEncoding.AppendEncode(w.enc[:0], png)
	for i := 0; i < len(w.enc); i += chunkSize {
		end := min(i+chunkSize, len(w.enc))
		more := 1
		if end == len(w.enc) {
			more = 0
		}
		if i == 0 {
			p = fmt.Appendf(p, "\x1b_Ga=T,f=100,i=%d,q=2,c=%d,r=%d,z=%d,m=%d;", id, cols, rows, z, more)
		} else {
			p = fmt.Appendf(p, "\x1b_Gm=%d,q=2;", more)
		}
		p = append(p, w.enc[i:end]...)
		p = append(p, "\x1b\\"...)
	}
	return p
}

func DeleteImages() []byte {
	return Passthrough(nil, []byte("\x1b_Ga=d,d=A,q=2\x1b\\"))
}

func DeleteOverlay() []byte {
	return Passthrough(nil, appendDelete(nil, overlayID))
}
