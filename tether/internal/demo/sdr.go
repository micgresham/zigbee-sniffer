package demo

// Simulated wideband SDR sweep: emits proto.SdrSegment messages shaped like a
// real ESP32-C6's (80 MS/s, LO-centered, analog filter roll-off toward the
// edges, DC bin zeroed by mean removal) so the host's stitcher and the
// dashboard's waterfall run over exactly the same path as on hardware.

import (
	"math"
	"math/rand"
	"sync/atomic"
	"time"

	"zbsniff/internal/proto"
)

var sdrSweepCounter uint32

type sdrRun struct {
	radio    int
	los      []uint16
	fftLog2  int
	peak     bool
	interval time.Duration
	stop     chan struct{}
}

// StartSdr begins a simulated wideband sweep on radioID (replacing any running
// one) — mirrors MODE_SDR_SPECTRUM. Returns false if demo mode isn't active.
func StartSdr(radioID int, los []uint16, fftLog2 int, peak bool, intervalMs int) bool {
	s := activeSource.Load()
	if s == nil {
		return false
	}
	s.stopSdr()
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	run := &sdrRun{radio: radioID, los: append([]uint16(nil), los...), fftLog2: fftLog2, peak: peak,
		interval: time.Duration(intervalMs) * time.Millisecond, stop: make(chan struct{})}
	s.sdr = run
	s.spectrumRadio.Store(int32(radioID)) // pauses that radio's simulated traffic
	s.sdrRadio.Store(int32(radioID))
	go s.sdrLoop(run)
	return true
}

// StopSdr halts the simulated sweep and lets its radio resume sniffing.
func StopSdr() bool {
	s := activeSource.Load()
	if s == nil {
		return false
	}
	s.stopSdr()
	return true
}

func (s *Source) stopSdr() {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if s.sdr != nil {
		close(s.sdr.stop)
		s.sdr = nil
		s.spectrumRadio.Store(-1)
		s.sdrRadio.Store(-1)
	}
}

func (s *Source) sdrLoop(run *sdrRun) {
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	start := time.Now()
	for {
		t0 := time.Now()
		id := uint16(atomic.AddUint32(&sdrSweepCounter, 1))
		for i, lo := range run.los {
			select {
			case <-s.stop:
				return
			case <-run.stop:
				return
			default:
			}
			// ~12 ms of capture + FFT per LO on real hardware.
			time.Sleep(12 * time.Millisecond)
			s.send(sdrSegment(run, id, i, lo, time.Since(start).Seconds(), rnd))
		}
		wait := run.interval - time.Since(t0)
		if wait < 20*time.Millisecond {
			wait = 20 * time.Millisecond
		}
		select {
		case <-s.stop:
			return
		case <-run.stop:
			return
		case <-time.After(wait):
		}
	}
}

// emitter is one simulated signal: power (dBFS, per 312.5 kHz bin) at f MHz.
type emitter func(f float64) float64

// band is a flat-topped channel of the given width with sloped skirts.
func band(center, width, top, skirtDBPerMHz float64) emitter {
	return func(f float64) float64 {
		d := math.Abs(f-center) - width/2
		if d <= 0 {
			return top
		}
		return top - 6 - d*skirtDBPerMHz
	}
}

// wifi follows the 802.11 OFDM spectral mask: flat ±9 MHz with a rounded
// shoulder, -20 dB at ±11, -28 dB at ±20, -40 dB at ±30 (interpolated in dB).
func wifi(center, top float64) emitter {
	pts := [][2]float64{{0, 0}, {8, -0.5}, {9, -3}, {11, -20}, {20, -28}, {30, -40}, {60, -70}}
	return func(f float64) float64 {
		d := math.Abs(f - center)
		for i := 1; i < len(pts); i++ {
			if d <= pts[i][0] {
				a, b := pts[i-1], pts[i]
				return top + a[1] + (b[1]-a[1])*(d-a[0])/(b[0]-a[0])
			}
		}
		return top - 70
	}
}

