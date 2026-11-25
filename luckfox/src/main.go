// ad-mute: mutes the TV during streaming ads on the Onn box.
// Port of ad-mute.ps1 that needs no adb install: it speaks the ADB
// protocol over TCP itself, so it runs as one static binary on the Luckfox.
// See youtube.go for the YouTube check.
package main

import (
	"bytes"
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
	"image"
	"image/png"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
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
	out, err := a.run("shell:" + cmd)
	return string(out), err
}

// exec runs a command and returns its output unchanged, for binary output
// such as a screen capture.
func (a *adbConn) exec(cmd string) ([]byte, error) {
	return a.run("exec:" + cmd)
}

// run opens an ADB service and returns all of its output.
func (a *adbConn) run(service string) ([]byte, error) {
	local := a.nextID
	a.nextID++
	if err := a.send(cmdOPEN, local, 0, []byte(service+"\x00")); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	var remote uint32
	for {
		m, err := a.recv(15 * time.Second)
		if err != nil {
			return nil, err
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
				return nil, err
			}
		case cmdCLSE:
			if m.arg0 == 0 {
				return nil, errors.New("TV refused the shell command")
			}
			a.send(cmdCLSE, local, m.arg0, nil)
			return out.Bytes(), nil
		}
	}
}

func (a *adbConn) Close() { a.c.Close() }

// ---------- ad detection ----------

// A detector reads the ad state of one app from the output of one shell command.
// If onScreen is set, the detector also captures the screen when isAd is true,
// and only onScreen decides. isAd then only rules out times with no ad, such as
// a pause.
type detector struct {
	pkg      string
	command  string
	isAd     func(out string) bool
	onScreen func(img image.Image) bool
}

func newDetector(pkg string) detector {
	switch pkg {
	case plutoPackage:
		return detector{pkg: pkg, command: plutoCommand, isAd: plutoAdPlaying}
	case youTubePackage:
		return detector{pkg: pkg, command: mediaSessionCommand(pkg), isAd: isPlaying, onScreen: hasSponsoredLabel}
	}
	return detector{pkg: pkg, command: mediaSessionCommand(pkg), isAd: isAd}
}

// mediaSessionCommand prints the PlaybackState line of the media session of the app.
func mediaSessionCommand(pkg string) string {
	return fmt.Sprintf("dumpsys media_session | grep -A20 'package=%s' | grep -m1 'state=PlaybackState'", pkg)
}

// isPlaying reads a PlaybackState line. It is true while the app plays.
func isPlaying(line string) bool {
	return strings.Contains(line, "{state=PLAYING")
}

// isAd reads a PlaybackState line. During an ad, Paramount+ reports state
// PLAYING with playback speed 0.0.
func isAd(line string) bool {
	return isPlaying(line) && strings.Contains(line, "speed=0.0,")
}

// Pluto TV keeps its media session at PLAYING, speed 1.0, during an ad, so the
// session tells nothing. The app log marks each ad break instead:
//
//	Emitting an analytic event: slotStart        the ad break starts
//	consumeID3: ID3Tag(creativeId=...)           an ad plays (every 3 to 8 seconds)
//	Emitting an analytic event: OnAdPodComplete  the ad break ends
//	Emitting an analytic event: slotEnd          the ad break ends
//
// The log tag of these lines is an obfuscated class name, so only the text is matched.
const (
	plutoPackage = "tv.pluto.android"

	plutoBreakStart = "Emitting an analytic event: slotStart"
	plutoAdTag      = "consumeID3: ID3Tag("
	plutoPodDone    = "Emitting an analytic event: OnAdPodComplete"
	plutoBreakEnd   = "Emitting an analytic event: slotEnd"

	// plutoAdHold is how long one ad line keeps the ad state when no newer line
	// comes. It guards against a missed end line, for example when the app
	// closes mid-ad. The longest gap between ad lines in the capture was 7.6 s.
	plutoAdHold = 35 * time.Second
)

