package cmd

// boards.go holds what login, install, scan, doctor and update draw on a
// terminal while they work. Each board keeps the state its command reports
// and renders it a frame at a time through display.Live; the command keeps
// printing its plain output when the stream is not a terminal, and every
// board method is a no-op on a nil board.

import (
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/display"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/version"
)

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s + strings.Repeat(" ", n-len(r))
}

// ─── login ──────────────────────────────────────────────────────────────────

type loginBoard struct {
	live    *display.Live
	url     string
	code    string
	expires time.Time

	mu    sync.Mutex
	state int // 0 waiting, 1 approved, 2 failed
}

func newLoginBoard(out io.Writer, verifyURL, code string, expiresIn int) *loginBoard {
	if expiresIn <= 0 {
		expiresIn = 600
	}
	b := &loginBoard{url: verifyURL, code: code, expires: time.Now().Add(time.Duration(expiresIn) * time.Second)}
	b.live = display.NewLive(out, b.render)
	b.live.Start()
	return b
}

func (b *loginBoard) finish(ok bool) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.state = 2
	if ok {
		b.state = 1
	}
	b.mu.Unlock()
	b.live.Stop()
}

const loginLink = 19

func (b *loginBoard) render(frame int) []string {
	b.mu.Lock()
	state := b.state
	b.mu.Unlock()
	host := b.url
	if u, err := url.Parse(b.url); err == nil && u.Host != "" {
		host = u.Host
	}
	var link string
	switch state {
	case 1:
		link = display.Accent(strings.Repeat("━", loginLink-1) + "▶")
	case 2:
		link = display.Bad(strings.Repeat("━", loginLink/2) + "╳" + strings.Repeat(" ", loginLink-loginLink/2-1))
	default:
		p := frame % loginLink
		if frame < 0 {
			p = 0
		}
		var l strings.Builder
		for i := 0; i < loginLink; i++ {
			switch {
			case i == p:
				l.WriteString(display.Shine("●"))
			case i%3 == 0:
				l.WriteString(display.Faint("·"))
			default:
				l.WriteByte(' ')
			}
		}
		link = l.String()
	}
	browser := display.Accent(clip("[ "+b.code+" ]", 16))
	if state == 1 {
		browser = display.Ok(clip("✓ approved", 16))
	}
	gap := strings.Repeat(" ", loginLink+4)
	lines := display.LogoFrame(version.Version, frame)
	lines = append(lines,
		"   "+display.Faint("╭──────────────╮")+gap+display.Faint("╭──────────────────╮"),
		"   "+display.Faint("│")+" $ telara     "+display.Faint("│")+"  "+link+"  "+display.Faint("│")+" "+clip(host, 17)+display.Faint("│"),
		"   "+display.Faint("│")+"   login      "+display.Faint("│")+gap+display.Faint("│")+"  "+browser+display.Faint("│"),
		"   "+display.Faint("╰──────────────╯")+gap+display.Faint("╰──────────────────╯"),
		"",
		" Open this URL in your browser:",
		"   "+b.url,
		" Enter code: "+display.Strong(b.code),
		"",
	)
	switch state {
	case 1:
		lines = append(lines, " "+display.Ok("✓")+" Authorized")
	case 2:
		lines = append(lines, " "+display.Bad("✗")+" Authorization failed")
	default:
		left := int(time.Until(b.expires).Seconds())
		if left < 0 {
			left = 0
		}
		lines = append(lines, " "+display.Spin(frame)+" Waiting for browser authorization…"+display.Faint(fmt.Sprintf("  expires in %d:%02d", left/60, left%60)))
	}
	return lines
}

// ─── install ────────────────────────────────────────────────────────────────

const wire = 14

type installBoard struct {
	live  *display.Live
	names []string
	scope string

	mu      sync.Mutex
	results []*installResult
}

func newInstallBoard(out io.Writer, names []string, scope string) *installBoard {
	b := &installBoard{names: names, scope: scope, results: make([]*installResult, len(names))}
	b.live = display.NewLive(out, b.render)
	b.live.Start()
	return b
}

// done is the callback the install functions call with each client's result.
func (b *installBoard) done(i int, r installResult) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.results[i] = &r
	b.mu.Unlock()
}

