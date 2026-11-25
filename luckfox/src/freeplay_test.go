package main

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"testing"
)

// The test data comes from a Google TV Freeplay capture with
// Capture-AdTest.ps1. show2 and show3 of the capture were the same file as
// show1, so they are not in testdata.
func TestFreeplayCountdown(t *testing.T) {
	for _, c := range []struct {
		f  string
		ad bool
	}{
		{"googlefreeplay-ad1", true},    // countdown ring with 1:37 left
		{"googlefreeplay-ad2", true},    // countdown ring with 1:26 left
		{"googlefreeplay-show1", false}, // show, blank screen
		// The Freeplay check must not fire on the screens of another app.
		{"youtube-ad1", false},
		{"youtube-ad2", false},
		{"youtube-show3", false},
		{"youtube-paused1", false},
		{"tubi-ad1", false},
		{"tubi-show1", false},
		{"tubi-paused1", false},
	} {
		img := loadPNG(t, "testdata/"+c.f+".png")
		if got := hasFreeplayCountdown(img); got != c.ad {
			t.Errorf("%s: hasFreeplayCountdown=%v want %v", c.f, got, c.ad)
		}
	}
	// The YouTube and Tubi checks must not fire on the Freeplay screens.
	for _, f := range []string{"ad1", "ad2", "show1"} {
		img := loadPNG(t, "testdata/googlefreeplay-"+f+".png")
		if hasSponsoredLabel(img) || hasTubiAdLabel(img) {
			t.Errorf("googlefreeplay-%s: a YouTube or Tubi check fired", f)
		}
	}
}

// The ring must still match when it moves.
func TestFreeplayCountdownMoved(t *testing.T) {
	src := loadPNG(t, "testdata/googlefreeplay-ad1.png")
	for _, d := range []image.Point{
		{-50, 0}, {45, 0}, {0, -35}, {0, 55}, {-50, 55},
	} {
		if !hasFreeplayCountdown(moved(src, d)) {
			t.Errorf("ring moved by %v: hasFreeplayCountdown=false want true", d)
		}
	}
}

// A 4K screen capture must give the same result as a 1080p one.
func TestFreeplayCountdown4K(t *testing.T) {
	for _, c := range []struct {
		f  string
		ad bool
	}{
		{"ad1", true}, {"show1", false},
	} {
		big := scaled2x(loadPNG(t, "testdata/googlefreeplay-"+c.f+".png"))
		if got := hasFreeplayCountdown(big); got != c.ad {
			t.Errorf("%s at 4K: hasFreeplayCountdown=%v want %v", c.f, got, c.ad)
		}
	}
}

// Near the end of the break, the bright arc is short and the gray track
// fills most of the ring. The check must still match. This test paints the
// whole arc in the gray of the track, the last moment of the break.
func TestFreeplayCountdownEnd(t *testing.T) {
	img := toNRGBA(loadPNG(t, "testdata/googlefreeplay-ad1.png"))
	track := color.NRGBA{50, 50, 50, 255}
	for y := 40; y < 120; y++ {
		for x := 1790; x < 1875; x++ {
			r := math.Hypot(float64(x)-1831.5, float64(y)-79.5)
			if r > 22 && isBright(img, x, y, 128) {
				img.SetNRGBA(x, y, track)
			}
		}
	}
	if !hasFreeplayCountdown(img) {
		t.Error("ring with only the gray track: hasFreeplayCountdown=false want true")
	}
}

// Shapes at the top right that are not the ring must not match.
func TestFreeplayCountdownLookalikes(t *testing.T) {
	blank := loadPNG(t, "testdata/googlefreeplay-show1.png")
	disc := toNRGBA(blank)  // a round profile picture
	block := toNRGBA(blank) // a bright picture over the top right
	white := color.NRGBA{230, 230, 230, 255}
	for y := 0; y < 200; y++ {
		for x := 1700; x < 1920; x++ {
			if math.Hypot(float64(x)-1831.5, float64(y)-79.5) <= 32 {
				disc.SetNRGBA(x, y, white)
			}
			block.SetNRGBA(x, y, color.NRGBA{128, 128, 128, 255})
		}
	}
	for name, img := range map[string]image.Image{"disc": disc, "block": block} {
		if hasFreeplayCountdown(img) {
			t.Errorf("%s: hasFreeplayCountdown=true want false", name)
		}
	}
}

