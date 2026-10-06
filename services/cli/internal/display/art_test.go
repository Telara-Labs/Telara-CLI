package display

import (
	"regexp"
	"strings"
	"testing"
)

var sgr = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func plain(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = sgr.ReplaceAllString(l, "")
	}
	return out
}

func width(s string) int { return len([]rune(s)) }

// Every radar frame is the same size, so redrawing in place leaves nothing
// behind, and the number of lit blips is what was found (up to the blips the
// scope has).
func TestRadarFramesKeepTheirShape(t *testing.T) {
	for _, lit := range []int{0, 3, 99} {
		for f := -1; f < 2*RadarTurnFrames; f++ {
			rows := plain(RadarFrame(f, lit))
			if len(rows) != radarRows {
				t.Fatalf("frame %d: %d rows", f, len(rows))
			}
			for _, r := range rows {
				if width(r) != radarCols {
					t.Fatalf("frame %d: row %q is %d wide", f, r, width(r))
				}
			}
			want := lit
			if want > len(radarBlips) {
				want = len(radarBlips)
			}
			if got := strings.Count(strings.Join(rows, ""), "●"); got != want {
				t.Fatalf("frame %d lit %d: %d blips", f, lit, got)
			}
		}
	}
	if strings.Contains(strings.Join(plain(RadarFrame(Final, 0)), ""), "▓") {
		t.Fatal("the final frame still shows the sweep")
	}
}

func TestHeartbeatScrollsThenSettles(t *testing.T) {
	a, b := Heartbeat(0, 52), Heartbeat(1, 52)
	if a == b {
		t.Fatal("heartbeat does not move between frames")
	}
	if got := plain([]string{Heartbeat(Final, 52)})[0]; strings.ContainsAny(got, "╱╲") {
		t.Fatalf("final heartbeat still beats: %q", got)
	}
}

// The release and installed boxes line up with their tops whatever the
// download has reached and however long the version is.
func TestConveyorBoxesLineUp(t *testing.T) {
	for _, c := range []struct {
		from, to    string
		done, total int64
		installed   bool
	}{
		{"0.1.46", "0.1.47", 0, 100, false},
		{"0.1.46", "0.1.47", 50, 100, false},
		{"0.1.46", "0.1.47", 100, 100, true},
		{"dev", "0.1.100-rc.1", 7, -1, false},
	} {
		rows := plain(ConveyorFrame(c.from, c.to, c.done, c.total, c.installed))
		w := width(rows[0])
		for _, r := range rows {
			if width(r) != w {
				t.Fatalf("%+v: rows differ in width:\n%s", c, strings.Join(rows, "\n"))
			}
		}
		if strings.Count(rows[1], "▣")+strings.Count(rows[1], "▶") == 0 {
			t.Fatalf("%+v: no package or arrow on the belt: %q", c, rows[1])
		}
	}
	if !strings.Contains(plain(ConveyorFrame("0.1.46", "0.1.47", 1, 1, true))[2], "0.1.47") {
		t.Fatal("installed copy did not flip to the new version")
	}
}

func TestLogoFrameHasTelaraLetters(t *testing.T) {
	got := strings.Join(plain(LogoFrame("0.1.47", Final)), "\n")
	if !strings.Contains(got, "███████╗██╗") || !strings.Contains(got, "v0.1.47") {
		t.Fatalf("logo:\n%s", got)
	}
}
