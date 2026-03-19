package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

type progressEvent struct {
	at   time.Time
	text string
}

type liveProgress struct {
	out        io.Writer
	isTTY      bool
	label      string
	current    string
	currentAt  time.Time
	lastStatic string
	history    []progressEvent
	maxHistory int
	rendered   int
	done       chan struct{}
	once       sync.Once
	mu         sync.Mutex
}

func newLiveProgress(out io.Writer, label string) *liveProgress {
	p := &liveProgress{
		out:        out,
		label:      strings.TrimSpace(label),
		done:       make(chan struct{}),
		maxHistory: 8,
	}
	if fd, ok := writerFD(out); ok {
		p.isTTY = term.IsTerminal(fd)
	}
	if !p.isTTY {
		if f, ok := out.(*os.File); ok {
			if st, err := f.Stat(); err == nil {
				p.isTTY = (st.Mode() & os.ModeCharDevice) != 0
			}
		}
	}
	if p.isTTY {
		go p.renderLoop()
	}
	return p
}

func (p *liveProgress) Updatef(format string, args ...any) {
	msg := strings.TrimSpace(fmt.Sprintf(format, args...))
	if msg == "" {
		return
	}
	p.mu.Lock()
	if msg != p.current {
		p.appendEventLocked(msg)
		p.currentAt = time.Now()
	}
	p.current = msg
	isTTY := p.isTTY
	duplicate := msg == p.lastStatic
	if !duplicate {
		p.lastStatic = msg
	}
	if isTTY && !duplicate {
		p.logTTYLocked("[phase]", p.renderMessage(msg))
	}
	p.mu.Unlock()
	if !isTTY && !duplicate {
		_, _ = fmt.Fprintf(p.out, "[progress] %s\n", p.renderMessage(msg))
	}
}

func (p *liveProgress) Eventf(format string, args ...any) {
	msg := strings.TrimSpace(fmt.Sprintf(format, args...))
	if msg == "" {
		return
	}
	p.mu.Lock()
	p.appendEventLocked(msg)
	isTTY := p.isTTY
	if isTTY {
		p.logTTYLocked("  ->", p.renderMessage(msg))
	}
	p.mu.Unlock()
	if !isTTY {
		_, _ = fmt.Fprintf(p.out, "[event] %s\n", p.renderMessage(msg))
	}
}

func (p *liveProgress) Printf(format string, args ...any) {
	msg := strings.TrimSpace(fmt.Sprintf(format, args...))
	if msg == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.isTTY {
		p.clearLocked()
		p.rendered = 0
	}
	_, _ = fmt.Fprintln(p.out, msg)
}

func (p *liveProgress) Donef(format string, args ...any) {
	p.finish("done", format, args...)
}

func (p *liveProgress) Failf(format string, args ...any) {
	p.finish("failed", format, args...)
}

func (p *liveProgress) Close() {
	p.once.Do(func() { close(p.done) })
	if p.isTTY {
		p.mu.Lock()
		p.clearLocked()
		p.mu.Unlock()
	}
}

func (p *liveProgress) finish(state, format string, args ...any) {
	msg := strings.TrimSpace(fmt.Sprintf(format, args...))
	p.Close()
	if msg == "" {
		msg = state
	}
	_, _ = fmt.Fprintf(p.out, "[%s] %s\n", state, p.renderMessage(msg))
}

func (p *liveProgress) renderLoop() {
	frames := []string{"-", "\\", "|", "/"}
	ticker := time.NewTicker(120 * time.Millisecond)
	defer ticker.Stop()
	i := 0
	for {
		select {
		case <-ticker.C:
			p.mu.Lock()
			msg := p.current
			if strings.TrimSpace(msg) != "" {
				p.renderLocked(frames[i%len(frames)])
			}
			p.mu.Unlock()
			i++
		case <-p.done:
			return
		}
	}
}

func (p *liveProgress) clearLocked() {
	if !p.isTTY {
		return
	}
	if p.rendered == 0 {
		return
	}
	if p.rendered > 1 {
		_, _ = fmt.Fprintf(p.out, "\r\033[%dA", p.rendered-1)
	} else {
		_, _ = fmt.Fprint(p.out, "\r")
	}
	for i := 0; i < maxInt(1, p.rendered); i++ {
		_, _ = fmt.Fprint(p.out, "\033[2K")
		if i < maxInt(1, p.rendered)-1 {
			_, _ = fmt.Fprint(p.out, "\n")
		}
	}
	if p.rendered > 1 {
		_, _ = fmt.Fprintf(p.out, "\r\033[%dA", p.rendered-1)
	} else {
		_, _ = fmt.Fprint(p.out, "\r")
	}
	p.rendered = 0
}

func (p *liveProgress) renderMessage(msg string) string {
	if p.label == "" {
		return msg
	}
	return p.label + ": " + msg
}

func (p *liveProgress) appendEventLocked(msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	if n := len(p.history); n > 0 && p.history[n-1].text == msg {
		return
	}
	p.history = append(p.history, progressEvent{at: time.Now(), text: msg})
	if len(p.history) > p.maxHistory {
		p.history = p.history[len(p.history)-p.maxHistory:]
	}
}

func (p *liveProgress) renderLocked(frame string) {
	lines := make([]string, 0, 1+len(p.history))
	elapsed := ""
	if !p.currentAt.IsZero() {
		elapsed = fmt.Sprintf(" (%s)", time.Since(p.currentAt).Round(time.Second))
	}
	lines = append(lines, fmt.Sprintf("[%s] %s%s", frame, p.renderMessage(p.current), elapsed))
	for _, event := range p.history {
		lines = append(lines, fmt.Sprintf("  -> %s %s", event.at.Format("15:04:05"), p.renderMessage(event.text)))
	}

	if p.rendered > 1 {
		_, _ = fmt.Fprintf(p.out, "\r\033[%dA", p.rendered-1)
	} else {
		_, _ = fmt.Fprint(p.out, "\r")
	}

	total := maxInt(p.rendered, len(lines))
	for i := 0; i < total; i++ {
		_, _ = fmt.Fprint(p.out, "\033[2K")
		if i < len(lines) {
			_, _ = fmt.Fprint(p.out, lines[i])
		}
		if i < total-1 {
			_, _ = fmt.Fprint(p.out, "\n")
		}
	}
	p.rendered = len(lines)
}

func (p *liveProgress) logTTYLocked(prefix, msg string) {
	if !p.isTTY {
		return
	}
	p.clearLocked()
	p.rendered = 0
	_, _ = fmt.Fprintf(p.out, "%s %s %s\n", prefix, time.Now().Format("15:04:05"), msg)
}

func writerFD(w io.Writer) (int, bool) {
	type fdWriter interface {
		Fd() uintptr
	}
	if fw, ok := w.(fdWriter); ok {
		return int(fw.Fd()), true
	}
	return 0, false
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