// The audio focus lines follow the format of the Android audio service. The
// capture has no dumpsys audio output, so these lines are not from the box.
const (
	focusLauncher = "  source:android.os.BinderProxy@5d8c1a2 -- pack: com.google.android.apps.tv.launcherx -- client: android.media.AudioManager@548ba23obb@e557e64 -- gain: GAIN -- flags:  -- loss: none -- notified: false -- limbofalse -- uid: 10048 -- attr: AudioAttributes: usage=USAGE_MEDIA content=CONTENT_TYPE_UNKNOWN flags=0x800 tags= bundle=null -- sdk:36\n"
	focusTubi     = "  source:android.os.BinderProxy@1f0e7d3 -- pack: com.tubitv -- client: android.media.AudioManager@9a1c2b4 -- gain: GAIN -- flags:  -- loss: none -- notified: false -- limbofalse -- uid: 10078 -- attr: AudioAttributes: usage=USAGE_MEDIA content=CONTENT_TYPE_MOVIE flags=0x800 tags= bundle=null -- sdk:34\n"
	focusOther    = "  source:android.os.BinderProxy@77aa001 -- pack: com.google.android.apps.tv.launcherx.extra -- client: x -- gain: GAIN -- flags:  -- loss: none -- uid: 10099\n"
)

func TestFreeplayPlaying(t *testing.T) {
	for _, c := range []struct {
		name, out string
		want      bool
	}{
		{"launcher holds the focus", focusLauncher, true},
		{"launcher on top of Tubi", focusTubi + focusLauncher, true},
		{"Tubi on top of the launcher", focusLauncher + focusTubi, false},
		{"another package with the same start", focusOther, false},
		{"empty focus stack", "", false},
	} {
		if got := freeplayPlaying(c.out); got != c.want {
			t.Errorf("%s: freeplayPlaying=%v want %v", c.name, got, c.want)
		}
	}
}

// adApp must flag Freeplay only when the launcher holds the audio focus and
// the screen shows the countdown ring. The other apps do not run on the fake
// TV.
func TestAdAppFreeplay(t *testing.T) {
	detectors := []detector{
		newDetector("com.cbs.ott"), newDetector(plutoPackage),
		newDetector(youTubePackage), newDetector(tubiPackage),
		newDetector(freeplayPackage),
	}
	for _, c := range []struct {
		name, focus, screen, want string
		captures                  int
	}{
		{"ad", focusLauncher, "ad2", freeplayPackage, 1},
		{"show", focusLauncher, "show1", "", 1},
		{"another app holds the focus", focusLauncher + focusTubi, "ad1", "", 0},
		{"no channel plays", "", "ad1", "", 0},
	} {
		screen, err := os.ReadFile("testdata/googlefreeplay-" + c.screen + ".png")
		if err != nil {
			t.Fatal(err)
		}
		captures := 0
		conn := fakeTV(t, func(service string) []byte {
			switch service {
			case "shell:" + freeplayCommand:
				return []byte(c.focus)
			case "shell:" + mediaSessionCommand("com.cbs.ott"),
				"shell:" + mediaSessionCommand(youTubePackage),
				"shell:" + mediaSessionCommand(tubiPackage),
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

func TestNewDetectorFreeplay(t *testing.T) {
	d := newDetector(freeplayPackage)
	if d.command != freeplayCommand || d.onScreen == nil {
		t.Errorf("Freeplay must use the audio focus and the screen: %q", d.command)
	}
}

func BenchmarkFreeplayScreenCheck(b *testing.B) {
	for _, f := range []string{"ad1", "show1"} {
		data, err := os.ReadFile("testdata/googlefreeplay-" + f + ".png")
		if err != nil {
			b.Fatal(err)
		}
		b.Run(f, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				img, err := png.Decode(bytes.NewReader(data))
				if err != nil {
					b.Fatal(err)
				}
				hasFreeplayCountdown(img)
			}
		})
	}
}

// toNRGBA returns an editable copy of the screen.
func toNRGBA(src image.Image) *image.NRGBA {
	dst := image.NewNRGBA(src.Bounds())
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
	return dst
}
