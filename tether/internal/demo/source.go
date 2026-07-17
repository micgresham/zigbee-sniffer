package demo

import (
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"zbsniff/internal/proto"
	"zbsniff/internal/serialio"
)

// Port is the fake "port" every simulated radio shares — like a real
// satellite relay, radio 0 (home) and radio 1 (Hue channel) share one port
// and are told apart by RadioID, so the existing multi-radio UI/tracking code
// (built for exactly that shape) works unmodified.
const Port = "demo://simulated"

// Source generates a live-looking capture: a home mesh, a Hue foreign
// network, and a second foreign network on a different channel. It
// implements the same minimal interface serialio.Reader does (C() <-chan
// serialio.Tagged), so main.go's ingest() runs identically over it.
type Source struct {
	out  chan serialio.Tagged
	stop chan struct{}

	scanMu   sync.Mutex
	scanStop chan struct{} // non-nil while a continuous ED sweep is running

	// spectrumRadio is the radioID (0 or 1) currently dedicated to an ED
	// sweep, or -1 if none — a single simulated radio can't sniff and sweep
	// at once, same as real hardware, so that radio's traffic loop pauses
	// and its status reports mode_id=2 (ED sweep) instead of 1 (capturing)
	// while this is set.
	spectrumRadio atomic.Int32
}

// activeSource lets the API layer trigger a simulated spectrum scan without
// main.go having to plumb a new callback through Server — /api/scan and
// /api/mode just call the package-level functions below, which are no-ops
// (returning false) when demo mode isn't running.
var activeSource atomic.Pointer[Source]

func New() *Source {
	s := &Source{out: make(chan serialio.Tagged, 1000), stop: make(chan struct{})}
	s.spectrumRadio.Store(-1)
	activeSource.Store(s)
	return s
}

// TriggerScan runs one simulated one-shot ED sweep across all 16 channels on
// the given radio, mirroring the real hardware's CMD_ED_SCAN — returns false
// if demo mode isn't active. radioID briefly stops sniffing for the duration
// of the sweep, same as a real radio would.
func TriggerScan(radioID, dwellMs int) bool {
	s := activeSource.Load()
	if s == nil {
		return false
	}
	go func() {
		s.spectrumRadio.Store(int32(radioID))
		s.scanOnce(radioID, dwellMs)
		s.spectrumRadio.Store(-1)
	}()
	return true
}

// StartContinuousScan begins repeating simulated sweeps on radioID (mirrors
// putting a real radio into MODE_ED_SWEEP) until StopContinuousScan is called.
func StartContinuousScan(radioID, dwellMs int) bool {
	s := activeSource.Load()
	if s == nil {
		return false
	}
	s.startContinuous(radioID, dwellMs)
	return true
}

// StopContinuousScan halts a running continuous simulated sweep, if any, and
// lets its radio resume sniffing.
func StopContinuousScan() bool {
	s := activeSource.Load()
	if s == nil {
		return false
	}
	s.stopContinuous()
	return true
}

func (s *Source) C() <-chan serialio.Tagged { return s.out }

func (s *Source) Close() { close(s.stop) }

func (s *Source) send(msg any) {
	select {
	case <-s.stop:
	case s.out <- serialio.Tagged{Port: Port, HostTS: nowTS(), Msg: msg}:
	default:
	}
}

func nowTS() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// deterministicSignal derives a stable-per-pair RSSI/LQI so the same
// conversation always reads about the same signal quality across the whole
// session (a consistent physical layout), with small jitter so it isn't
// perfectly static.
func deterministicSignal(a, b uint16, rnd *rand.Rand) (rssi int8, lqi byte) {
	h := int(a)*31 + int(b)
	base := -55 - (h % 35) // -55..-89
	jitter := rnd.Intn(7) - 3
	rssi = int8(base + jitter)
	lqiBase := 255 - (h%10)*8
	lqiJitter := rnd.Intn(9) - 4
	l := lqiBase + lqiJitter
	if l > 255 {
		l = 255
	}
	if l < 60 {
		l = 60
	}
	lqi = byte(l)
	return
}

