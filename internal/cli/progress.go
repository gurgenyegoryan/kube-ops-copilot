package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

type liveProgress struct {
	out        io.Writer
	isTTY      bool
	label      string
	current    string
	lastStatic string
	done       chan struct{}
	once       sync.Once
	mu         sync.Mutex
}

func newLiveProgress(out io.Writer, label string) *liveProgress {
	p := &liveProgress{
		out:   out,
		label: strings.TrimSpace(label),
		done:  make(chan struct{}),
	}
	if f, ok := out.(*os.File); ok {
		if st, err := f.Stat(); err == nil {
			p.isTTY = (st.Mode() & os.ModeCharDevice) != 0
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
	p.current = msg
	isTTY := p.isTTY
	duplicate := msg == p.lastStatic
	if !isTTY && !duplicate {
		p.lastStatic = msg
	}
	p.mu.Unlock()
	if !isTTY && !duplicate {
		_, _ = fmt.Fprintf(p.out, "[progress] %s\n", p.renderMessage(msg))
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
				p.clearLocked()
				_, _ = fmt.Fprintf(p.out, "\r[%s] %s", frames[i%len(frames)], p.renderMessage(msg))
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
	_, _ = fmt.Fprint(p.out, "\r\033[2K")
}

func (p *liveProgress) renderMessage(msg string) string {
	if p.label == "" {
		return msg
	}
	return p.label + ": " + msg
}