func (b *installBoard) stop(stderr io.Writer) {
	if b == nil {
		return
	}
	b.live.Stop()
	// A row shows the start of a failure; print it in full below.
	for _, r := range b.results {
		if r != nil && r.status == "FAILED" {
			fmt.Fprintf(stderr, "%s: %s\n", r.client, r.detail)
		}
	}
}

func (b *installBoard) render(frame int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	lines := display.LogoFrame(version.Version, frame)
	lines = append(lines, " Connecting your AI clients to Telara"+display.Faint(" · "+b.scope+" scope"), "")
	width := 0
	for _, n := range b.names {
		if len(n) > width {
			width = len(n)
		}
	}
	connected := false
	for i, n := range b.names {
		hub := "   "
		switch {
		case len(b.names) == 1:
			hub += display.Accent("TELARA ━━")
		case i == 0:
			hub += display.Accent("TELARA ━┳")
		case i == len(b.names)-1:
			hub += display.Accent("        ┗")
		default:
			hub += display.Accent("        ┣")
		}
		label := n + strings.Repeat(" ", width-len(n))
		var line, status string
		r := b.results[i]
		switch {
		case r == nil && frame == display.Final:
			line = display.Faint(strings.Repeat("╌", wire) + "  " + label)
			status = display.Faint("· not reached")
		case r == nil:
			p := frame % wire
			line = display.Accent(strings.Repeat("━", p)) + display.Shine("●") + display.Faint(strings.Repeat("─", wire-p-1)) + "  " + label
			status = display.Spin(frame) + " connecting…"
		case r.status == "CONNECTED":
			connected = true
			line = display.Accent(strings.Repeat("━", wire-1)+"▶") + "  " + display.Strong(label)
			status = display.Ok("✓") + " connected" + display.Faint(" · "+r.detail)
		default:
			line = display.Bad(strings.Repeat("━", wire/2-1)+"╳"+strings.Repeat(" ", wire/2)) + "  " + label
			status = display.Bad("✗") + " " + clip(r.detail, 50)
		}
		lines = append(lines, hub+line+"   "+status)
	}
	lines = append(lines, "")
	if frame == display.Final && connected {
		lines = append(lines, " Restart your AI clients to load Telara. Check it any time with "+display.Strong("telara doctor")+".")
	}
	return lines
}

// ─── scan ───────────────────────────────────────────────────────────────────

type scanBoard struct {
	live *display.Live

	mu                                  sync.Mutex
	scanned                             bool
	scopes, complete, partial, servers  int
	submitting, submitted, submitFailed bool
}

func newScanBoard(out io.Writer) *scanBoard {
	b := &scanBoard{}
	b.live = display.NewLive(out, b.render)
	b.live.Start()
	return b
}

func (b *scanBoard) found(scopes, complete, partial, servers int) {
	b.mu.Lock()
	b.scanned, b.scopes, b.complete, b.partial, b.servers = true, scopes, complete, partial, servers
	b.mu.Unlock()
}

func (b *scanBoard) submit() { b.mu.Lock(); b.submitting = true; b.mu.Unlock() }

func (b *scanBoard) stop(ok bool) {
	b.mu.Lock()
	b.submitted, b.submitFailed = ok, !ok
	b.mu.Unlock()
	b.live.Stop()
}

func (b *scanBoard) render(frame int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	lit := 0
	if b.scanned {
		lit = b.servers
	}
	count := func(n int) string {
		if !b.scanned {
			return display.Faint("  …")
		}
		return display.Strong(fmt.Sprintf("%3d", n))
	}
	side := []string{
		display.Strong("Scanning this machine"), "",
		"Scopes scanned  " + count(b.scopes),
		"Complete        " + count(b.complete),
		"Partial         " + count(b.partial),
		"Servers found   " + count(b.servers),
		"", "",
	}
	switch {
	case b.submitted:
		side = append(side, display.Ok("✓")+" Discovery report submitted")
	case b.submitFailed:
		side = append(side, display.Bad("✗")+" Failed to submit discovery report")
	case b.submitting:
		side = append(side, display.Spin(frame)+" Submitting discovery report…")
	default:
		side = append(side, display.Spin(frame)+display.Faint(" looking…"))
	}
	radar := display.RadarFrame(frame, lit)
	for i := range radar {
		radar[i] = " " + radar[i]
	}
	return append(append([]string{""}, display.SideBySide(radar, side, 3)...), "")
}