// Run starts the background generators. Call Close to stop them; the message
// channel then drains and callers should stop reading once idle.
func (s *Source) Run() {
	rnd := rand.New(rand.NewSource(1)) // fixed seed: same demo "feel" every run
	go s.statusLoop()
	go s.homeTrafficLoop(rnd)
	go s.hueTrafficLoop(rnd)
	go s.foreignTrafficLoop(rnd)
	go s.threadTrafficLoop(rnd)
}

func (s *Source) statusLoop() {
	start := time.Now()
	var homeCaptured, hueCaptured uint32
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-tick.C:
			sweeping := s.spectrumRadio.Load()
			mode0, mode1 := byte(1), byte(1)
			if sweeping == 0 {
				mode0 = 2 // ED sweep — matches proto.ModeEdSweep, shown as "ED sweep" in the header
			} else {
				homeCaptured += uint32(2 + rand.Intn(4))
			}
			if sweeping == 1 {
				mode1 = 2
			} else {
				hueCaptured += uint32(1 + rand.Intn(3))
			}
			uptime := uint32(time.Since(start).Seconds())
			s.send(&proto.Status{
				RadioID: 0, Mode: mode0, Channel: HomeChan, UptimeS: uptime,
				Captured: homeCaptured, FWMajor: 0, FWMinor: 29,
			})
			s.send(&proto.Status{
				RadioID: 1, Mode: mode1, Channel: HueChan, UptimeS: uptime,
				Captured: hueCaptured, FWMajor: 0, FWMinor: 29,
			})
		}
	}
}

var seqCounters = map[uint16]byte{}
var frameCounters = map[uint16]uint32{}

func nextSeq(m map[uint16]byte, key uint16) byte {
	v := m[key]
	m[key] = v + 1
	return v
}
func nextCounter(key uint16) uint32 {
	v := frameCounters[key]
	frameCounters[key] = v + 1
	return v
}

// emitConversation sends one NWK-secured message end-to-end (nwkSrc->nwkDst),
// relayed hop-by-hop along hops (MAC-layer neighbors only) — each hop gets its
// own Data+Ack frame pair, exactly as a sniffer at a fixed vantage point would
// see a multi-hop relay, so the routing tree reconstructs real depth.
func (s *Source) emitConversation(radio byte, pan, channel uint16, hops []uint16, nwkSrc, nwkDst uint16, srcExt uint64, apsPayload []byte, rnd *rand.Rand) {
	fc := nextCounter(nwkSrc)
	nwkSeq := nextSeq(seqCounters, nwkSrc^0x8000)
	nwk := nwkSecured(nwkSrc, nwkDst, srcExt, fc, nwkSeq, apsPayload)
	if nwk == nil {
		return
	}
	for i := 0; i+1 < len(hops); i++ {
		macSrc, macDst := hops[i], hops[i+1]
		seq := nextSeq(seqCounters, macSrc)
		rssi, lqi := deterministicSignal(macSrc, macDst, rnd)
		mpdu := hopMAC(seq, pan, macSrc, macDst, nwk)
		s.send(&proto.CapturedFrame{RadioID: radio, Channel: byte(channel), RSSI: rssi, LQI: lqi, MPDU: mpdu})
		time.Sleep(3 * time.Millisecond)
		s.send(&proto.CapturedFrame{RadioID: radio, Channel: byte(channel), RSSI: rssi, LQI: lqi, MPDU: ackMAC(seq)})
		time.Sleep(2 * time.Millisecond)
	}
}

var sensorClusters = []uint16{clusterTemp, clusterHumidity, clusterOccupancy, clusterPowerCfg}

