package main

// Screen checks.
//
// Some apps give no ad marker in their media session, but draw an ad overlay
// over the video. The video plays on a hardware layer, so a screen capture
// holds only the app's own drawing. A screen check looks for a fixed piece of
// text from the ad overlay of the app, such as a word, in an area of the
// screen capture. See youtube.go and tubi.go.

import "image"

// Screen size that each mask and each search area uses.
const (
	refWidth  = 1920
	refHeight = 1080
)

// An overlayText is a fixed piece of text from the ad overlay of an app, and
// the area of the screen where it shows.
type overlayText struct {
	mask

	// The search area for the top left corner of the mask, in 1920x1080
	// pixels. It allows for a move of the overlay layout.
	x0, x1, y0, y1 int

	// A pixel is bright when its luminance is above this value, of 255.
	threshold uint32

	// Percent of mask pixels that may differ from the screen.
	maxMismatchPercent int
}

// mask is a parsed text mask. In the text of a mask, a '#' is a bright pixel
// and a '.' is a dark pixel.
type mask struct {
	w, h   int
	bright []bool
	count  int // number of bright pixels
}

func parseMask(rows []string) mask {
	m := mask{w: len(rows[0]), h: len(rows)}
	m.bright = make([]bool, m.w*m.h)
	for y, row := range rows {
		for x, c := range row {
			if c == '#' {
				m.bright[y*m.w+x] = true
				m.count++
			}
		}
	}
	return m
}

// on reports whether the screen shows the text.
func (t overlayText) on(img image.Image) bool {
	m := t.mask
	gw := t.x1 - t.x0 + m.w
	gh := t.y1 - t.y0 + m.h
	grid := brightGrid(img, t.x0, t.y0, gw, gh, t.threshold)
	limit := m.w * m.h * t.maxMismatchPercent / 100

	// sum[y*sw+x] is the number of bright pixels above and left of (x, y).
	// It gives the bright count of each spot fast, so most spots need no
	// pixel compare.
	sw := gw + 1
	sum := make([]int, sw*(gh+1))
	for y := 0; y < gh; y++ {
		row := 0
		for x := 0; x < gw; x++ {
			if grid[y*gw+x] {
				row++
			}
			sum[(y+1)*sw+x+1] = sum[y*sw+x+1] + row
		}
	}

	for oy := 0; oy+m.h <= gh; oy++ {
		for ox := 0; ox+m.w <= gw; ox++ {
			n := sum[(oy+m.h)*sw+ox+m.w] - sum[oy*sw+ox+m.w] - sum[(oy+m.h)*sw+ox] + sum[oy*sw+ox]
			if n-m.count > limit || m.count-n > limit {
				continue
			}
			if m.mismatches(grid, gw, ox, oy, limit) <= limit {
				return true
			}
		}
	}
	return false
}

// mismatches counts the mask pixels that differ from the grid at (ox, oy).
// It stops after the count passes limit.
func (m mask) mismatches(grid []bool, gw, ox, oy, limit int) int {
	n := 0
	for y := 0; y < m.h; y++ {
		g := grid[(oy+y)*gw+ox:]
		b := m.bright[y*m.w:]
		for x := 0; x < m.w; x++ {
			if g[x] != b[x] {
				n++
			}
		}
		if n > limit {
			return n
		}
	}
	return n
}

// brightGrid returns the bright pixels of a w x h area of the screen. The
// area starts at (x0, y0) in 1920x1080 pixels. A screen of another size is
// scaled to 1920x1080.
func brightGrid(img image.Image, x0, y0, w, h int, threshold uint32) []bool {
	b := img.Bounds()
	grid := make([]bool, w*h)
	for y := 0; y < h; y++ {
		iy := b.Min.Y + (y0+y)*b.Dy()/refHeight
		for x := 0; x < w; x++ {
			ix := b.Min.X + (x0+x)*b.Dx()/refWidth
			grid[y*w+x] = isBright(img, ix, iy, threshold)
		}
	}
	return grid
}

// isBright reports whether the pixel has a luminance above threshold, of 255.
func isBright(img image.Image, x, y int, threshold uint32) bool {
	var r, g, b uint32
	if n, ok := img.(*image.NRGBA); ok {
		p := n.Pix[n.PixOffset(x, y):]
		r, g, b = uint32(p[0]), uint32(p[1]), uint32(p[2])
	} else {
		r16, g16, b16, _ := img.At(x, y).RGBA()
		r, g, b = r16>>8, g16>>8, b16>>8
	}
	return (299*r+587*g+114*b)/1000 > threshold
}
