package main

// YouTube detection.
//
// YouTube gives no ad marker in its media session. During an ad it reports
// state PLAYING at speed 1.0 with the title of the video, the same as during
// the show. Each ad plays as a separate clip from position 0. A new video also
// starts at position 0, so the position does not help either.
//
// The ad overlay is the marker. The video plays on a hardware layer, so a
// screen capture holds only the app's own drawing. During the show the capture
// is blank. During an ad, the overlay shows the advertiser and the word
// "Sponsored" at the bottom left. The check looks for that word.

import (
	"bytes"
	"image"
	"image/png"
	"log"
)

const youTubePackage = "com.google.android.youtube.tv"

// sponsoredMask is the word "Sponsored" from the YouTube ad overlay, cut from
// a 1920x1080 screen capture. A '#' is a bright pixel. The top left corner of
// the word was at x=175, y=986.
var sponsoredMask = []string{
	"...............................................................................................................####",
	"...######......................................................................................................####",
	"..#########....................................................................................................####",
	".###########...................................................................................................####",
	"####....#####..................................................................................................####",
	"####.....####..#########........#####......####.####.......#####.......#####......####.##.....#####.......#########",
	"####...........##########......########....##########....########.....########....########..#########....##########",
	".#####.........###########....##########...###########..##########...##########...########..#########...###########",
	"..#######......####...####...####...####...####...####..###....###..####...####...#####....####...####..####...####",
	"...########....####....####..####....####..####....###..####........####....####..####.....###.....###..###....####",
	".....#######...####....####..###.....####..####....###..#######.....###.....####..####.....###########..###....####",
	"........#####..####....####..###.....####..####....###...########...###.....####..####.....###########.####....####",
	"###......####..####....####..###.....####..####....###.....#######..###.....####..####.....###..........###....####",
	"####.....####..####....####..####....####..####....###.........###..####....####..####.....###..........###....####",
	"####.....####..####....###...####...####...####....###..###....###..####...####...####.....####....##...####...####",
	".###########...###########....##########...####....###..##########...##########...####......##########..###########",
	"..##########...##########......########....####....###...#########....########....####.......########....##########",
	"....######.....####.####........#####......####....###....######.......#####......####........######......#####.###",
	"...............####................................................................................................",
	"...............####................................................................................................",
	"...............####................................................................................................",
	"...............####................................................................................................",
	"...............####................................................................................................",
}

const (
	// Screen size that the mask and the search area use.
	refWidth  = 1920
	refHeight = 1080

	// The search area for the top left corner of the word, in 1920x1080
	// pixels. It allows for a move of the overlay layout.
	searchX0, searchX1 = 40, 300
	searchY0, searchY1 = 860, 1057

	// Percent of mask pixels that may differ from the screen. The real ads
	// differ by 0.2% at most. Other screens differ by 30% or more.
	maxMismatchPercent = 10
)

// mask is a parsed sponsoredMask.
type mask struct {
	w, h   int
	bright []bool
	count  int // number of bright pixels
}

var sponsored = parseMask(sponsoredMask)

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

// screenWarned stops a repeat of the same warning on each reading.
var screenWarned bool

// youTubeAdOnScreen captures the screen and looks for the ad overlay.
// It returns an error only when the connection fails.
func youTubeAdOnScreen(conn *adbConn) (bool, error) {
	data, err := conn.exec("screencap -p")
	if err != nil {
		return false, err
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		if !screenWarned {
			log.Printf("Can't read the screen capture, so YouTube ads stay unmuted: %v", err)
			screenWarned = true
		}
		return false, nil
	}
	screenWarned = false
	return hasSponsoredLabel(img), nil
}

// hasSponsoredLabel reports whether the screen shows the word "Sponsored" of
// the YouTube ad overlay.
func hasSponsoredLabel(img image.Image) bool {
	m := sponsored
	gw := searchX1 - searchX0 + m.w
	gh := searchY1 - searchY0 + m.h
	grid := brightGrid(img, searchX0, searchY0, gw, gh)
	limit := m.w * m.h * maxMismatchPercent / 100

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
func brightGrid(img image.Image, x0, y0, w, h int) []bool {
	b := img.Bounds()
	grid := make([]bool, w*h)
	for y := 0; y < h; y++ {
		iy := b.Min.Y + (y0+y)*b.Dy()/refHeight
		for x := 0; x < w; x++ {
			ix := b.Min.X + (x0+x)*b.Dx()/refWidth
			grid[y*w+x] = isBright(img, ix, iy)
		}
	}
	return grid
}

// isBright reports whether the pixel has a luminance above 128 of 255.
func isBright(img image.Image, x, y int) bool {
	var r, g, b uint32
	if n, ok := img.(*image.NRGBA); ok {
		p := n.Pix[n.PixOffset(x, y):]
		r, g, b = uint32(p[0]), uint32(p[1]), uint32(p[2])
	} else {
		r16, g16, b16, _ := img.At(x, y).RGBA()
		r, g, b = r16>>8, g16>>8, b16>>8
	}
	return (299*r+587*g+114*b)/1000 > 128
}
