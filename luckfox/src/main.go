// ad-mute: mutes the TV during streaming ads on the Onn box.
// Port of ad-mute.ps1 that needs no adb install: it speaks the ADB
// protocol over TCP itself, so it runs as one static binary on the Luckfox.
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	cmdCNXN = 0x4e584e43
	cmdAUTH = 0x48545541
	cmdOPEN = 0x4e45504f
	cmdOKAY = 0x59414b4f
	cmdCLSE = 0x45534c43
	cmdWRTE = 0x45545257

	authToken     = 1
	authSignature = 2
	authPublicKey = 3

	adbVersion = 0x01000001
	maxData    = 256 * 1024
)

type message struct {
	cmd, arg0, arg1 uint32
	data            []byte
}

// ---------- keys ----------

func loadOrCreateKey(path string) (*rsa.PrivateKey, error) {
	if b, err := os.ReadFile(path); err == nil {
		blk, _ := pem.Decode(b)
		if blk == nil {
			return nil, fmt.Errorf("%s: not a PEM key", path)
		}
		if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
			return k, nil
		}
		k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
		if err != nil {
			return nil, err
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("%s: not an RSA key", path)
		}
		return rk, nil
	}
	log.Printf("Creating new ADB key at %s", path)
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	return k, os.WriteFile(path, pemBytes, 0600)
}

// androidPubKey encodes the key in the format adbd expects (same as adbkey.pub).
func androidPubKey(k *rsa.PublicKey, name string) []byte {
	const words = 64 // 2048-bit
	n := k.N
	r32 := new(big.Int).Lsh(big.NewInt(1), 32)
	n0inv := new(big.Int).ModInverse(new(big.Int).Mod(n, r32), r32)
	n0inv.Sub(r32, n0inv)
	rr := new(big.Int).Lsh(big.NewInt(1), words*32*2)
	rr.Mod(rr, n)

	buf := make([]byte, 4+4+words*4+words*4+4)
	binary.LittleEndian.PutUint32(buf[0:], words)
	binary.LittleEndian.PutUint32(buf[4:], uint32(n0inv.Uint64()))
	putLE(buf[8:8+words*4], n)
	putLE(buf[8+words*4:8+words*8], rr)
	binary.LittleEndian.PutUint32(buf[8+words*8:], uint32(k.E))
	return []byte(base64.StdEncoding.EncodeToString(buf) + " " + name + "\x00")
}

func putLE(dst []byte, v *big.Int) {
	be := v.FillBytes(make([]byte, len(dst)))
	for i := range be {
		dst[i] = be[len(be)-1-i]
	}
}

// ---------- ADB transport ----------

type adbConn struct {
	c      net.Conn
	nextID uint32
}

func (a *adbConn) send(cmd, arg0, arg1 uint32, data []byte) error {
	var sum uint32
	for _, b := range data {
		sum += uint32(b)
	}
	h := make([]byte, 24)
	binary.LittleEndian.PutUint32(h[0:], cmd)
	binary.LittleEndian.PutUint32(h[4:], arg0)
	binary.LittleEndian.PutUint32(h[8:], arg1)
	binary.LittleEndian.PutUint32(h[12:], uint32(len(data)))
	binary.LittleEndian.PutUint32(h[16:], sum)
	binary.LittleEndian.PutUint32(h[20:], cmd^0xffffffff)
	_, err := a.c.Write(append(h, data...))
	return err
}

func (a *adbConn) recv(timeout time.Duration) (message, error) {
	a.c.SetReadDeadline(time.Now().Add(timeout))
	h := make([]byte, 24)
	if _, err := io.ReadFull(a.c, h); err != nil {
		return message{}, err
	}
	m := message{
		cmd:  binary.LittleEndian.Uint32(h[0:]),
		arg0: binary.LittleEndian.Uint32(h[4:]),
		arg1: binary.LittleEndian.Uint32(h[8:]),
	}
	if binary.LittleEndian.Uint32(h[20:]) != m.cmd^0xffffffff {
		return m, errors.New("bad ADB packet header")
	}
	n := binary.LittleEndian.Uint32(h[12:])
	if n > 16*1024*1024 {
		return m, errors.New("ADB packet too large")
	}
	m.data = make([]byte, n)
	_, err := io.ReadFull(a.c, m.data)
	return m, err
}