func (s *Source) homeTrafficLoop(rnd *rand.Rand) {
	for {
		select {
		case <-s.stop:
			return
		case <-time.After(time.Duration(300+rnd.Intn(500)) * time.Millisecond):
		}
		if s.spectrumRadio.Load() == 0 {
			continue // radio 0 is dedicated to an ED sweep right now
		}
		d := HomeDevices[rnd.Intn(len(HomeDevices))]
		hops := path(d)
		if rnd.Intn(3) == 0 && d.Type == "Router" {
			// A controller (coordinator) command down to a router-capable
			// device, e.g. a plug — reverse hop order (coordinator -> device).
			args := []byte{}
			cmd := byte(rnd.Intn(3)) // Off/On/Toggle
			payload := zclCommand(nextSeq(seqCounters, Coordinator|0x4000), nextSeq(seqCounters, Coordinator|0x5000), clusterOnOff, cmd, args)
			rev := make([]uint16, len(hops))
			for i := range hops {
				rev[i] = hops[len(hops)-1-i]
			}
			s.emitConversation(0, HomePAN, HomeChan, rev, Coordinator, d.Short, coordinatorExt, payload, rnd)
			continue
		}
		cluster := sensorClusters[rnd.Intn(len(sensorClusters))]
		payload := zclReportAttrs(nextSeq(seqCounters, d.Short|0x4000), nextSeq(seqCounters, d.Short|0x5000), cluster)
		s.emitConversation(0, HomePAN, HomeChan, hops, d.Short, Coordinator, d.Ext, payload, rnd)
	}
}

func (s *Source) hueTrafficLoop(rnd *rand.Rand) {
	bridge := HueDevices[0]
	for {
		select {
		case <-s.stop:
			return
		case <-time.After(time.Duration(800+rnd.Intn(1200)) * time.Millisecond):
		}
		if s.spectrumRadio.Load() == 1 {
			continue // radio 1 is dedicated to an ED sweep right now
		}
		bulb := HueDevices[1+rnd.Intn(len(HueDevices)-1)]
		var payload []byte
		if rnd.Intn(2) == 0 {
			payload = zclCommand(nextSeq(seqCounters, bridge.Short|0x4000), nextSeq(seqCounters, bridge.Short|0x5000), clusterOnOff, byte(rnd.Intn(2)), nil)
			s.emitConversation(1, HuePAN, HueChan, []uint16{bridge.Short, bulb.Short}, bridge.Short, bulb.Short, bridge.Ext, payload, rnd)
		} else {
			payload = zclCommand(nextSeq(seqCounters, bridge.Short|0x4000), nextSeq(seqCounters, bridge.Short|0x5000), clusterColorCtl, 0x0A, []byte{0x00, 0x02, 0x0A, 0x00})
			s.emitConversation(1, HuePAN, HueChan, []uint16{bridge.Short, bulb.Short}, bridge.Short, bulb.Short, bridge.Ext, payload, rnd)
		}
	}
}

// foreignTrafficLoop is sparse and unencrypted-looking (we don't have — and a
// real sniffer wouldn't have — that network's key), just enough to populate
// the Networks tab and foreign-device list with a second, distinct PAN/channel.
func (s *Source) foreignTrafficLoop(rnd *rand.Rand) {
	a, b := ForeignDevices[0], ForeignDevices[1]
	for {
		select {
		case <-s.stop:
			return
		case <-time.After(time.Duration(4000+rnd.Intn(6000)) * time.Millisecond):
		}
		if s.spectrumRadio.Load() == 1 {
			continue // radio 1 is dedicated to an ED sweep right now
		}
		seq := nextSeq(seqCounters, a.Short)
		rssi, lqi := deterministicSignal(a.Short, b.Short, rnd)
		// Foreign network: NWK-secured with a key we don't have, so it stays
		// "Secure(undecrypted)" — DecodeFrame will bail after the NWK header,
		// same as a real foreign network's traffic, still populating MAC/NWK
		// device + PAN tracking.
		nwk := le16(nil, 0x0A08) // secured (bit9) + version 2
		nwk = le16(nwk, b.Short)
		nwk = le16(nwk, a.Short)
		nwk = append(nwk, 30, seq)
		nwk = append(nwk, 0x28, 0, 0, 0, 0)
		nwk = append(nwk, 0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE) // opaque ciphertext-looking bytes, never decrypted
		mpdu := hopMAC(seq, ForeignPAN, a.Short, b.Short, nwk)
		s.send(&proto.CapturedFrame{RadioID: 1, Channel: ForeignChan, RSSI: rssi, LQI: lqi, MPDU: mpdu})
	}
}

