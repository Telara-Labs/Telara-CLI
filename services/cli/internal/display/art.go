package display

// art.go draws what the CLI shows on a terminal while it works: the TELARA
// logo and the larger animations of scan, doctor and update. Each renderer
// is a pure function of a frame number and the state to show, so tests can
// check any frame. termart (shared with the TAP runner) supplies the colors,
// the live redraw and the progress bar. Nothing here is drawn on a pipe.

import (
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/Telara-Labs/TAP-Runtime/discover/termart"
)

// telaraArt is the block-letter logo, in the same letters as TAP's.
var telaraArt = []string{
	"████████╗███████╗██╗      █████╗ ██████╗  █████╗ ",
	"╚══██╔══╝██╔════╝██║     ██╔══██╗██╔══██╗██╔══██╗",
	"   ██║   █████╗  ██║     ███████║██████╔╝███████║",
	"   ██║   ██╔══╝  ██║     ██╔══██║██╔══██╗██╔══██║",
	"   ██║   ███████╗███████╗██║  ██║██║  ██║██║  ██║",
	"   ╚═╝   ╚══════╝╚══════╝╚═╝  ╚═╝╚═╝  ╚═╝╚═╝  ╚═╝",
}

// artWidth is the columns the widest animation needs.
const artWidth = 60

// Final is the frame Live passes for the block it leaves on screen.
const Final = termart.Final

// Animate reports whether w is a terminal wide enough for the animations.
func Animate(w io.Writer) bool { return termart.Terminal(w) && termart.WideFor(w, artWidth) }

// LogoFrame is one frame of the animated TELARA logo.
func LogoFrame(version string, frame int) []string {
	return termart.ArtFrame(telaraArt, version, true, frame)
}

// Live redraws a block of lines in place; see termart.Live.
type Live = termart.Live

// NewLive starts nothing; call Start on the result.
func NewLive(out io.Writer, render func(frame int) []string) *Live {
	return termart.NewLive(out, render)
}

// Small helpers in the shared palette.
func Accent(t string) string { return termart.Accent(true, t) }
func Shine(t string) string  { return termart.Paint(true, termart.ShineSGR, t) }
func Faint(t string) string  { return termart.Dim(true, t) }
func Ok(t string) string     { return termart.Good(true, t) }
func Bad(t string) string    { return termart.Bad(true, t) }
func Warn(t string) string   { return termart.Paint(true, "33", t) }
func Strong(t string) string { return termart.Paint(true, "1", t) }
func Spin(frame int) string  { return Accent(termart.Spin(frame)) }
func Bar(done, total, width int) string {
	return termart.Bar(done, total, width, true)
}

// ─── scan: radar ────────────────────────────────────────────────────────────

const (
	radarCols   = 31
	radarRows   = 15
	radarRadius = 7
	// RadarTurnFrames is how many frames one sweep takes.
	RadarTurnFrames = 28
)

// radarBlips are fixed points on the scope, as fractions of the radius; the
// first n are lit when n things have been found.
var radarBlips = [][2]float64{
	{0.55, -0.35}, {-0.4, -0.6}, {-0.7, 0.2}, {0.25, 0.65}, {0.8, 0.3},
	{-0.15, -0.2}, {-0.5, 0.55}, {0.35, -0.75}, {0.05, 0.35}, {-0.85, -0.25},
}