func dialADB(target string, key *rsa.PrivateKey) (*adbConn, error) {
	c, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		return nil, err
	}
	a := &adbConn{c: c, nextID: 1}
	if err := a.send(cmdCNXN, adbVersion, maxData, []byte("host::\x00")); err != nil {
		c.Close()
		return nil, err
	}
	sentSig, sentPub := false, false
	for {
		wait := 10 * time.Second
		if sentPub {
			wait = 90 * time.Second // user must tap "Allow" on the TV
		}
		m, err := a.recv(wait)
		if err != nil {
			c.Close()
			if sentPub {
				return nil, fmt.Errorf("not authorised on the TV (did you tap Allow?): %w", err)
			}
			return nil, err
		}
		switch {
		case m.cmd == cmdCNXN:
			return a, nil
		case m.cmd == cmdAUTH && m.arg0 == authToken && !sentSig:
			sig, err := rsa.SignPKCS1v15(nil, key, crypto.SHA1, m.data)
			if err != nil {
				c.Close()
				return nil, err
			}
			sentSig = true
			err = a.send(cmdAUTH, authSignature, 0, sig)
			if err != nil {
				c.Close()
				return nil, err
			}
		case m.cmd == cmdAUTH && m.arg0 == authToken && !sentPub:
			log.Printf("TV doesn't know this device yet. On the TV, tick 'Always allow' and tap Allow.")
			sentPub = true
			if err := a.send(cmdAUTH, authPublicKey, 0, androidPubKey(&key.PublicKey, "ad-mute@luckfox")); err != nil {
				c.Close()
				return nil, err
			}
		default:
			c.Close()
			return nil, fmt.Errorf("unexpected ADB reply %08x during connect", m.cmd)
		}
	}
}

// shell runs a command and returns its output.
func (a *adbConn) shell(cmd string) (string, error) {
	local := a.nextID
	a.nextID++
	if err := a.send(cmdOPEN, local, 0, []byte("shell:"+cmd+"\x00")); err != nil {
		return "", err
	}
	var out strings.Builder
	var remote uint32
	for {
		m, err := a.recv(15 * time.Second)
		if err != nil {
			return "", err
		}
		if m.arg1 != local { // stale packet from an earlier stream
			continue
		}
		switch m.cmd {
		case cmdOKAY:
			remote = m.arg0
		case cmdWRTE:
			remote = m.arg0
			out.Write(m.data)
			if err := a.send(cmdOKAY, local, remote, nil); err != nil {
				return "", err
			}
		case cmdCLSE:
			if m.arg0 == 0 {
				return "", errors.New("TV refused the shell command")
			}
			a.send(cmdCLSE, local, m.arg0, nil)
			return out.String(), nil
		}
	}
}

func (a *adbConn) Close() { a.c.Close() }

// ---------- ad muting ----------

func isAd(line string) bool {
	return strings.Contains(line, "{state=PLAYING") && strings.Contains(line, "speed=0.0,")
}

func main() {
	target := flag.String("target", "", "Onn box address, e.g. 192.168.1.50:5555 (required)")
	pkg := flag.String("package", "com.cbs.ott", "app package to watch (Paramount+ is com.cbs.ott)")
	unmuteAfter := flag.Int("unmute-after", 2, "non-ad readings in a row before unmuting")
	interval := flag.Duration("interval", 500*time.Millisecond, "time between readings")
	keyPath := flag.String("key", "/etc/ad-mute/adbkey", "where to keep this device's ADB key")
	flag.Parse()
	log.SetFlags(log.Ldate | log.Ltime)

	if *target == "" {
		fmt.Fprintln(os.Stderr, "ad-mute: -target is required (Onn box IP:port)")
		os.Exit(2)
	}
	if !strings.Contains(*target, ":") {
		*target += ":5555"
	}
	key, err := loadOrCreateKey(*keyPath)
	if err != nil {
		log.Fatalf("ADB key: %v", err)
	}

	query := fmt.Sprintf("dumpsys media_session | grep -A20 'package=%s' | grep -m1 'state=PlaybackState'", *pkg)
	var conn *adbConn
	muted, showCount := false, 0

	toggle := func() error {
		_, err := conn.shell("input keyevent KEYCODE_VOLUME_MUTE")
		return err
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	log.Printf("Watching %s on %s", *pkg, *target)
	tick := time.NewTicker(*interval)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			if muted && conn != nil && toggle() == nil {
				log.Printf("Stopped while muted: unmuted.")
			}
			return
		case <-tick.C:
		}

		if conn == nil {
			c, err := dialADB(*target, key)
			if err != nil {
				log.Printf("Can't reach Onn box: %v (retrying in 5s)", err)
				time.Sleep(5 * time.Second)
				continue
			}
			conn = c
			log.Printf("Connected to %s", *target)
		}

		line, err := conn.shell(query)
		if err != nil {
			log.Printf("Lost connection: %v", err)
			conn.Close()
			conn = nil
			continue
		}

		if isAd(line) {
			showCount = 0
			if !muted {
				if err := toggle(); err == nil {
					muted = true
					log.Printf("Ad started: muted")
				}
			}
		} else if muted {
			showCount++
			if showCount >= *unmuteAfter {
				if err := toggle(); err == nil {
					muted = false
					showCount = 0
					log.Printf("Show resumed: unmuted")
				}
			}
		}
	}
}