// plutoCommand prints the device time as "now=<epoch seconds>" and then the
// Pluto TV ad lines of the last 40 seconds, each led by its epoch time.
// It prints nothing when the app is not running.
const plutoCommand = "p=$(pidof -s " + plutoPackage + ") || exit 0; n=$(date +%s); echo now=$n; " +
	"logcat -d -b main -v epoch --pid=$p -t $((n-40)).000 " +
	"-e 'consumeID3: ID3Tag|Emitting an analytic event: (slotStart|slotEnd|OnAdPodComplete)'"

// plutoAdPlaying reads the output of plutoCommand. The last ad line decides:
// an end line means the show plays, and a start line or an ID3 line means an
// ad plays, unless that line is older than plutoAdHold.
func plutoAdPlaying(out string) bool {
	now, lastAd := -1.0, 0.0
	inAd := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "now="); ok {
			if t, err := strconv.ParseFloat(v, 64); err == nil {
				now = t
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		t, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			continue // a header such as "--------- beginning of main"
		}
		switch {
		case strings.Contains(line, plutoBreakEnd), strings.Contains(line, plutoPodDone):
			inAd = false
		case strings.Contains(line, plutoBreakStart), strings.Contains(line, plutoAdTag):
			inAd, lastAd = true, t
		}
	}
	return now > 0 && inAd && now-lastAd <= plutoAdHold.Seconds()
}

// adApp returns the package of the first app that plays an ad, or "" if none does.
func adApp(conn *adbConn, detectors []detector) (string, error) {
	for _, d := range detectors {
		out, err := conn.shell(d.command)
		if err != nil {
			return "", err
		}
		if !d.isAd(out) {
			continue
		}
		if d.onScreen != nil {
			ad, err := screenShows(conn, d.onScreen)
			if err != nil {
				return "", err
			}
			if !ad {
				continue
			}
		}
		return d.pkg, nil
	}
	return "", nil
}

// screenWarned stops a repeat of the same warning at each check.
var screenWarned bool

// screenShows captures the screen and passes it to onScreen. It returns an
// error only when the connection fails. A capture that does not decode counts
// as no ad.
func screenShows(conn *adbConn, onScreen func(img image.Image) bool) (bool, error) {
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
	return onScreen(img), nil
}

// ---------- ad muting ----------

func main() {
	target := flag.String("target", "", "Onn box address, e.g. 192.168.1.50:5555 (required)")
	pkgs := flag.String("package", "com.cbs.ott,"+plutoPackage+","+youTubePackage,
		"comma-separated app packages to watch (Paramount+ is com.cbs.ott, Pluto TV is "+plutoPackage+
			", YouTube is "+youTubePackage+")")
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
	var detectors []detector
	var names []string
	for _, p := range strings.Split(*pkgs, ",") {
		if p = strings.TrimSpace(p); p != "" {
			detectors = append(detectors, newDetector(p))
			names = append(names, p)
		}
	}
	if len(detectors) == 0 {
		fmt.Fprintln(os.Stderr, "ad-mute: -package needs at least one app package")
		os.Exit(2)
	}
	key, err := loadOrCreateKey(*keyPath)
	if err != nil {
		log.Fatalf("ADB key: %v", err)
	}

	var conn *adbConn
	muted, showCount := false, 0

	toggle := func() error {
		_, err := conn.shell("input keyevent KEYCODE_VOLUME_MUTE")
		return err
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	log.Printf("Watching %s on %s", strings.Join(names, ", "), *target)
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

		app, err := adApp(conn, detectors)
		if err != nil {
			log.Printf("Lost connection: %v", err)
			conn.Close()
			conn = nil
			continue
		}

		if app != "" {
			showCount = 0
			if !muted {
				if err := toggle(); err == nil {
					muted = true
					log.Printf("Ad started in %s: muted", app)
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
