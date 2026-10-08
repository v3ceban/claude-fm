// Package render turns decoded video frames into PNG images for the terminal.
package render

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
)

type PNGFrame struct {
	q    *Quantizer
	img  *image.Paletted
	enc  png.Encoder
	pool pngPool
	buf  bytes.Buffer
}

func NewPNGFrame(w, h int) *PNGFrame {
	q := NewQuantizer(w, h)
	img := &image.Paletted{Pix: q.Idx, Stride: w, Rect: image.Rect(0, 0, w, h), Palette: make(color.Palette, 0, maxColours)}
	p := &PNGFrame{q: q, img: img}
	p.enc.CompressionLevel = png.BestSpeed
	p.enc.BufferPool = &p.pool
	return p
}

type pngPool struct{ b *png.EncoderBuffer }

func (p *pngPool) Get() *png.EncoderBuffer  { return p.b }
func (p *pngPool) Put(b *png.EncoderBuffer) { p.b = b }

func (p *PNGFrame) Encode(yuv []byte) []byte {
	p.q.Quantize(yuv)
	p.img.Palette = p.img.Palette[:0]
	for _, c := range p.q.Palette {
		p.img.Palette = append(p.img.Palette, color.RGBA{c[0], c[1], c[2], 255})
	}
	p.buf.Reset()
	p.enc.Encode(&p.buf, p.img)
	return p.buf.Bytes()
}
