// Package logbuf is an io.Writer that keeps the last N log lines in a ring so
// the web UI can show the host's log/diagnostic output.
package logbuf

import (
	"strings"
	"sync"
)

type Buf struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func New(max int) *Buf { return &Buf{max: max} }

// Write implements io.Writer (used via log.SetOutput with a MultiWriter).
func (b *Buf) Write(p []byte) (int, error) {
	b.mu.Lock()
	for _, ln := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if ln == "" {
			continue
		}
		b.lines = append(b.lines, ln)
	}
	if len(b.lines) > b.max {
		b.lines = b.lines[len(b.lines)-b.max:]
	}
	b.mu.Unlock()
	return len(p), nil
}

// Lines returns a copy of the buffered log lines (oldest first).
func (b *Buf) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.lines...)
}
