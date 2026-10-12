package sdr

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"zbsniff/internal/proto"
)

// seg builds a 256-bin, 80 MS/s segment whose bins all read `db` dBFS, except
// DC (which mean removal zeroes on hardware) and an optional tone bin.
func seg(radio byte, sweep uint16, idx, count byte, lo uint16, db float64, toneMHz, toneDB float64) *proto.SdrSegment {
	const n = 256
	codes := make([]byte, n)
	for i := range codes {
		codes[i] = byte((db + 128) * 2)
	}
	codes[n/2] = 0
	if toneMHz != 0 {
		bin := int(math.Round((toneMHz - (float64(lo) - 40)) / (80.0 / n)))
		if bin >= 0 && bin < n {
			codes[bin] = byte((toneDB + 128) * 2)
		}
	}
	return &proto.SdrSegment{RadioID: radio, SweepID: sweep, SegIdx: idx, SegCount: count, LoMHz: lo,
		FFTLog2: 8, SampleRateHz: 80000000, NFFTs: 63, CodesPerDB: 2, FloorDBFS: -128, Codes: codes}
}

func at(t *Trace, mhz float64) float64 {
	return float64(t.DB[int(math.Round((mhz-t.F0MHz)/t.StepMHz))])
}

func TestStitchDefaultPlan(t *testing.T) {
	s := NewStore()
	los := []uint16{2412, 2427, 2442, 2457, 2472}
	var out []*Trace
	for i, lo := range los {
		// Each segment reads a different floor so we can tell who supplied a bin;
		// a tone at 2425 MHz is visible in segments 0 and 1.
		out = append(out, s.Add(seg(0, 7, byte(i), byte(len(los)), lo, -90+float64(i), 2425, -40))...)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 trace, got %d", len(out))
	}
	tr := out[0]
	if tr.StepMHz != 0.3125 || tr.Missing != 0 || tr.Failed != 0 {
		t.Fatalf("bad trace meta: %+v", tr)
	}
	if lo, hi := tr.F0MHz, tr.F0MHz+float64(len(tr.DB)-1)*tr.StepMHz; lo > 2400 || hi < 2483.5 {
		t.Fatalf("trace %.1f..%.1f MHz doesn't cover the ISM band", lo, hi)
	}
	// 2425 is nearest LO 2427 → segment 1's tone.
	if v := at(tr, 2425); v != -40 {
		t.Errorf("tone at 2425: got %.1f want -40", v)
	}
	// 2419 is nearest 2412 (7 MHz) vs 2427 (8 MHz) → segment 0's floor.
	if v := at(tr, 2419); v != -90 {
		t.Errorf("2419 MHz: got %.1f want -90 (segment 0)", v)
	}
	// The LO itself is the DC notch: interpolated from its neighbors, never 0-code.
	if v := at(tr, 2442); v != -88 {
		t.Errorf("DC at 2442: got %.1f want -88 (bridged)", v)
	}
	for i, v := range tr.DB {
		if math.IsNaN(float64(v)) {
			t.Fatalf("unexpected gap at %.4f MHz", tr.F0MHz+float64(i)*tr.StepMHz)
		}
	}
}

func TestNewSweepFlushesPartial(t *testing.T) {
	s := NewStore()
	if got := s.Add(seg(1, 1, 0, 3, 2412, -90, 0, 0)); len(got) != 0 {
		t.Fatal("trace completed too early")
	}
	got := s.Add(seg(1, 2, 0, 3, 2412, -90, 0, 0))
	if len(got) != 1 || got[0].Sweep != 1 || got[0].Missing != 2 || !got[0].Dropped {
		t.Fatalf("partial sweep not flushed as dropped: %+v", got)
	}
	if h := s.History(1, 0); len(h) != 1 {
		t.Fatalf("history len %d", len(h))
	}
}

func TestFailedSegmentLeavesGap(t *testing.T) {
	s := NewStore()
	s.Add(seg(0, 3, 0, 3, 2412, -90, 0, 0))
	bad := seg(0, 3, 1, 3, 2442, -90, 0, 0)
	bad.Flags |= proto.SdrSegFailed
	s.Add(bad)
	tr := s.Add(seg(0, 3, 2, 3, 2472, -90, 0, 0))[0]
	if tr.Failed != 1 || !math.IsNaN(at(tr, 2442)) {
		t.Fatalf("failed segment should leave a gap at 2442: failed=%d v=%v", tr.Failed, at(tr, 2442))
	}
	b, err := json.Marshal(tr)
	if err != nil || !strings.Contains(string(b), "null") {
		t.Fatalf("NaN should marshal as null: %v %s", err, b)
	}
}

func TestProtoRoundTrip(t *testing.T) {
	in := seg(2, 9, 1, 5, 2427, -77.5, 0, 0)
	in.Flags = proto.SdrSegClipped
	in.GainMin, in.GainMax, in.TimestampMs = 3, 40, 123456
	frames := (&proto.StreamDecoder{}).Feed(proto.EncodeSdrSegment(in))
	if len(frames) != 1 || frames[0].Type != proto.MsgSdrSpectrum {
		t.Fatalf("decode: %+v", frames)
	}
	out, ok := proto.ParsePayload(frames[0].Type, frames[0].Payload).(*proto.SdrSegment)
	if !ok || out.LoMHz != 2427 || out.SweepID != 9 || out.GainMax != 40 || out.FloorDBFS != -128 ||
		out.TimestampMs != 123456 || len(out.Codes) != 256 || out.Codes[10] != in.Codes[10] {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}
