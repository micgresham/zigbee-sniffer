package api

import "sync"

// WatchSet tracks addresses the operator has flagged in the device drawer —
// e.g. a device that dropped off the mesh — so ingest() can surface a "watch"
// WS event the moment that address does anything on-air again, instead of the
// operator having to babysit the Live Frames table for it.
type WatchSet struct {
	mu   sync.Mutex
	addr map[string]bool
}

func NewWatchSet() *WatchSet { return &WatchSet{addr: map[string]bool{}} }

func (w *WatchSet) Add(addr string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.addr[addr] = true
}

func (w *WatchSet) Remove(addr string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.addr, addr)
}

func (w *WatchSet) Has(addr string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.addr[addr]
}

func (w *WatchSet) List() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, 0, len(w.addr))
	for a := range w.addr {
		out = append(out, a)
	}
	return out
}
