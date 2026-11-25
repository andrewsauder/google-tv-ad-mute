package main

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"
	"os"
	"strings"
	"testing"
)

// The test data comes from a Tubi capture with Capture-AdTest.ps1. paused2 of
// the capture was the same file as paused1, so it is not in testdata.
func TestTubiAdLabel(t *testing.T) {
	for _, c := range []struct {
		f  string
		ad bool
	}{
		{"tubi-ad1", true},      // "Ad 1 of 3. This ad will end in 00:05"
		{"tubi-ad2", true},      // "Ad 2 of 3. ...": the words start 5 pixels further right
		{"tubi-ad3", true},      // "Ad 3 of 3. ..."
		{"tubi-show1", false},   // show, rating at the top left
		{"tubi-show2", false},   // show, blank screen
		{"tubi-paused1", false}, // show paused, still ad with "Press any button to close the ad"
		// The Tubi check must not fire on the screens of another app.
		{"youtube-ad1", false},
		{"youtube-ad2", false},
		{"youtube-show3", false},
		{"youtube-paused1", false},
	} {
		img := loadPNG(t, "testdata/"+c.f+".png")
		if got := hasTubiAdLabel(img); got != c.ad {
			t.Errorf("%s: hasTubiAdLabel=%v want %v", c.f, got, c.ad)
		}
	}
	// The YouTube check must not fire on the Tubi screens.
	for _, f := range []string{"ad1", "ad2", "ad3", "show1", "paused1"} {
		if hasSponsoredLabel(loadPNG(t, "testdata/tubi-"+f+".png")) {
			t.Errorf("tubi-%s: hasSponsoredLabel=true want false", f)
		}
	}
}

// The label must still match when it moves, for example when a longer label
// such as "Ad 10 of 12." pushes the words to the right.
func TestTubiAdLabelMoved(t *testing.T) {
	src := loadPNG(t, "testdata/tubi-ad1.png")
	for _, d := range []image.Point{
		{30, 0},   // two more digits
		{120, 0},  // far right
		{-40, 0},  // left
		{0, 60},   // down
		{0, -30},  // up
		{120, 60}, // right and down
	} {
		if !hasTubiAdLabel(moved(src, d)) {
			t.Errorf("label moved by %v: hasTubiAdLabel=false want true", d)
		}
	}
}

// A 4K screen capture must give the same result as a 1080p one.
func TestTubiAdLabel4K(t *testing.T) {
	for _, c := range []struct {
		f  string
		ad bool
	}{
		{"ad1", true}, {"show1", false},
	} {
		big := scaled2x(loadPNG(t, "testdata/tubi-"+c.f+".png"))
		if got := hasTubiAdLabel(big); got != c.ad {
			t.Errorf("%s at 4K: hasTubiAdLabel=%v want %v", c.f, got, c.ad)
		}
	}
}

// adApp must flag Tubi only when it plays and the screen shows the ad label.
// The media session lines come from the capture. The other apps do not run on
// the fake TV.
func TestAdAppTubi(t *testing.T) {
	const playing = "      state=PlaybackState {state=PLAYING(3), position=0, buffered position=0, speed=1.0, updated=319880305, actions=847, custom actions=[], active item id=595036, error=null}\n"
	const paused = "      state=PlaybackState {state=PAUSED(2), position=700806, buffered position=0, speed=1.0, updated=319969669, actions=847, custom actions=[], active item id=595036, error=null}\n"
	detectors := []detector{
		newDetector("com.cbs.ott"), newDetector(plutoPackage),
		newDetector(youTubePackage), newDetector(tubiPackage),
	}
	for _, c := range []struct {
		name, line, screen, want string
		captures                 int
	}{
		{"ad", playing, "ad2", tubiPackage, 1},
		{"show", playing, "show1", "", 1},
		{"paused with a still ad", paused, "paused1", "", 0},
		{"not running", "", "ad1", "", 0},
	} {
		screen, err := os.ReadFile("testdata/tubi-" + c.screen + ".png")
		if err != nil {
			t.Fatal(err)
		}
		captures := 0
		conn := fakeTV(t, func(service string) []byte {
			switch service {
			case "shell:" + mediaSessionCommand(tubiPackage):
				return []byte(c.line)
			case "shell:" + mediaSessionCommand("com.cbs.ott"),
				"shell:" + mediaSessionCommand(youTubePackage),
				"shell:" + plutoCommand:
				return nil
			case "exec:screencap -p":
				captures++
				return screen
			}
			t.Errorf("unexpected service %q", service)
			return nil
		})
		got, err := adApp(conn, detectors)
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want || captures != c.captures {
			t.Errorf("%s: adApp=%q with %d captures, want %q with %d",
				c.name, got, captures, c.want, c.captures)
		}
	}
}

func TestNewDetectorTubi(t *testing.T) {
	d := newDetector(tubiPackage)
	if !strings.Contains(d.command, "package="+tubiPackage) || d.onScreen == nil {
		t.Errorf("Tubi must use the media session and the screen: %q", d.command)
	}
}

func BenchmarkTubiScreenCheck(b *testing.B) {
	for _, f := range []string{"ad1", "show1"} {
		data, err := os.ReadFile("testdata/tubi-" + f + ".png")
		if err != nil {
			b.Fatal(err)
		}
		b.Run(f, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				img, err := png.Decode(bytes.NewReader(data))
				if err != nil {
					b.Fatal(err)
				}
				hasTubiAdLabel(img)
			}
		})
	}
}

// moved returns a copy of the screen, moved by d on a black background.
func moved(src image.Image, d image.Point) image.Image {
	b := src.Bounds()
	dst := image.NewNRGBA(b)
	draw.Draw(dst, b, image.Black, image.Point{}, draw.Src)
	draw.Draw(dst, b.Add(d), src, b.Min, draw.Src)
	return dst
}

// scaled2x returns a copy of the screen at twice the size.
func scaled2x(src image.Image) image.Image {
	b := src.Bounds()
	big := image.NewNRGBA(image.Rect(0, 0, b.Dx()*2, b.Dy()*2))
	for y := 0; y < b.Dy()*2; y++ {
		for x := 0; x < b.Dx()*2; x++ {
			big.Set(x, y, src.At(b.Min.X+x/2, b.Min.Y+y/2))
		}
	}
	return big
}
