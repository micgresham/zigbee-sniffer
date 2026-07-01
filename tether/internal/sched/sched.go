// Package sched runs recurring active tests ("cron" for probes). Each schedule
// fires a MAC probe at a fixed interval against a target via a chosen radio.
package sched

import (
	"time"

	"zbsniff/internal/store"
)

// ProbeFunc sends one probe (target short addr, pan, port="" = any radio).
// Returns false if it could not be dispatched (e.g. the radio isn't connected).
type ProbeFunc func(target, pan int, port string) bool

// Scheduler ticks once a second and fires any due schedules.
type Scheduler struct {
	db    *store.DB
	probe ProbeFunc
	stop  chan struct{}
}

func New(db *store.DB, probe ProbeFunc) *Scheduler {
	return &Scheduler{db: db, probe: probe, stop: make(chan struct{})}
}

// Run starts the tick loop (call as a goroutine).
func (s *Scheduler) Run() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			now := float64(time.Now().UnixNano()) / 1e9
			for _, sc := range s.db.Schedules() {
				if !sc.Enabled || sc.IntervalS <= 0 {
					continue
				}
				if now-sc.LastRun < float64(sc.IntervalS) {
					continue
				}
				if s.probe(sc.Target, sc.Pan, sc.Port) {
					s.db.MarkScheduleRun(sc.ID, now, false)
				}
			}
		}
	}
}

func (s *Scheduler) Stop() { close(s.stop) }
