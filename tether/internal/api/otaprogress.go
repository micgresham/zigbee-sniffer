package api

import "sync"

// OtaProgress tracks the last OTA_STATUS a target has actually acknowledged —
// both the "received" byte count (so /api/ota's sender can pace itself against
// real progress instead of a blind fixed delay: the satellite's own OTA queue
// is small and drops chunks, silently bar a log line, that arrive faster than
// it can flash-write them) and the last reported state (so CMD_OTA_END, a
// single one-shot message with no chunk-level redundancy, can be confirmed
// and retried if it never lands — unlike DATA, which tolerates the occasional
// relay hiccup because there are 1000+ of them).
type OtaProgress struct {
	mu    sync.Mutex
	rcv   map[int]uint32
	state map[int]byte
}

func NewOtaProgress() *OtaProgress {
	return &OtaProgress{rcv: map[int]uint32{}, state: map[int]byte{}}
}

func (o *OtaProgress) Update(target int, received uint32, state byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.rcv[target] = received
	o.state[target] = state
}

func (o *OtaProgress) Get(target int) uint32 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.rcv[target]
}

// State returns the last reported OTA state for target (proto.OtaIdle if
// none has ever been reported).
func (o *OtaProgress) State(target int) byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state[target]
}

// Reset clears a target's tracked progress — call before starting a new
// transfer so a stale count/state from a previous attempt can't let the flow-
// control gate or the OTA_END confirmation wait think it's further along than
// it really is.
func (o *OtaProgress) Reset(target int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.rcv, target)
	delete(o.state, target)
}