// RadarFrame is the scope at a frame: the sweep turns while scanning (Final
// stops it) and lit blips stand for what was found.
func RadarFrame(frame, lit int) []string {
	ang := -1.0
	if frame != Final {
		ang = math.Mod(float64(frame)/RadarTurnFrames*2*math.Pi, 2*math.Pi)
	}
	cx, cy := float64(radarCols-1)/2, float64(radarRows-1)/2
	blipAt := map[[2]int]bool{}
	for i, b := range radarBlips {
		if i < lit {
			blipAt[[2]int{int(math.Round(b[0]*radarRadius*2 + cx)), int(math.Round(b[1]*radarRadius + cy))}] = true
		}
	}
	rows := make([]string, 0, radarRows)
	for y := 0; y < radarRows; y++ {
		var b strings.Builder
		for x := 0; x < radarCols; x++ {
			dx, dy := (float64(x)-cx)/2, float64(y)-cy
			d := math.Hypot(dx, dy)
			switch {
			case blipAt[[2]int{x, y}]:
				b.WriteString(Accent("●"))
			case math.Abs(d-radarRadius) < 0.5:
				b.WriteString(Faint("·"))
			case d > radarRadius:
				b.WriteByte(' ')
			case dx == 0 && dy == 0:
				b.WriteString(Accent("+"))
			default:
				behind := 99.0
				if ang >= 0 {
					a := math.Mod(math.Atan2(dy, dx)+2*math.Pi, 2*math.Pi)
					behind = math.Mod(ang-a+2*math.Pi, 2*math.Pi)
				}
				switch {
				case behind < 0.12:
					b.WriteString(Shine("▓"))
				case behind < 0.45:
					b.WriteString(Accent("▒"))
				case behind < 0.9:
					b.WriteString(Faint("░"))
				case math.Abs(d-radarRadius/2) < 0.4:
					b.WriteString(Faint("·"))
				default:
					b.WriteByte(' ')
				}
			}
		}
		rows = append(rows, b.String())
	}
	return rows
}

// SideBySide puts right beside left, line by line, with a gap.
func SideBySide(left, right []string, gap int) []string {
	n := len(left)
	if len(right) > n {
		n = len(right)
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out[i] = l + strings.Repeat(" ", gap) + r
	}
	return out
}

// ─── doctor: heartbeat ──────────────────────────────────────────────────────

const beat = "─────╱╲╱╲──────────"

// Heartbeat is the doctor's pulse line: it scrolls while checks run and is a
// steady line once they are done (Final).
func Heartbeat(frame, width int) string {
	if frame == Final {
		return Ok(strings.Repeat("─", width)) + "  " + Ok("✓")
	}
	runes := []rune(beat)
	line := make([]rune, width)
	for i := range line {
		line[i] = runes[(i+frame)%len(runes)]
	}
	return Accent(string(line)) + "  " + Accent("♥")
}

// ─── update: conveyor ───────────────────────────────────────────────────────

const beltWidth = 26

// ConveyorFrame is the update moving from the release to the installed copy:
// done of total bytes have arrived; installed flips the destination version.
func ConveyorFrame(from, to string, done, total int64, installed bool) []string {
	pos := 0
	if total > 0 {
		pos = int(float64(done) / float64(total) * float64(beltWidth-1))
	}
	if pos > beltWidth-1 {
		pos = beltWidth - 1
	}
	var belt strings.Builder
	switch {
	case installed:
		belt.WriteString(Accent(strings.Repeat("═", beltWidth-1) + "▶"))
	default:
		for i := 0; i < beltWidth; i++ {
			switch {
			case i == pos:
				belt.WriteString(Shine("▣"))
			case i == beltWidth-1:
				belt.WriteString(Faint("▶"))
			default:
				belt.WriteString(Faint("═"))
			}
		}
	}
	// A box holds 7 columns of version; a longer one is cut, never widened,
	// so the boxes and the belt stay lined up.
	box := func(s string) string {
		if r := []rune(s); len(r) > 7 {
			s = string(r[:6]) + "…"
		}
		return fmt.Sprintf(" %-7s ", s)
	}
	dest := Faint(box(from))
	if installed {
		dest = Strong(box(to))
	}
	gap := strings.Repeat(" ", beltWidth)
	return []string{
		"   " + Accent("┌─────────┐") + "  " + gap + "  " + Faint("┌─────────┐"),
		"   " + Accent("│") + " release " + Accent("│") + "  " + belt.String() + "  " + Faint("│") + " telara  " + Faint("│"),
		"   " + Accent("│") + Strong(box(to)) + Accent("│") + "  " + gap + "  " + Faint("│") + dest + Faint("│"),
		"   " + Accent("└─────────┘") + "  " + gap + "  " + Faint("└─────────┘"),
	}
}

// MB formats bytes as megabytes with one decimal.
func MB(n int64) string { return fmt.Sprintf("%.1f", float64(n)/(1<<20)) }
