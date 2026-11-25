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
	a.Close()
}

func TestIsAd(t *testing.T) {
	for _, c := range []struct{ f string; ad bool }{
		{"show1", false}, {"ad1", true}, {"ad2", true}, {"show2", false},
	} {
		b, _ := os.ReadFile("../../paramount-" + c.f + "-media.txt")
		s := string(b)
		i := strings.Index(s, "package=com.cbs.ott")
		j := strings.Index(s[i:], "state=PlaybackState")
		line := s[i+j : i+j+strings.Index(s[i+j:], "\n")]
		if isAd(line) != c.ad {
			t.Errorf("%s: isAd=%v want %v", c.f, !c.ad, c.ad)
		}
	}
}
