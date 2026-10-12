// Package proto implements the device<->host wire framing (v1).
// Port of host/zbsniff/proto.py and firmware/src/core/codec.c — keep in sync
// with /protocol/framing.md.
package proto

import (
	"encoding/binary"
	"encoding/json"
)

const (
	Magic0 = 0x5A
	Magic1 = 0xBE
	Ver    = 1

	MaxPayload = 2048
	ChannelMin = 11
	ChannelMax = 26
)

// Message types
const (
	MsgCapturedFrame = 0x01
	MsgEdResult      = 0x02
	MsgStatus        = 0x03
	MsgLog           = 0x04
	MsgAck           = 0x05
	MsgIncident      = 0x06
	MsgProbeResult   = 0x07
	MsgOtaStatus     = 0x08
	MsgSdrSpectrum   = 0x09

	CmdSetChannel = 0x81
	CmdSetMode    = 0x82
	CmdStart      = 0x83
	CmdStop       = 0x84
	CmdSetHop     = 0x85
	CmdEdScan     = 0x86
	CmdSetKey     = 0x87
	CmdGetStatus  = 0x88
	CmdSetRadioID = 0x89
	CmdProbe      = 0x8A
	CmdOtaBegin   = 0x8B
	CmdOtaData    = 0x8C
	CmdOtaEnd     = 0x8D
	CmdOtaAbort   = 0x8E
	CmdBeaconReq  = 0x8F
	CmdTxRaw      = 0x90

	// Relayed satellite control (target(1) + inner args, stripped and forwarded
	// over SPI by the primary) — see docs on CMD_SET_CHANNEL/CMD_START/CMD_STOP.
	CmdSatSetChannel = 0x91
	CmdSatStart      = 0x92
	CmdSatStop       = 0x93
	CmdSatRelay      = 0x94
	CmdSdrConfig     = 0x95
)

// OTA states (MsgOtaStatus.State).
const (
	OtaIdle = iota
	OtaReceiving
	OtaWriting
	OtaVerifying
	OtaOK
	OtaError
)

// Capture modes
const (
	ModeIdle          = 0
	ModeCapture       = 1
	ModeEdSweep       = 2
	ModeCapturePlusEd = 3
	ModeSdrSpectrum   = 4 // wideband raw-I/Q sweep (802.15.4 suspended meanwhile)
)

// STATUS capability bits (Status.Caps).
const (
	CapSDR = 0x01 // radio can run ModeSdrSpectrum
)

// SDR detectors (CmdSdrConfigMsg detector).
const (
	SdrDetMean = 0
	SdrDetPeak = 1
)

// SdrSegment flags.
const (
	SdrSegPeak    = 0x01 // codes are peak-hold, not mean
	SdrSegFailed  = 0x02 // tune/capture failed; codes are meaningless
	SdrSegClipped = 0x04 // ADC hit full scale
	SdrSegDropped = 0x08 // an earlier segment was lost in transport
)

const sdrHdrLen = 22

// CAPTURED_FRAME flags
const (
	FlagCRCOK       = 0x01
	FlagPromiscuous = 0x02
	FlagFramePend   = 0x04
	FlagWasAcked    = 0x08
)

