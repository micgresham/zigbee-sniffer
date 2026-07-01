// Package runtime holds mutable host state that the API can change at runtime
// (the decryption key) and the latest device status, shared with the ingest loop.
package runtime

import "sync"

type Runtime struct {
	mu         sync.RWMutex
	key        []byte
	pan        uint16
	lastStatus map[string]any
}

// New creates a Runtime with an optional initial network key.
func New(key []byte) *Runtime { return &Runtime{key: key} }

// SetPan records the network PAN id (needed to address active probes).
func (r *Runtime) SetPan(pan uint16) {
	r.mu.Lock()
	r.pan = pan
	r.mu.Unlock()
}

// Pan returns the network PAN id (0 if unknown).
func (r *Runtime) Pan() uint16 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.pan
}

// Key returns the current decryption key (nil = no decryption).
func (r *Runtime) Key() []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.key
}

// SetKey updates the decryption key live (nil/empty clears it).
func (r *Runtime) SetKey(k []byte) {
	r.mu.Lock()
	if len(k) == 16 {
		r.key = k
	} else {
		r.key = nil
	}
	r.mu.Unlock()
}

// HasKey reports whether decryption is active.
func (r *Runtime) HasKey() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.key) == 16
}

// SetStatus stores the latest device status (as a JSON-ready map).
func (r *Runtime) SetStatus(s map[string]any) {
	r.mu.Lock()
	r.lastStatus = s
	r.mu.Unlock()
}

// Status returns the latest device status (or an empty map).
func (r *Runtime) Status() map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.lastStatus == nil {
		return map[string]any{}
	}
	return r.lastStatus
}