// threadTrafficLoop is a third foreign network — a Thread/Matter mesh on its
// own channel. Unlike foreignTrafficLoop (which fakes a Zigbee NWK header we
// simply can't decrypt), this payload starts with a real 6LoWPAN IPHC
// dispatch byte (RFC 6282, 0x60-0x7F) so it exercises the decoder's actual
// Thread/Matter detection path (decode.isSixLowPANDispatch) rather than just
// failing the Zigbee NWK version check for an unrelated reason.
func (s *Source) threadTrafficLoop(rnd *rand.Rand) {
	a, b := ThreadDevices[0], ThreadDevices[1]
	for {
		select {
		case <-s.stop:
			return
		case <-time.After(time.Duration(3000+rnd.Intn(5000)) * time.Millisecond):
		}
		if s.spectrumRadio.Load() == 1 {
			continue // radio 1 is dedicated to an ED sweep right now
		}
		seq := nextSeq(seqCounters, a.Short)
		rssi, lqi := deterministicSignal(a.Short, b.Short, rnd)
		// LOWPAN_IPHC dispatch (0x7A: TF=01 NH=1 HLIM=10 CID=0 SAC=0 SAM=00
		// DAC=0 DAM=00 — a plausible compressed-header pattern), followed by
		// opaque bytes standing in for the (to us, unreadable) compressed
		// IPv6/UDP/CoAP payload a real Matter device would send.
		payload := []byte{0x7A, 0x33, 0x5C, 0x01}
		payload = append(payload, 0xCA, 0xFE, 0xBA, 0xBE, seq)
		mpdu := hopMAC(seq, ThreadPAN, a.Short, b.Short, payload)
		s.send(&proto.CapturedFrame{RadioID: 1, Channel: ThreadChan, RSSI: rssi, LQI: lqi, MPDU: mpdu})
	}
}

var sweepCounter uint32

func noiseFloor() int8 { return int8(-92 + rand.Intn(6)) } // -92..-87

// edLevelFor fakes one channel's reading for one sweep. Real Zigbee traffic
// is bursty — a device transmits for a millisecond or two, then the channel
// sits idle — so even a channel with an active network reads as noise floor
// most of the time; only occasionally does a sweep's dwell window land on an
// actual burst. Always returning a hot value here would look like a
// continuous carrier/jammer, not real traffic.
func edLevelFor(ch int) int8 {
	switch ch {
	case HomeChan, HueChan:
		if rand.Intn(100) < 30 { // ~30% of sweeps catch a burst in progress
			return int8(-48 + rand.Intn(10)) // -48..-39
		}
		return noiseFloor()
	case ForeignChan, ThreadChan:
		if rand.Intn(100) < 20 {
			return int8(-60 + rand.Intn(8)) // -60..-53
		}
		return noiseFloor()
	default:
		return noiseFloor()
	}
}

// scanOnce emits one full 11-26 ED sweep, dwellMs apart per channel — the
// same shape as a real CMD_ED_SCAN reply stream.
func (s *Source) scanOnce(radioID, dwellMs int) {
	if dwellMs <= 0 {
		dwellMs = 5
	}
	sweepID := uint16(atomic.AddUint32(&sweepCounter, 1))
	for ch := 11; ch <= 26; ch++ {
		s.send(&proto.EdResult{RadioID: byte(radioID), Channel: byte(ch), EdDBm: edLevelFor(ch), SweepID: sweepID})
		time.Sleep(time.Duration(dwellMs) * time.Millisecond)
	}
}

// startContinuous repeats scanOnce back-to-back on radioID until
// stopContinuous is called or the source itself is closed — mirrors
// MODE_ED_SWEEP on real hardware, which sweeps continuously once put in that
// mode.
func (s *Source) startContinuous(radioID, dwellMs int) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if s.scanStop != nil {
		return // already running
	}
	s.spectrumRadio.Store(int32(radioID))
	stopCh := make(chan struct{})
	s.scanStop = stopCh
	go func() {
		for {
			select {
			case <-s.stop:
				return
			case <-stopCh:
				return
			default:
				s.scanOnce(radioID, dwellMs)
			}
		}
	}()
}

func (s *Source) stopContinuous() {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if s.scanStop != nil {
		close(s.scanStop)
		s.scanStop = nil
	}
	s.spectrumRadio.Store(-1)
}
