// Package sdr stitches wideband SDR spectrum segments (one per LO, see
// proto.SdrSegment) into full-band traces and keeps a short history per radio
// for the waterfall.
//
// Each segment spans the full complex sample rate (80 MHz on the ESP32-C6)
// around its LO, but only the middle is trustworthy: the analog filter rolls
// off toward the edges and the center bins hold the removed DC term. For every
// output frequency the stitcher picks the segment whose LO is nearest (so the
// bin sits as close to the flat middle as possible), skips the DC bins, and
// bridges the resulting short gaps by linear interpolation.
package sdr

import (
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"zbsniff/internal/proto"
)

const (
	// DefaultUsableHalfMHz is how far from its LO a segment's bins are used.
	DefaultUsableHalfMHz = 20.0
	// dcBins is how many bins either side of the LO are treated as the DC notch.
	dcBins = 1
	// maxGapFill bounds interpolation: wider holes stay missing (null).
	maxGapFill = 6
	// HistoryLen is how many stitched traces are kept per radio.
	HistoryLen = 600
)

// Levels is a trace's dBFS values; NaN marks a missing point and marshals as
// null with one decimal of precision (keeps the WebSocket payload small).
type Levels []float32

// MarshalJSON implements json.Marshaler.
func (l Levels) MarshalJSON() ([]byte, error) {
	b := make([]byte, 0, len(l)*6+2)
	b = append(b, '[')
	for i, v := range l {
		if i > 0 {
			b = append(b, ',')
		}
		if math.IsNaN(float64(v)) {
			b = append(b, "null"...)
			continue
		}
		b = strconv.AppendFloat(b, float64(v), 'f', 1, 32)
	}
	return append(b, ']'), nil
}

// Trace is one stitched sweep.
type Trace struct {
	Radio    int       `json:"radio"`
	Sweep    uint16    `json:"sweep"`
	TS       float64   `json:"ts"`       // host receive time, unix seconds
	F0MHz    float64   `json:"f0"`       // frequency of DB[0]
	StepMHz  float64   `json:"df"`       // spacing of DB
	DB       Levels    `json:"db"`       // dBFS per point
	LOs      []float64 `json:"los"`      // LO of each segment, in sweep order
	Peak     bool      `json:"peak"`     // peak-hold detector, else mean
	Clipped  bool      `json:"clipped"`  // ADC saturated somewhere in the sweep
	Dropped  bool      `json:"dropped"`  // segments lost in transport
	Failed   int       `json:"failed"`   // segments whose capture failed
	Missing  int       `json:"missing"`  // segments never received
	NFFTs    int       `json:"n_ffts"`   // FFT blocks averaged per segment
	GainMin  int       `json:"gain_min"` // RX gain index range seen
	GainMax  int       `json:"gain_max"`
	Duration float64   `json:"dur_ms"` // device time from first to last segment
}

type pending struct {
	sweep uint16
	segs  map[byte]*proto.SdrSegment
	count byte
	first time.Time
}

// Store assembles segments into traces and remembers recent ones.
type Store struct {
	mu         sync.Mutex
	usableHalf float64
	pend       map[int]*pending
	hist       map[int][]*Trace
	now        func() time.Time
}

// NewStore creates an empty store.
func NewStore() *Store {
	return &Store{usableHalf: DefaultUsableHalfMHz, pend: map[int]*pending{},
		hist: map[int][]*Trace{}, now: time.Now}
}

// SetUsableHalf sets how far (MHz) from its LO a segment's bins are trusted.
func (s *Store) SetUsableHalf(mhz float64) {
	if mhz <= 0 {
		return
	}
	s.mu.Lock()
	s.usableHalf = mhz
	s.mu.Unlock()
}

// Add ingests one segment. It returns every trace completed by it: normally
// zero or one, but a segment from a new sweep also flushes the radio's
// previous, incomplete one (its missing segments show as gaps).
func (s *Store) Add(seg *proto.SdrSegment) []*Trace {
	radio := int(seg.RadioID &^ 0x80) // RADIO_ID_COLLISION_BIT
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Trace
	p := s.pend[radio]
	if p != nil && p.sweep != seg.SweepID {
		out = append(out, s.finish(radio, p))
		p = nil
	}
	if p == nil {
		p = &pending{sweep: seg.SweepID, segs: map[byte]*proto.SdrSegment{}, count: seg.SegCount, first: s.now()}
		s.pend[radio] = p
	}
	p.segs[seg.SegIdx] = seg
	if seg.SegCount > 0 && len(p.segs) >= int(p.count) {
		out = append(out, s.finish(radio, p))
		delete(s.pend, radio)
	}
	return out
}

// finish stitches p and appends it to the radio's history. Caller holds mu.
func (s *Store) finish(radio int, p *pending) *Trace {
	t := stitch(radio, p, s.usableHalf)
	t.TS = float64(p.first.UnixNano()) / 1e9
	h := append(s.hist[radio], t)
	if len(h) > HistoryLen {
		h = h[len(h)-HistoryLen:]
	}
	s.hist[radio] = h
	return t
}