// CRC16 computes CRC-16/CCITT-FALSE (poly 0x1021, init 0xFFFF).
func CRC16(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// Encode builds a complete framed message.
func Encode(msgType byte, payload []byte) []byte {
	body := make([]byte, 4+len(payload))
	body[0] = Ver
	body[1] = msgType
	binary.LittleEndian.PutUint16(body[2:], uint16(len(payload)))
	copy(body[4:], payload)
	crc := CRC16(body)
	out := make([]byte, 2+len(body)+2)
	out[0] = Magic0
	out[1] = Magic1
	copy(out[2:], body)
	binary.LittleEndian.PutUint16(out[2+len(body):], crc)
	return out
}

// CmdSdrConfigMsg sets the wideband sweep: LO list (MHz), FFT size (log2),
// FFT blocks averaged per LO (0 = all that fit), detector and minimum sweep
// interval. Applies from the radio's next sweep.
func CmdSdrConfigMsg(loMHz []uint16, fftLog2, nAvg, detector byte, intervalMs uint16) []byte {
	p := make([]byte, 0, 1+2*len(loMHz)+5)
	p = append(p, byte(len(loMHz)))
	for _, f := range loMHz {
		p = binary.LittleEndian.AppendUint16(p, f)
	}
	p = append(p, fftLog2, nAvg, detector)
	p = binary.LittleEndian.AppendUint16(p, intervalMs)
	return Encode(CmdSdrConfig, p)
}

// Command builders
func CmdSetChannelMsg(ch byte) []byte { return Encode(CmdSetChannel, []byte{ch}) }
func CmdSetModeMsg(m byte) []byte     { return Encode(CmdSetMode, []byte{m}) }
func CmdStartMsg() []byte             { return Encode(CmdStart, nil) }
func CmdStopMsg() []byte              { return Encode(CmdStop, nil) }
func CmdGetStatusMsg() []byte         { return Encode(CmdGetStatus, nil) }

// AllChannelsMask returns the bitmask of channels 11..26 (bit i = channel 11+i).
func AllChannelsMask() uint32 {
	var m uint32
	for c := ChannelMin; c <= ChannelMax; c++ {
		m |= 1 << uint(c-ChannelMin)
	}
	return m
}

// CmdEdScanMsg requests a one-shot energy-detect sweep of hopMask (dwell ms/channel).
func CmdEdScanMsg(hopMask uint32, dwellMs uint16) []byte {
	p := make([]byte, 6)
	binary.LittleEndian.PutUint32(p[0:], hopMask)
	binary.LittleEndian.PutUint16(p[4:], dwellMs)
	return Encode(CmdEdScan, p)
}

// CmdSetHopMsg configures channel hopping (dwellMs=0 disables / pins the channel).
func CmdSetHopMsg(hopMask uint32, dwellMs uint16) []byte {
	p := make([]byte, 6)
	binary.LittleEndian.PutUint32(p[0:], hopMask)
	binary.LittleEndian.PutUint16(p[4:], dwellMs)
	return Encode(CmdSetHop, p)
}

// CmdSetRadioIDMsg provisions a dongle's radio-id (persisted to its NVS).
func CmdSetRadioIDMsg(id byte) []byte { return Encode(CmdSetRadioID, []byte{id}) }

// OTA command builders. target 0 = the tethered C6, 1..3 = a satellite.
func CmdOtaBeginMsg(target byte, total, crc32 uint32) []byte {
	p := make([]byte, 9)
	p[0] = target
	binary.LittleEndian.PutUint32(p[1:], total)
	binary.LittleEndian.PutUint32(p[5:], crc32)
	return Encode(CmdOtaBegin, p)
}

// CmdOtaDataMsg's wire format is deliberately NOT versioned/extended lightly:
// this message is parsed by whatever firmware is CURRENTLY RUNNING on the
// target, which is exactly the firmware an OTA update is trying to replace.
// A field added here only helps once new firmware understanding it is
// already installed — but the only way to install it is a transfer using
// THIS message, parsed by the OLD firmware. Changing the format silently
// breaks that transfer for every device still running anything older,
// with no way to recover except a physical reflash. (Learned the hard way:
// an added content-checksum field shifted every chunk's data by 4 bytes as
// seen by old firmware, corrupting the image from byte zero — every
// subsequent failure looked like a deeper bug until this was traced back.)
func CmdOtaDataMsg(target byte, offset uint32, chunk []byte) []byte {
	p := make([]byte, 5+len(chunk))
	p[0] = target
	binary.LittleEndian.PutUint32(p[1:], offset)
	copy(p[5:], chunk)
	return Encode(CmdOtaData, p)
}
func CmdOtaEndMsg(target byte) []byte   { return Encode(CmdOtaEnd, []byte{target}) }
func CmdOtaAbortMsg(target byte) []byte { return Encode(CmdOtaAbort, []byte{target}) }

// Relayed satellite control builders. target is the satellite's radio-id (1..3).
func CmdSatSetChannelMsg(target, ch byte) []byte { return Encode(CmdSatSetChannel, []byte{target, ch}) }
func CmdSatStartMsg(target byte) []byte          { return Encode(CmdSatStart, []byte{target}) }
func CmdSatStopMsg(target byte) []byte           { return Encode(CmdSatStop, []byte{target}) }

// CmdSatRelayMsg wraps a fully-encoded inner command frame so the primary
// forwards it verbatim over SPI to satellite `target` (1..3). This is how any
// host->device command (mode, ED, hop, probe, beacon, raw TX) reaches a
// satellite — see CMD_SAT_RELAY in proto.h.
func CmdSatRelayMsg(target byte, inner []byte) []byte {
	return Encode(CmdSatRelay, append([]byte{target}, inner...))
}

// CmdBeaconReqMsg asks the radio to transmit an 802.15.4 beacon request on the
// current channel; coordinators/routers reply with a beacon revealing their PAN.
func CmdBeaconReqMsg() []byte { return Encode(CmdBeaconReq, nil) }

// CmdTxRawMsg transmits a host-built raw MPDU (the radio appends the FCS).
func CmdTxRawMsg(mpdu []byte) []byte { return Encode(CmdTxRaw, mpdu) }

// CmdProbeMsg requests an active MAC probe of target (short addr) on pan.
func CmdProbeMsg(target, pan uint16) []byte {
	p := make([]byte, 4)
	binary.LittleEndian.PutUint16(p[0:], target)
	binary.LittleEndian.PutUint16(p[2:], pan)
	return Encode(CmdProbe, p)
}

func CmdSetKeyMsg(key []byte) []byte {
	if len(key) != 16 {
		return Encode(CmdSetKey, []byte{0})
	}
	return Encode(CmdSetKey, append([]byte{1}, key...))
}

// Parsed message payloads
type CapturedFrame struct {
	RadioID   byte
	Channel   byte
	RSSI      int8
	LQI       byte
	Flags     byte
	Timestamp uint64
	MPDU      []byte
}

type EdResult struct {
	RadioID byte
	Channel byte
	EdDBm   int8
	SweepID uint16
}

type Status struct {
	RadioID    byte
	Mode       byte
	Channel    byte
	HopMask    uint32
	HopDwellMs uint16
	Captured   uint32
	DroppedBuf uint32
	UptimeS    uint32
	FWMajor    byte
	FWMinor    byte
	Caps       byte // Cap* bits; 0 from firmware older than the field
}

// SdrSegment is one LO's slice of a wideband sweep: Codes[i] is the power of
// the bin at LoMHz - SampleRate/2 + i*SampleRate/len(Codes), in dBFS =
// Codes[i]/CodesPerDB + FloorDBFS.
type SdrSegment struct {
	RadioID      byte
	SweepID      uint16
	SegIdx       byte
	SegCount     byte
	LoMHz        uint16
	FFTLog2      byte
	SampleRateHz uint32
	NFFTs        byte
	Flags        byte
	GainMin      byte
	GainMax      byte
	TimestampMs  uint32
	CodesPerDB   byte
	FloorDBFS    int8
	Codes        []byte
}

type Incident struct {
	Raw  string
	Data map[string]any
}

// ProbeResult is the outcome of an active MAC probe.
type ProbeResult struct {
	RadioID byte
	Target  uint16
	Acked   bool
	RSSI    int8
	LQI     byte
}

// OtaStatus is an OTA progress report.
type OtaStatus struct {
	Target   byte
	State    byte
	Received uint32
	Total    uint32
	Err      byte
}

// ParsePayload decodes a payload by message type. Returns nil for unknown types.
func ParsePayload(msgType byte, p []byte) any {
	switch msgType {
	case MsgCapturedFrame:
		if len(p) < 14 {
			return nil
		}
		mlen := int(p[13])
		if 14+mlen > len(p) {
			return nil
		}
		return &CapturedFrame{
			RadioID:   p[0],
			Channel:   p[1],
			RSSI:      int8(p[2]),
			LQI:       p[3],
			Flags:     p[4],
			Timestamp: binary.LittleEndian.Uint64(p[5:13]),
			MPDU:      append([]byte(nil), p[14:14+mlen]...),
		}
	case MsgEdResult:
		if len(p) < 13 {
			return nil
		}
		return &EdResult{
			RadioID: p[0], Channel: p[1],
			EdDBm:   int8(p[10]),
			SweepID: binary.LittleEndian.Uint16(p[11:13]),
		}
	case MsgStatus:
		if len(p) < 27 {
			return nil
		}
		return &Status{
			RadioID:    p[0],
			Mode:       p[1],
			Channel:    p[2],
			HopMask:    binary.LittleEndian.Uint32(p[3:7]),
			HopDwellMs: binary.LittleEndian.Uint16(p[7:9]),
			UptimeS:    binary.LittleEndian.Uint32(p[9:13]),
			Captured:   binary.LittleEndian.Uint32(p[13:17]),
			DroppedBuf: binary.LittleEndian.Uint32(p[21:25]),
			FWMajor:    p[25],
			FWMinor:    p[26],
			Caps:       capsByte(p),
		}
	case MsgSdrSpectrum:
		if len(p) < sdrHdrLen {
			return nil
		}
		n := 1 << p[7]
		if p[7] > 12 || len(p) < sdrHdrLen+n || p[20] == 0 {
			return nil
		}
		return &SdrSegment{
			RadioID:      p[0],
			SweepID:      binary.LittleEndian.Uint16(p[1:3]),
			SegIdx:       p[3],
			SegCount:     p[4],
			LoMHz:        binary.LittleEndian.Uint16(p[5:7]),
			FFTLog2:      p[7],
			SampleRateHz: binary.LittleEndian.Uint32(p[8:12]),
			NFFTs:        p[12],
			Flags:        p[13],
			GainMin:      p[14],
			GainMax:      p[15],
			TimestampMs:  binary.LittleEndian.Uint32(p[16:20]),
			CodesPerDB:   p[20],
			FloorDBFS:    int8(p[21]),
			Codes:        append([]byte(nil), p[sdrHdrLen:sdrHdrLen+n]...),
		}
	case MsgIncident:
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		return &Incident{Raw: string(p), Data: m}
	case MsgProbeResult:
		if len(p) < 6 {
			return nil
		}
		return &ProbeResult{
			RadioID: p[0],
			Target:  binary.LittleEndian.Uint16(p[1:3]),
			Acked:   p[3] != 0,
			RSSI:    int8(p[4]),
			LQI:     p[5],
		}
	case MsgOtaStatus:
		if len(p) < 11 {
			return nil
		}
		return &OtaStatus{
			Target:   p[0],
			State:    p[1],
			Received: binary.LittleEndian.Uint32(p[2:6]),
			Total:    binary.LittleEndian.Uint32(p[6:10]),
			Err:      p[10],
		}
	}
	return nil
}

func capsByte(p []byte) byte {
	if len(p) >= 28 {
		return p[27]
	}
	return 0
}

// EncodeSdrSegment is the inverse of ParsePayload for MsgSdrSpectrum (used by
// demo mode and tests).
func EncodeSdrSegment(s *SdrSegment) []byte {
	p := make([]byte, sdrHdrLen, sdrHdrLen+len(s.Codes))
	p[0] = s.RadioID
	binary.LittleEndian.PutUint16(p[1:], s.SweepID)
	p[3], p[4] = s.SegIdx, s.SegCount
	binary.LittleEndian.PutUint16(p[5:], s.LoMHz)
	p[7] = s.FFTLog2
	binary.LittleEndian.PutUint32(p[8:], s.SampleRateHz)
	p[12], p[13], p[14], p[15] = s.NFFTs, s.Flags, s.GainMin, s.GainMax
	binary.LittleEndian.PutUint32(p[16:], s.TimestampMs)
	p[20], p[21] = s.CodesPerDB, byte(s.FloorDBFS)
	return Encode(MsgSdrSpectrum, append(p, s.Codes...))
}

// StreamDecoder reassembles framed messages from a byte stream.
type StreamDecoder struct {
	buf []byte
}

// Frame is a decoded (type, payload) pair.
type Frame struct {
	Type    byte
	Payload []byte
}

// Feed appends data and returns any complete, CRC-valid frames.
func (d *StreamDecoder) Feed(data []byte) []Frame {
	d.buf = append(d.buf, data...)
	var out []Frame
	i := 0
	for {
		// find magic
		for i+1 < len(d.buf) && !(d.buf[i] == Magic0 && d.buf[i+1] == Magic1) {
			i++
		}
		if i+6 > len(d.buf) {
			break
		}
		if d.buf[i+2] != Ver {
			i++
			continue
		}
		plen := int(binary.LittleEndian.Uint16(d.buf[i+4:]))
		if plen > MaxPayload {
			i++
			continue
		}
		total := 6 + plen + 2
		if i+total > len(d.buf) {
			break
		}
		crcRx := binary.LittleEndian.Uint16(d.buf[i+6+plen:])
		if CRC16(d.buf[i+2:i+6+plen]) == crcRx {
			payload := append([]byte(nil), d.buf[i+6:i+6+plen]...)
			out = append(out, Frame{Type: d.buf[i+3], Payload: payload})
			i += total
		} else {
			i++
		}
	}
	d.buf = append([]byte(nil), d.buf[i:]...)
	return out
}
