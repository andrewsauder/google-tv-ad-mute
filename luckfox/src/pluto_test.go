package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// testdata/plutotv-ad-lines.txt holds the Pluto TV ad lines of the capture
// plutotv-20261124-2029, plus a few progress lines that are not ad lines.
// The capture used the "logcat -v time" format. The device command uses
// "-v epoch", so the tests turn each time into epoch seconds.

type logLine struct {
	at   time.Time
	text string
}

func loadPlutoLog(t *testing.T) []logLine {
	t.Helper()
	f, err := os.Open("testdata/plutotv-ad-lines.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []logLine
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		at, err := time.Parse("2006-01-02 15:04:05.000", "2026-"+line[:18])
		if err != nil {
			t.Fatalf("bad line %q: %v", line, err)
		}
		lines = append(lines, logLine{at, strings.TrimSpace(line[18:])})
	}
	return lines
}

// plutoRegex is the -e pattern of plutoCommand, which logcat applies on the device.
func plutoRegex(t *testing.T) *regexp.Regexp {
	t.Helper()
	i := strings.Index(plutoCommand, "-e '")
	j := strings.LastIndex(plutoCommand, "'")
	if i < 0 || j <= i+4 {
		t.Fatal("plutoCommand has no -e pattern")
	}
	return regexp.MustCompile(plutoCommand[i+4 : j])
}

// plutoOutput builds the output of plutoCommand at the time now.
func plutoOutput(lines []logLine, re *regexp.Regexp, now time.Time) string {
	var b strings.Builder
	n := now.Unix()
	fmt.Fprintf(&b, "now=%d\n", n)
	from := time.Unix(n-40, 0)
	for _, l := range lines {
		if l.at.Before(from) || l.at.After(now) || !re.MatchString(l.text) {
			continue
		}
		fmt.Fprintf(&b, "%19d.%03d %s\n", l.at.Unix(), l.at.Nanosecond()/1e6, l.text)
	}
	return b.String()
}

func at(clock string) time.Time {
	t, err := time.Parse("2006-01-02 15:04:05.000", "2026-11-24 "+clock)
	if err != nil {
		panic(err)
	}
	return t
}

// The snapshot times and kinds come from times.txt of the capture.
func TestPlutoSnapshots(t *testing.T) {
	lines := loadPlutoLog(t)
	re := plutoRegex(t)
	for _, c := range []struct {
		name, clock string
		ad          bool
	}{
		{"show1", "20:29:16.000", false},
		{"ad1", "20:29:50.000", true},
		{"ad2", "20:30:19.000", true},
		{"ad3", "20:31:00.000", true},
		{"ad4", "20:31:28.000", true},
		{"show2", "20:32:22.000", false},
		{"show3", "20:32:30.000", false},
		{"paused1", "20:32:42.000", false},
		{"show4", "20:33:05.000", false},
		{"ad5", "20:33:29.000", true},
		{"ad6", "20:33:57.000", true},
	} {
		if got := plutoAdPlaying(plutoOutput(lines, re, at(c.clock))); got != c.ad {
			t.Errorf("%s at %s: ad=%v, want %v", c.name, c.clock, got, c.ad)
		}
	}
}

// Readings every 500 ms over the whole capture must change state only at the
// two ad break starts and at the one ad break end. A change between two ads
// of one break would unmute the TV for a moment.
func TestPlutoTimeline(t *testing.T) {
	lines := loadPlutoLog(t)
	re := plutoRegex(t)
	var got []string
	ad := false
	for now := at("20:29:09.000"); !now.After(at("20:34:27.000")); now = now.Add(500 * time.Millisecond) {
		if v := plutoAdPlaying(plutoOutput(lines, re, now)); v != ad {
			ad = v
			got = append(got, fmt.Sprintf("%s ad=%v", now.Format("15:04:05.0"), v))
		}
	}
	want := []string{
		"20:29:50.0 ad=true",  // slotStart at 20:29:49.675
		"20:32:19.5 ad=false", // slotEnd at 20:32:19.378
		"20:33:27.0 ad=true",  // slotStart at 20:33:26.941
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("state changes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestPlutoRegexSelectsAdLines(t *testing.T) {
	re := plutoRegex(t)
	for line, want := range map[string]bool{
		"consumeID3: ID3Tag(creativeId=x, currentIndexSec=0, maximumIndexSec=15, typeFlags=[QUARTILE_FIRST])": true,
		"Emitting an analytic event: slotStart":                            true,
		"Emitting an analytic event: slotEnd":                              true,
		"Emitting an analytic event: OnAdPodComplete":                      true,
		"Emitting an analytic event: impression":                           false,
		"consumeProgress: VodProgress(positionMillis=625646, contentId=x)": false,
	} {
		if re.MatchString(line) != want {
			t.Errorf("%q: match=%v, want %v", line, !want, want)
		}
	}
}

func TestPlutoAdPlayingEdges(t *testing.T) {
	tag := "V df8     : consumeID3: ID3Tag(creativeId=x)"
	for _, c := range []struct {
		name, out string
		ad        bool
	}{
		{"app not running", "", false},
		{"no ad lines", "now=1000\n", false},
		{"no device time", "        990.000 " + tag + "\n", false},
		{"recent ad line", "now=1024\n--------- beginning of main\n        990.000 " + tag + "\n", true},
		{"ad line past the hold", "now=1026\n        990.000 " + tag + "\n", false},
		{"break end after the ad line", "now=1000\n        990.000 " + tag + "\n        991.000 D df8     : Emitting an analytic event: slotEnd\n", false},
		{"new break after an end", "now=1000\n        990.000 D df8     : Emitting an analytic event: slotEnd\n        995.000 D df8     : Emitting an analytic event: slotStart\n", true},
	} {
		if got := plutoAdPlaying(c.out); got != c.ad {
			t.Errorf("%s: ad=%v, want %v", c.name, got, c.ad)
		}
	}
}

func TestNewDetector(t *testing.T) {
	if d := newDetector("tv.pluto.android"); d.command != plutoCommand {
		t.Errorf("Pluto TV does not use the log command: %q", d.command)
	}
	if d := newDetector("com.cbs.ott"); !strings.Contains(d.command, "dumpsys media_session") {
		t.Errorf("Paramount+ does not use the media session: %q", d.command)
	}
}
