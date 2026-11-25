package main

import (
	"crypto"
	"crypto/rsa"
	"net"
	"os"
	"strings"
	"testing"
)

func TestPubKeyMatchesRealAdb(t *testing.T) {
	ref := os.Getenv("REFKEY")
	if ref == "" {
		t.Skip("set REFKEY to the path of an adbkey made by real adb")
	}
	k, err := loadOrCreateKey(ref)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(ref + ".pub")
	got := string(androidPubKey(&k.PublicKey, "x"))
	if strings.Fields(string(want))[0] != strings.Fields(got)[0] {
		t.Fatalf("pubkey mismatch")
	}
}

// fake adbd: rejects first signature (unknown key), accepts pubkey, serves shell.
func TestProtocolAgainstFakeTV(t *testing.T) {
	key, _ := loadOrCreateKey(t.TempDir() + "/k")
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	toggles := 0
	go func() {
		c, _ := ln.Accept()
		d := &adbConn{c: c}
		m, _ := d.recv(5e9) // CNXN
		if m.cmd != cmdCNXN {
			panic("no CNXN")
		}
		token := make([]byte, 20)
		d.send(cmdAUTH, authToken, 0, token)
		m, _ = d.recv(5e9) // signature
		if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA1, token, m.data) != nil {
			panic("bad signature")
		}
		d.send(cmdAUTH, authToken, 0, token) // pretend key unknown
		m, _ = d.recv(5e9)
		if m.arg0 != authPublicKey || !strings.HasSuffix(string(m.data), "\x00") {
			panic("no pubkey")
		}
		d.send(cmdCNXN, adbVersion, maxData, []byte("device::\x00"))
		for i := uint32(100); ; i++ {
			m, err := d.recv(5e9)
			if err != nil {
				return
			}
			cmd := string(m.data)
			if strings.Contains(cmd, "KEYCODE_VOLUME_MUTE") {
				toggles++
			}
			d.send(cmdOKAY, i, m.arg0, nil)
			d.send(cmdWRTE, i, m.arg0, []byte("out:"+cmd[:6]))
			d.recv(5e9) // OKAY
			d.send(cmdCLSE, i, m.arg0, nil)
			d.recv(5e9) // CLSE
		}
	}()
	a, err := dialADB(ln.Addr().String(), key)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		out, err := a.shell("input keyevent KEYCODE_VOLUME_MUTE")
		if err != nil || out != "out:shell:" {
			t.Fatalf("shell: %q %v", out, err)
		}
	}
	if out, err := a.exec("screencap -p"); err != nil || string(out) != "out:exec:s" {
		t.Fatalf("exec: %q %v", out, err)
	}
	a.Close()
}

// The Paramount+ media dumps are not in the repository. Copy them to the
// repository root to run this test.
func TestSpeedZeroAd(t *testing.T) {
	for _, c := range []struct {
		f  string
		ad bool
	}{
		{"show1", false}, {"ad1", true}, {"ad2", true}, {"show2", false},
	} {
		b, err := os.ReadFile("../../paramount-" + c.f + "-media.txt")
		if err != nil {
			t.Skipf("no Paramount+ capture: %v", err)
		}
		line := playbackStates(string(b))["com.cbs.ott"]
		if speedZeroAd(line) != c.ad {
			t.Errorf("%s: speedZeroAd=%v want %v", c.f, !c.ad, c.ad)
		}
	}
}

func TestPlaybackStates(t *testing.T) {
	dump := `  Sessions Stack - have 3 sessions:
    starboard com.google.android.youtube.tv/starboard (userId=0)
      package=com.google.android.youtube.tv
      active=true
      state=PlaybackState {state=PLAYING(3), position=262, buffered position=0, speed=1.0, updated=317541664, actions=382, custom actions=[], active item id=-1, error=null}
      metadata: size=5, description=Thanksgiving Through American History, HISTORY, null
    Netflix media session com.netflix.ninja/Netflix media session (userId=0)
      package=com.netflix.ninja
      state=null
    Paramount com.cbs.ott/Paramount (userId=0)
      package=com.cbs.ott
      state=PlaybackState {state=PLAYING(3), position=5000, buffered position=0, speed=0.0, updated=1, actions=0, custom actions=[], active item id=-1, error=null}
`
	got := playbackStates(dump)
	if !isPlaying(got[youTubePackage]) || speedZeroAd(got[youTubePackage]) {
		t.Errorf("YouTube line: %q", got[youTubePackage])
	}
	if line, ok := got["com.netflix.ninja"]; ok {
		t.Errorf("Netflix has no playback state, got %q", line)
	}
	if !speedZeroAd(got["com.cbs.ott"]) {
		t.Errorf("Paramount+ line: %q", got["com.cbs.ott"])
	}
}

func TestSplitPackages(t *testing.T) {
	got := strings.Join(splitPackages(" com.cbs.ott, ,com.google.android.youtube.tv,"), "|")
	if got != "com.cbs.ott|com.google.android.youtube.tv" {
		t.Errorf("splitPackages: %q", got)
	}
}
