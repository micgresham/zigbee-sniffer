// Package stats holds live connection/ingest counters shared between the serial
// reader and the ingest loop, surfaced by the API's diagnostics window.
package stats

import (
	"sync/atomic"
	"time"
)

type Stats struct {
	Bytes      atomic.Int64 // raw bytes read from the device
	Frames     atomic.Int64 // CAPTURED_FRAME messages
	ED         atomic.Int64 // ED_RESULT messages
	Status     atomic.Int64 // STATUS messages
	Incidents  atomic.Int64
	Msgs       atomic.Int64 // total framed messages parsed
	lastMsgUtc atomic.Int64 // unix ms of last message
	Port       atomic.Value // string
	Open       atomic.Bool  // serial port currently open
}

func New() *Stats { s := &Stats{}; s.Port.Store(""); return s }

// Touch records that a framed message arrived now.
func (s *Stats) Touch() { s.Msgs.Add(1); s.lastMsgUtc.Store(time.Now().UnixMilli()) }

// LastMsgAge is the time since the last framed message. It returns 0 when no
// message has ever arrived (so a liveness watchdog doesn't trip before the
// initial connection produces data).
func (s *Stats) LastMsgAge() time.Duration {
	last := s.lastMsgUtc.Load()
	if last == 0 {
		return 0
	}
	return time.Since(time.UnixMilli(last))
}

// Snapshot returns a JSON-ready view of the counters.
func (s *Stats) Snapshot() map[string]any {
	last := s.lastMsgUtc.Load()
	ageMs := int64(-1)
	if last > 0 {
		ageMs = time.Now().UnixMilli() - last
	}
	port, _ := s.Port.Load().(string)
	return map[string]any{
		"port": port, "open": s.Open.Load(),
		"bytes": s.Bytes.Load(), "msgs": s.Msgs.Load(),
		"frames": s.Frames.Load(), "ed": s.ED.Load(), "status": s.Status.Load(),
		"incidents": s.Incidents.Load(), "last_msg_age_ms": ageMs,
	}
}
