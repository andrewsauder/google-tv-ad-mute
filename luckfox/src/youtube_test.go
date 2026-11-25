package main

import (
	"bytes"
	"image"
	"image/png"
	"net"
	"os"
	"strings"
	"testing"
)

// The test data comes from a YouTube capture with Capture-AdTest.ps1.
// show3 was saved as ad3 by mistake: it shows the player controls during the
// show, not an ad.
func TestSponsoredLabel(t *testing.T) {
	for _, c := range []struct {
		f  string
		ad bool
	}{
		{"ad1", true},      // ad overlay with a skip countdown
		{"ad2", true},      // ad overlay with a QR code and a skip button
		{"show1", false},   // show, blank screen
		{"show3", false},   // show, player controls
		{"paused1", false}, // show paused, player controls
	} {
		img := loadPNG(t, "testdata/youtube-"+c.f+".png")
		if got := hasSponsoredLabel(img); got != c.ad {
			t.Errorf("%s: hasSponsoredLabel=%v want %v", c.f, got, c.ad)
		}
	}
}

// A 4K screen capture must give the same result as a 1080p one.
func TestSponsoredLabel4K(t *testing.T) {
	for _, c := range []struct {
		f  string
		ad bool
	}{
		{"ad1", true}, {"show3", false},
	} {
		src := loadPNG(t, "testdata/youtube-"+c.f+".png")
		b := src.Bounds()
		big := image.NewNRGBA(image.Rect(0, 0, b.Dx()*2, b.Dy()*2))
		for y := 0; y < b.Dy()*2; y++ {
			for x := 0; x < b.Dx()*2; x++ {
				big.Set(x, y, src.At(b.Min.X+x/2, b.Min.Y+y/2))
			}
		}
		if got := hasSponsoredLabel(big); got != c.ad {
			t.Errorf("%s at 4K: hasSponsoredLabel=%v want %v", c.f, got, c.ad)
		}
	}
}

// adApp must flag YouTube only when it plays and the screen shows the ad
// overlay. The fake TV serves the media session lines and the screen capture.
// Paramount+ and Pluto TV do not run on it.
func TestAdAppYouTube(t *testing.T) {
	const playing = "state=PlaybackState {state=PLAYING(3), position=262, buffered position=0, speed=1.0, updated=1, actions=382, custom actions=[], active item id=-1, error=null}\n"
	const paused = "state=PlaybackState {state=PAUSED(2), position=262, buffered position=0, speed=0.0, updated=1, actions=382, custom actions=[], active item id=-1, error=null}\n"
	detectors := []detector{newDetector("com.cbs.ott"), newDetector(plutoPackage), newDetector(youTubePackage)}
	for _, c := range []struct {
		name, line, screen, want string
		captures                 int
	}{
		{"ad", playing, "ad1", youTubePackage, 1},
		{"show", playing, "show1", "", 1},
		{"show with controls", playing, "show3", "", 1},
		{"paused", paused, "ad1", "", 0},
		{"not running", "", "ad1", "", 0},
	} {
		screen, err := os.ReadFile("testdata/youtube-" + c.screen + ".png")
		if err != nil {
			t.Fatal(err)
		}
		captures := 0
		conn := fakeTV(t, func(service string) []byte {
			switch service {
			case "shell:" + mediaSessionCommand(youTubePackage):
				return []byte(c.line)
			case "shell:" + mediaSessionCommand("com.cbs.ott"), "shell:" + plutoCommand:
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

// A screen capture that does not decode counts as no ad, and the connection stays.
func TestAdAppYouTubeBadCapture(t *testing.T) {
	conn := fakeTV(t, func(service string) []byte {
		if service == "exec:screencap -p" {
			return []byte("not a PNG")
		}
		return []byte("state=PlaybackState {state=PLAYING(3), position=262, speed=1.0, actions=382}\n")
	})
	defer conn.Close()
	got, err := adApp(conn, []detector{newDetector(youTubePackage)})
	if err != nil || got != "" {
		t.Errorf("adApp=%q, %v, want no ad and no error", got, err)
	}
}

func TestNewDetectorYouTube(t *testing.T) {
	d := newDetector(youTubePackage)
	if !strings.Contains(d.command, "dumpsys media_session") || d.onScreen == nil {
		t.Errorf("YouTube must use the media session and the screen: %q", d.command)
	}
	if newDetector("com.cbs.ott").onScreen != nil || newDetector(plutoPackage).onScreen != nil {
		t.Error("only YouTube, Tubi and Freeplay need a screen capture")
	}
}

// fakeTV starts a fake adbd that answers each service with reply, and
// returns a connection to it.
func fakeTV(t *testing.T, reply func(service string) []byte) *adbConn {
	t.Helper()
	key, err := loadOrCreateKey(t.TempDir() + "/k")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		d := &adbConn{c: c}
		defer c.Close()
		d.recv(5e9) // CNXN
		d.send(cmdAUTH, authToken, 0, make([]byte, 20))
		d.recv(5e9) // signature
		d.send(cmdCNXN, adbVersion, maxData, []byte("device::\x00"))
		for i := uint32(100); ; i++ {
			m, err := d.recv(5e9) // OPEN
			if err != nil {
				return
			}
			out := reply(strings.TrimSuffix(string(m.data), "\x00"))
			d.send(cmdOKAY, i, m.arg0, nil)
			for len(out) > 0 {
				n := min(len(out), maxData)
				d.send(cmdWRTE, i, m.arg0, out[:n])
				d.recv(5e9) // OKAY
				out = out[n:]
			}
			d.send(cmdCLSE, i, m.arg0, nil)
			d.recv(5e9) // CLSE
		}
	}()
	conn, err := dialADB(ln.Addr().String(), key)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func BenchmarkYouTubeScreenCheck(b *testing.B) {
	data, err := os.ReadFile("testdata/youtube-ad1.png")
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < b.N; i++ {
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			b.Fatal(err)
		}
		hasSponsoredLabel(img)
	}
}

func loadPNG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}
