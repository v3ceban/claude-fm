package render

import (
	"cmp"
	"runtime"
	"slices"
	"sync"
)

const (
	binBits    = 5
	binCount   = 1 << (3 * binBits)
	maxColours = 256
)

type Quantizer struct {
	w, h    int
	Idx     []uint8
	Palette [][3]uint8
	rgb     []uint8
	bins    []uint16
	used    []int
	count   []uint32
	sum     [][3]uint32
	binMap  []int16
	workers int
}

func NewQuantizer(w, h int) *Quantizer {
	q := &Quantizer{w: w, h: h, Idx: make([]uint8, w*h), rgb: make([]uint8, w*h*3), bins: make([]uint16, w*h)}
	q.count = make([]uint32, binCount)
	q.sum = make([][3]uint32, binCount)
	q.binMap = make([]int16, binCount)
	q.workers = min(runtime.NumCPU(), 8)
	return q
}

func bin(r, g, b uint8) uint16 {
	return uint16(r>>(8-binBits))<<(2*binBits) | uint16(g>>(8-binBits))<<binBits | uint16(b>>(8-binBits))
}

func (q *Quantizer) parallel(n int, fn func(lo, hi int)) {
	var wg sync.WaitGroup
	per := (n + q.workers - 1) / q.workers
	for lo := 0; lo < n; lo += per {
		hi := min(lo+per, n)
		wg.Go(func() { fn(lo, hi) })
	}
	wg.Wait()
}

func (q *Quantizer) convert(yuv []byte) {
	w, h := q.w, q.h
	yP := yuv[:w*h]
	cw := w / 2
	uP := yuv[w*h : w*h+cw*(h/2)]
	vP := yuv[w*h+cw*(h/2):]
	q.parallel(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			crow := (y / 2) * cw
			out := q.rgb[y*w*3 : (y+1)*w*3]
			bins := q.bins[y*w : (y+1)*w]
			for x := range w {
				yy := int32(yP[y*w+x]) - 16
				u := int32(uP[crow+x/2]) - 128
				v := int32(vP[crow+x/2]) - 128
				yv := 1192 * yy
				r, g, b := clamp8((yv+1836*v+512)>>10), clamp8((yv-218*u-546*v+512)>>10), clamp8((yv+2163*u+512)>>10)
				out[x*3], out[x*3+1], out[x*3+2] = r, g, b
				bins[x] = bin(r, g, b)
			}
		}
	})
}

func (q *Quantizer) Quantize(yuv []byte) {
	q.convert(yuv)
	clear(q.count)
	used := q.used[:0]
	for i, b := range q.bins {
		if q.count[b] == 0 {
			used = append(used, int(b))
			q.sum[b] = [3]uint32{}
		}
		q.count[b]++
		q.sum[b][0] += uint32(q.rgb[i*3])
		q.sum[b][1] += uint32(q.rgb[i*3+1])
		q.sum[b][2] += uint32(q.rgb[i*3+2])
	}
	q.used = used
	slices.SortFunc(used, func(a, b int) int { return cmp.Compare(q.count[b], q.count[a]) })
	n := min(len(used), maxColours)
	q.Palette = q.Palette[:0]
	for i, b := range used[:n] {
		c := q.count[b]
		q.Palette = append(q.Palette, [3]uint8{uint8(q.sum[b][0] / c), uint8(q.sum[b][1] / c), uint8(q.sum[b][2] / c)})
		q.binMap[b] = int16(i)
	}
	rest := used[n:]
	q.parallel(len(rest), func(lo, hi int) {
		for _, b := range rest[lo:hi] {
			c := q.count[b]
			r, g, bl := int(q.sum[b][0]/c), int(q.sum[b][1]/c), int(q.sum[b][2]/c)
			best, bestD := 0, 1<<30
			for i, p := range q.Palette {
				d := (r-int(p[0]))*(r-int(p[0])) + (g-int(p[1]))*(g-int(p[1])) + (bl-int(p[2]))*(bl-int(p[2]))
				if d < bestD {
					best, bestD = i, d
				}
			}
			q.binMap[b] = int16(best)
		}
	})
	q.parallel(len(q.Idx), func(lo, hi int) {
		for i := lo; i < hi; i++ {
			q.Idx[i] = uint8(q.binMap[q.bins[i]])
		}
	})
}

func clamp8(v int32) uint8 { return uint8(min(max(v, 0), 255)) }
