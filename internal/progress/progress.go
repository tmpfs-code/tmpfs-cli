// Package progress draws a throttled progress line on a terminal.
package progress

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Bar is a progress line. A nil *Bar is valid and draws nothing.
type Bar struct {
	w     io.Writer
	label string
	total int64

	mu        sync.Mutex
	done      int64
	start     time.Time
	lastDraw  time.Time
	lastWidth int
}

// New returns a bar writing to w, or nil (no output) if enabled is false.
func New(w io.Writer, enabled bool, label string, total int64) *Bar {
	if !enabled {
		return nil
	}
	return &Bar{w: w, label: label, total: total, start: time.Now()}
}

// Add records n more bytes done.
func (b *Bar) Add(n int64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.done += n
	if time.Since(b.lastDraw) >= 100*time.Millisecond {
		b.draw()
	}
}

// Finish draws the final state and ends the line.
func (b *Bar) Finish() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.draw()
	fmt.Fprintln(b.w)
}

// Clear erases the line (e.g. before printing an error).
func (b *Bar) Clear() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	fmt.Fprintf(b.w, "\r%s\r", strings.Repeat(" ", b.lastWidth))
}

func (b *Bar) draw() {
	b.lastDraw = time.Now()

	perc := 100.0
	if b.total > 0 {
		perc = float64(b.done) * 100 / float64(b.total)
	}
	const width = 24
	filled := int(perc / 100 * width)
	if filled > width {
		filled = width
	}

	rate := ""
	if secs := time.Since(b.start).Seconds(); secs > 0.5 {
		rate = "  " + FormatBytes(int64(float64(b.done)/secs)) + "/s"
	}

	line := fmt.Sprintf("%s [%s%s] %3.0f%%  %s / %s%s",
		b.label, strings.Repeat("=", filled), strings.Repeat(" ", width-filled),
		perc, FormatBytes(b.done), FormatBytes(b.total), rate)
	pad := ""
	if len(line) < b.lastWidth {
		pad = strings.Repeat(" ", b.lastWidth-len(line))
	}
	b.lastWidth = len(line)
	fmt.Fprintf(b.w, "\r%s%s", line, pad)
}

// FormatBytes formats a size with decimal units (like the webapp).
func FormatBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}