// History returns up to n of the radio's most recent traces, oldest first.
func (s *Store) History(radio, n int) []*Trace {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.hist[radio]
	if n > 0 && len(h) > n {
		h = h[len(h)-n:]
	}
	return append([]*Trace(nil), h...)
}

// Radios lists radios with stored traces.
func (s *Store) Radios() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]int, 0, len(s.hist))
	for id := range s.hist {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

// Clear drops all history (pending sweeps too).
func (s *Store) Clear() {
	s.mu.Lock()
	s.pend = map[int]*pending{}
	s.hist = map[int][]*Trace{}
	s.mu.Unlock()
}

func stitch(radio int, p *pending, usableHalf float64) *Trace {
	t := &Trace{Radio: radio, Sweep: p.sweep, GainMin: 255}
	idx := make([]int, 0, len(p.segs))
	for i := range p.segs {
		idx = append(idx, int(i))
	}
	sort.Ints(idx)

	var ref *proto.SdrSegment
	var tMin, tMax uint32
	for k, i := range idx {
		g := p.segs[byte(i)]
		t.LOs = append(t.LOs, float64(g.LoMHz))
		t.Peak = t.Peak || g.Flags&proto.SdrSegPeak != 0
		t.Clipped = t.Clipped || g.Flags&proto.SdrSegClipped != 0
		t.Dropped = t.Dropped || g.Flags&proto.SdrSegDropped != 0
		if g.Flags&proto.SdrSegFailed != 0 {
			t.Failed++
			continue
		}
		if int(g.GainMin) < t.GainMin {
			t.GainMin = int(g.GainMin)
		}
		if int(g.GainMax) > t.GainMax {
			t.GainMax = int(g.GainMax)
		}
		if k == 0 || g.TimestampMs < tMin {
			tMin = g.TimestampMs
		}
		if g.TimestampMs > tMax {
			tMax = g.TimestampMs
		}
		if ref == nil {
			ref = g
			t.NFFTs = int(g.NFFTs)
		}
	}
	if int(p.count) > len(p.segs) {
		t.Missing = int(p.count) - len(p.segs)
		t.Dropped = true
	}
	if t.GainMin > t.GainMax {
		t.GainMin = 0
	}
	t.Duration = float64(tMax - tMin)
	if ref == nil {
		return t
	}

	n := len(ref.Codes)
	fs := float64(ref.SampleRateHz) / 1e6
	step := fs / float64(n)
	lo, hi := math.Inf(1), math.Inf(-1)
	usable := make([]*proto.SdrSegment, 0, len(p.segs))
	for _, i := range idx {
		g := p.segs[byte(i)]
		// Segments must share the bin grid to be stitched together.
		if g.Flags&proto.SdrSegFailed != 0 || len(g.Codes) != n || g.SampleRateHz != ref.SampleRateHz {
			continue
		}
		usable = append(usable, g)
		lo = math.Min(lo, float64(g.LoMHz)-usableHalf)
		hi = math.Max(hi, float64(g.LoMHz)+usableHalf)
	}
	half := math.Min(usableHalf, fs/2-step)
	// Align the grid to the reference segment's bin centers.
	refStart := float64(ref.LoMHz) - fs/2
	j0 := math.Ceil((lo - refStart) / step)
	j1 := math.Floor((hi - refStart) / step)
	t.F0MHz = refStart + j0*step
	t.StepMHz = step
	pts := int(j1-j0) + 1
	if pts <= 0 {
		return t
	}
	t.DB = make(Levels, pts)
	for j := 0; j < pts; j++ {
		f := t.F0MHz + float64(j)*step
		// The nearest LO always wins, even when f lands in its DC notch: that
		// short hole is bridged below rather than borrowing a bin from a
		// neighbor whose LO is far enough away to sit on its filter skirt.
		best, bestDist := -1, math.Inf(1)
		var bestSeg *proto.SdrSegment
		for _, g := range usable {
			d := math.Abs(f - float64(g.LoMHz))
			if d > half+1e-9 || d >= bestDist {
				continue
			}
			bin := int(math.Round((f - (float64(g.LoMHz) - fs/2)) / step))
			if bin < 0 || bin >= n {
				continue
			}
			best, bestDist, bestSeg = bin, d, g
		}
		if bestSeg == nil || abs(best-n/2) <= dcBins {
			t.DB[j] = float32(math.NaN())
			continue
		}
		t.DB[j] = float32(float64(bestSeg.Codes[best])/float64(bestSeg.CodesPerDB) + float64(bestSeg.FloorDBFS))
	}
	fillGaps(t.DB)
	return t
}

// fillGaps linearly bridges NaN runs of at most maxGapFill points that have a
// value on both sides (the DC notches).
func fillGaps(v Levels) {
	for i := 0; i < len(v); {
		if !math.IsNaN(float64(v[i])) {
			i++
			continue
		}
		j := i
		for j < len(v) && math.IsNaN(float64(v[j])) {
			j++
		}
		if i > 0 && j < len(v) && j-i <= maxGapFill {
			a, b := v[i-1], v[j]
			for k := i; k < j; k++ {
				frac := float32(k-i+1) / float32(j-i+1)
				v[k] = a + (b-a)*frac
			}
		}
		i = j
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