// oqpsk approximates an 802.15.4 (2 Mchip/s half-sine O-QPSK) or BLE GFSK hump.
func oqpsk(center, top float64) emitter {
	return func(f float64) float64 {
		d := (f - center) / 0.9
		return top - 12*d*d
	}
}

func zigbeeMHz(ch int) float64 { return 2405 + 5*float64(ch-11) }

// scene returns what's on the air during one snapshot at time t (seconds).
func scene(t float64, rnd *rand.Rand) []emitter {
	var e []emitter
	burst := func(p float64) bool { return rnd.Float64() < p }
	// Wi-Fi: busy AP on 6, neighbors on 1 and 11. A ~200 us snapshot either
	// lands in a frame or doesn't.
	if burst(0.55) {
		e = append(e, wifi(2437, -52+rnd.Float64()*4))
	}
	if burst(0.3) {
		e = append(e, wifi(2412, -68+rnd.Float64()*4))
	}
	if burst(0.2) {
		e = append(e, wifi(2462, -63+rnd.Float64()*5))
	}
	// BLE: advertising on 37/38/39 plus a connection hopping its data channels.
	for _, adv := range []float64{2402, 2426, 2480} {
		if burst(0.08) {
			e = append(e, oqpsk(adv, -58))
		}
	}
	if burst(0.35) {
		e = append(e, oqpsk(2404+2*float64(rnd.Intn(37)), -64))
	}
	// Zigbee: your network and the Hue bridge, on their demo channels.
	if burst(0.18) {
		e = append(e, oqpsk(zigbeeMHz(HomeChan), -50))
	}
	if burst(0.12) {
		e = append(e, oqpsk(zigbeeMHz(HueChan), -61))
	}
	// Microwave oven: on for 25 s of every 90, magnetron sweeping 2450–2470
	// at mains rate, so roughly half the snapshots catch it.
	if math.Mod(t, 90) < 25 && burst(0.5) {
		c := 2452 + rnd.Float64()*14
		e = append(e, band(c, 6, -44, 3.5), band(2460, 22, -66, 1.2))
	}
	// A steady clock spur.
	e = append(e, oqpsk(2440, -78), func(f float64) float64 {
		if math.Abs(f-2440) < 0.16 {
			return -70
		}
		return -200
	})
	return e
}

func sdrSegment(run *sdrRun, sweep uint16, idx int, lo uint16, t float64, rnd *rand.Rand) *proto.SdrSegment {
	n := 1 << run.fftLog2
	const fs = 80.0
	step := fs / float64(n)
	codes := make([]byte, n)
	sigs := scene(t, rnd)
	for i := 0; i < n; i++ {
		f := float64(lo) - fs/2 + float64(i)*step
		// Noise floor: thermal + ADC, ~0.5 dB jitter after averaging 63 FFTs,
		// a few dB more for the peak detector.
		mw := math.Pow(10, (-94+rnd.NormFloat64()*0.4)/10)
		if run.peak {
			mw *= math.Pow(10, (4+rnd.Float64()*2)/10)
		}
		for _, sig := range sigs {
			mw += math.Pow(10, sig(f)/10)
		}
		db := 10 * math.Log10(mw)
		// Analog baseband filter: flat to ±16 MHz, then rolling off.
		if d := math.Abs(f-float64(lo)) - 16; d > 0 {
			db -= d * d * 0.12
		}
		code := math.Round((db + 128) * 2)
		codes[i] = byte(math.Max(0, math.Min(255, code)))
	}
	codes[n/2] = 0 // per-block mean removal zeroes DC
	flags := byte(0)
	if run.peak {
		flags |= proto.SdrSegPeak
	}
	return &proto.SdrSegment{
		RadioID: byte(run.radio), SweepID: sweep, SegIdx: byte(idx), SegCount: byte(len(run.los)),
		LoMHz: lo, FFTLog2: byte(run.fftLog2), SampleRateHz: 80000000, NFFTs: byte(16128 / n),
		Flags: flags, GainMin: 38, GainMax: 41 + byte(rnd.Intn(3)), TimestampMs: uint32(t * 1000),
		CodesPerDB: 2, FloorDBFS: -128, Codes: codes,
	}
}
