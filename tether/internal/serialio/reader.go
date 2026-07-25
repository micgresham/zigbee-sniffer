// Package serialio reads framed messages from one or more sniffer dongles and
// merges them. Port of host/zbsniff/transport/serial_reader.py.
package serialio

import (
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"

	"zbsniff/internal/proto"
	"zbsniff/internal/stats"
)

// Tagged is a parsed message plus its source port and host arrival time.
type Tagged struct {
	Port   string
	HostTS float64
	Msg    any // *proto.CapturedFrame / *proto.EdResult / *proto.Status / *proto.Incident
}

// Reader manages serial ports and emits parsed messages.
type Reader struct {
	ports    []string
	baud     int
	out      chan Tagged
	mu       sync.Mutex
	sers     []serial.Port
	byName   map[string]serial.Port
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
	doneOnce sync.Once
	wg       sync.WaitGroup
	st       *stats.Stats
}

// New creates a Reader for the given ports.
func New(ports []string, baud int, st *stats.Stats) *Reader {
	if st == nil {
		st = stats.New()
	}
	return &Reader{ports: ports, baud: baud, out: make(chan Tagged, 10000),
		byName: map[string]serial.Port{}, stop: make(chan struct{}),
		done: make(chan struct{}), st: st}
}

// Done is closed when every port's read loop has exited (the connection is gone
// — either dropped or closed). The out channel is closed at the same time.
func (r *Reader) Done() <-chan struct{} { return r.done }

// C returns the channel of parsed messages.
func (r *Reader) C() <-chan Tagged { return r.out }

// Start opens all ports and begins reading. If any port fails to open, the ones
// already opened are closed and the error is returned (so callers can retry).
func (r *Reader) Start() error {
	opened := make([]serial.Port, 0, len(r.ports))
	for _, p := range r.ports {
		port, err := serial.Open(p, &serial.Mode{BaudRate: r.baud})
		if err != nil {
			for _, o := range opened {
				o.Close()
			}
			return err
		}
		port.SetReadTimeout(100 * time.Millisecond)
		opened = append(opened, port)
	}
	r.mu.Lock()
	for i, p := range r.ports {
		r.sers = append(r.sers, opened[i])
		r.byName[p] = opened[i]
		r.wg.Add(1)
		go r.read(p, opened[i])
	}
	r.mu.Unlock()
	// When all read loops exit, signal Done and close the output channel.
	go func() {
		r.wg.Wait()
		r.doneOnce.Do(func() {
			close(r.out)
			close(r.done)
		})
	}()
	r.st.MarkOpen() // records open time for the fresh-connect liveness watchdog
	r.st.Port.Store(strings.Join(r.ports, ", "))
	return nil
}

func (r *Reader) read(name string, port serial.Port) {
	defer r.wg.Done()
	dec := &proto.StreamDecoder{}
	buf := make([]byte, 4096)
	for {
		select {
		case <-r.stop:
			return
		default:
		}
		n, err := port.Read(buf)
		if err != nil {
			r.st.Open.Store(false)
			return
		}
		if n == 0 {
			continue
		}
		r.st.Bytes.Add(int64(n))
		now := float64(time.Now().UnixNano()) / 1e9
		for _, f := range dec.Feed(buf[:n]) {
			msg := proto.ParsePayload(f.Type, f.Payload)
			if msg == nil {
				continue
			}
			// Non-blocking send; also bail if the reader is shutting down so we
			// never write to a closed channel.
			select {
			case <-r.stop:
				return
			case r.out <- Tagged{Port: name, HostTS: now, Msg: msg}:
			default: // queue full — drop to stay live
			}
		}
	}
}

// Send writes a command frame to every open port.
func (r *Reader) Send(data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.sers {
		p.Write(data)
	}
}

// SendTo writes a command frame to a single named port (per-radio targeting).
// An empty name broadcasts to all ports. Returns false if the port is unknown.
func (r *Reader) SendTo(name string, data []byte) bool {
	if name == "" {
		r.Send(data)
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.byName[name]; ok {
		p.Write(data)
		return true
	}
	return false
}

// Ports returns the names of the currently open ports.
func (r *Reader) Ports() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.byName))
	for n := range r.byName {
		out = append(out, n)
	}
	return out
}

// Close stops reading and closes the ports (idempotent).
func (r *Reader) Close() {
	r.stopOnce.Do(func() { close(r.stop) })
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.sers {
		p.Close()
	}
}