// ─── doctor ─────────────────────────────────────────────────────────────────

type doctorBoard struct {
	live  *display.Live
	names []string

	mu      sync.Mutex
	current int
	results []*checkResult
}

func newDoctorBoard(out io.Writer, names []string) *doctorBoard {
	b := &doctorBoard{names: names, results: make([]*checkResult, len(names)), current: -1}
	b.live = display.NewLive(out, b.render)
	b.live.Start()
	return b
}

func (b *doctorBoard) begin(i int) { b.mu.Lock(); b.current = i; b.mu.Unlock() }
func (b *doctorBoard) end(i int, r checkResult) {
	b.mu.Lock()
	b.results[i] = &r
	b.mu.Unlock()
}
func (b *doctorBoard) stop() { b.live.Stop() }

func (b *doctorBoard) render(frame int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	lines := []string{"", "   " + display.Heartbeat(frame, 52), ""}
	width := 0
	for _, n := range b.names {
		if len(n) > width {
			width = len(n)
		}
	}
	for i, n := range b.names {
		label := n + strings.Repeat(" ", width-len(n)) + "  "
		r := b.results[i]
		switch {
		case r != nil:
			mark := map[string]string{"pass": display.Ok("✓"), "fail": display.Bad("✗"), "warn": display.Warn("▲"), "skip": display.Faint("─")}[r.status]
			if mark == "" {
				mark = " "
			}
			lines = append(lines, "   "+mark+" "+label+display.Faint(r.message))
		case i == b.current:
			lines = append(lines, "   "+display.Spin(frame)+" "+label+display.Faint("checking…"))
		default:
			lines = append(lines, "   "+display.Faint("· "+n))
		}
	}
	return append(lines, "")
}

// ─── update ─────────────────────────────────────────────────────────────────

type updateBoard struct {
	live     *display.Live
	from, to string

	mu          sync.Mutex
	done, total int64
	started     time.Time
	downloaded  bool
	installed   bool
	failed      string
}

func newUpdateBoard(out io.Writer, from, to string) *updateBoard {
	b := &updateBoard{from: from, to: to, started: time.Now()}
	b.live = display.NewLive(out, b.render)
	b.live.Start()
	return b
}

func (b *updateBoard) progress(done, total int64) {
	b.mu.Lock()
	b.done, b.total = done, total
	b.mu.Unlock()
}

func (b *updateBoard) set(f func(b *updateBoard)) {
	b.mu.Lock()
	f(b)
	b.mu.Unlock()
}

func (b *updateBoard) stop() { b.live.Stop() }

func (b *updateBoard) render(frame int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	lines := append([]string{""}, display.ConveyorFrame(b.from, b.to, b.done, b.total, b.installed)...)
	lines = append(lines, "", "   "+display.Ok("✓")+" Latest version is "+display.Strong(b.to)+display.Faint(" (you have "+b.from+")"))
	got := display.MB(b.done)
	switch {
	case b.downloaded:
		lines = append(lines, "   "+display.Ok("✓")+" Downloaded "+got+" MB")
	case b.failed != "" && !b.downloaded:
		lines = append(lines, "   "+display.Bad("✗")+" "+b.failed)
		return append(lines, "")
	case b.total > 0:
		rate := float64(b.done) / time.Since(b.started).Seconds() / (1 << 20)
		lines = append(lines, "   "+display.Spin(frame)+" Downloading  "+display.Bar(int(b.done>>10), int(b.total>>10), 24)+"  "+got+display.Faint("/"+display.MB(b.total)+" MB"+fmt.Sprintf("  %.1f MB/s", rate)))
	default:
		lines = append(lines, "   "+display.Spin(frame)+" Downloading  "+got+display.Faint(" MB"))
	}
	switch {
	case b.installed:
		lines = append(lines, "   "+display.Ok("✓")+" Installed "+display.Strong("telara "+b.to))
	case b.failed != "":
		lines = append(lines, "   "+display.Bad("✗")+" "+b.failed)
	case b.downloaded:
		lines = append(lines, "   "+display.Spin(frame)+" Installing…")
	}
	return append(lines, "")
}
