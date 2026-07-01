// Package decode implements IEEE 802.15.4 MAC + Zigbee NWK/APS/ZCL decoding,
// with optional AES-CCM* decryption. Port of host/zbsniff/decode/*.py.
package decode

import (
	"encoding/binary"
	"fmt"
)

const (
	addrNone     = 0
	addrShort    = 2
	addrExtended = 3
)

var frameTypes = []string{"Beacon", "Data", "Ack", "MAC Cmd", "?", "?", "?", "?"}

// MacFrame is a decoded 802.15.4 MAC header.
type MacFrame struct {
	FrameType byte
	TypeName  string
	Seq       int
	DstPan    int64
	DstAddr   int64 // -1 if absent
	DstMode   byte
	SrcPan    int64
	SrcAddr   int64 // -1 if absent
	SrcMode   byte
	PanComp   bool
	Payload   []byte
}

func fmtAddr(addr int64, mode byte) string {
	if addr < 0 {
		return "—"
	}
	if mode == addrShort {
		return fmt.Sprintf("0x%04x", uint16(addr))
	}
	return fmt.Sprintf("0x%016x", uint64(addr))
}

// Summary is a one-line description of the MAC frame.
func (m *MacFrame) Summary() string {
	s := fmt.Sprintf("%s seq=%d", m.TypeName, m.Seq)
	if m.SrcAddr >= 0 || m.DstAddr >= 0 {
		s += " " + fmtAddr(m.SrcAddr, m.SrcMode) + "→" + fmtAddr(m.DstAddr, m.DstMode)
	}
	return s
}

// DecodeMAC decodes a raw MPDU. Tolerant of truncation.
func DecodeMAC(mpdu []byte) *MacFrame {
	m := &MacFrame{DstAddr: -1, SrcAddr: -1, DstPan: -1, SrcPan: -1, Seq: -1}
	if len(mpdu) < 3 {
		m.TypeName = "(runt)"
		return m
	}
	fcf := uint16(mpdu[0]) | uint16(mpdu[1])<<8
	m.FrameType = byte(fcf & 0x7)
	m.TypeName = frameTypes[m.FrameType]
	m.PanComp = (fcf>>6)&1 == 1
	m.DstMode = byte((fcf >> 10) & 0x3)
	m.SrcMode = byte((fcf >> 14) & 0x3)
	o := 2
	m.Seq = int(mpdu[o])
	o++
	rd := func(n int) int64 {
		if o+n > len(mpdu) {
			return -1
		}
		v := int64(0)
		for i := 0; i < n; i++ {
			v |= int64(mpdu[o+i]) << (8 * i)
		}
		o += n
		return v
	}
	if m.DstMode == addrShort || m.DstMode == addrExtended {
		m.DstPan = rd(2)
		if m.DstMode == addrShort {
			m.DstAddr = rd(2)
		} else {
			m.DstAddr = rd(8)
		}
	}
	if m.SrcMode == addrShort || m.SrcMode == addrExtended {
		if m.PanComp {
			m.SrcPan = m.DstPan
		} else {
			m.SrcPan = rd(2)
		}
		if m.SrcMode == addrShort {
			m.SrcAddr = rd(2)
		} else {
			m.SrcAddr = rd(8)
		}
	}
	if len(mpdu)-o >= 2 {
		m.Payload = mpdu[o : len(mpdu)-2] // strip FCS
	}
	return m
}

// NwkFrame is a decoded Zigbee NWK frame.
type NwkFrame struct {
	FrameType    byte
	Version      int
	Secure       bool
	Dst          int
	Src          int
	Radius       int
	Seq          int
	Decrypted    bool
	CommandID    int    // NWK command frames only (-1 otherwise)
	StatusReason string // for "Network Status" command frames
	StatusDest   int    // for "Network Status": the unreachable destination (-1 if none)
	Payload      []byte
}

// TypeName names the NWK frame type / command.
func (n *NwkFrame) TypeName() string {
	if n.FrameType == 1 {
		if name, ok := nwkCommands[byte(n.CommandID)]; ok {
			if n.StatusReason != "" {
				return "NWK " + name + ": " + n.StatusReason
			}
			return "NWK " + name
		}
		return "NWK Command"
	}
	return "NWK Data"
}

// IsRouteFailure reports a NWK Network-Status route/link failure (dropout signal).
func (n *NwkFrame) IsRouteFailure() bool {
	return n.FrameType == 1 && n.CommandID == 0x03 && n.StatusReason != "" &&
		n.StatusReason != "PAN identifier update" && n.StatusReason != "Network address update"
}

// DecodeNWK parses the NWK header and, if secured and a key is given, decrypts.
func DecodeNWK(data []byte, networkKey []byte) *NwkFrame {
	if len(data) < 8 {
		return nil
	}
	fcf := uint16(data[0]) | uint16(data[1])<<8
	n := &NwkFrame{FrameType: byte(fcf & 0x3), Version: int((fcf >> 2) & 0xF), CommandID: -1}
	// Real Zigbee Pro uses NWK protocol version 2. Reject other versions so we
	// don't treat non-NWK MAC payloads (or green-power stub frames) as devices.
	if n.Version != 2 {
		return nil
	}
	multicast := (fcf>>8)&1 == 1
	n.Secure = (fcf>>9)&1 == 1
	srcRoute := (fcf>>10)&1 == 1
	dstIeee := (fcf>>11)&1 == 1
	srcIeee := (fcf>>12)&1 == 1
	o := 2
	n.Dst = int(binary.LittleEndian.Uint16(data[o:]))
	o += 2
	n.Src = int(binary.LittleEndian.Uint16(data[o:]))
	o += 2
	n.Radius = int(data[o])
	o++
	n.Seq = int(data[o])
	o++
	if dstIeee {
		o += 8
	}
	if srcIeee {
		o += 8
	}
	if multicast {
		o++
	}
	if srcRoute {
		if o >= len(data) {
			return n
		}
		relayCount := int(data[o])
		o += 2 // count + index
		o += relayCount * 2
	}
	if o > len(data) {
		return n
	}
	n.Payload = data[o:]
	if n.Secure {
		auxStart := o
		if o >= len(data) {
			return n
		}
		secControl := data[o]
		o++
		keyID := (secControl >> 3) & 0x3
		extNonce := (secControl>>5)&1 == 1
		if o+4 > len(data) {
			return n
		}
		frameCounter := binary.LittleEndian.Uint32(data[o:])
		o += 4
		var srcExt uint64
		haveExt := false
		if extNonce {
			if o+8 > len(data) {
				return n
			}
			srcExt = binary.LittleEndian.Uint64(data[o:])
			o += 8
			haveExt = true
		}
		if keyID == 1 {
			o++ // key seq number
		}
		auxEnd := o
		n.Payload = data[auxEnd:]
		if networkKey != nil && haveExt {
			realSC := RestoreSecLevel(secControl)
			aad := append([]byte(nil), data[:auxEnd]...)
			aad[auxStart] = realSC
			nonce := MakeNonce(srcExt, frameCounter, realSC)
			if pt, ok := DecryptCCM(networkKey, nonce, aad, data[auxEnd:], 4); ok {
				n.Payload = pt
				n.Decrypted = true
			}
		}
	}
	// NWK command frames: the command id is the first byte of the (decrypted)
	// payload. Network Status carries a route/link failure reason.
	n.StatusDest = -1
	if n.FrameType == 1 && (!n.Secure || n.Decrypted) && len(n.Payload) >= 1 {
		n.CommandID = int(n.Payload[0])
		if n.CommandID == 0x03 && len(n.Payload) >= 2 {
			n.StatusReason = nwkStatusCodes[n.Payload[1]]
			if len(n.Payload) >= 4 { // status code is followed by the unreachable dst addr
				n.StatusDest = int(n.Payload[2]) | int(n.Payload[3])<<8
			}
		}
	}
	return n
}

// ApsFrame is a decoded APS header (data frames).
type ApsFrame struct {
	FrameType byte
	Cluster   int
	Profile   int
	SrcEP     int
	DstEP     int
	Payload   []byte
}

// DecodeAPS parses an APS data frame.
func DecodeAPS(data []byte) *ApsFrame {
	if len(data) < 1 {
		return nil
	}
	fc := data[0]
	a := &ApsFrame{FrameType: fc & 0x3, Cluster: -1, Profile: -1, SrcEP: -1, DstEP: -1}
	delivery := (fc >> 2) & 0x3
	o := 1
	if a.FrameType == 0 { // data
		if delivery == 3 { // group
			o += 2
		} else if o < len(data) {
			a.DstEP = int(data[o])
			o++
		}
		if o+2 <= len(data) {
			a.Cluster = int(binary.LittleEndian.Uint16(data[o:]))
			o += 2
		}
		if o+2 <= len(data) {
			a.Profile = int(binary.LittleEndian.Uint16(data[o:]))
			o += 2
		}
		if o < len(data) {
			a.SrcEP = int(data[o])
			o++
		}
	}
	if o < len(data) {
		o++ // APS counter
	}
	if o <= len(data) {
		a.Payload = data[o:]
	}
	return a
}

var zclGlobalCmds = map[byte]string{
	0x00: "Read Attributes", 0x01: "Read Attributes Response",
	0x02: "Write Attributes", 0x0A: "Report Attributes", 0x0B: "Default Response",
}

// ZclFrame is a decoded ZCL header.
type ZclFrame struct {
	FrameType byte
	TSN       int
	CommandID int
}

// Summary describes the ZCL command.
func (z *ZclFrame) Summary() string {
	if z.FrameType == 0 {
		if name, ok := zclGlobalCmds[byte(z.CommandID)]; ok {
			return name
		}
		return fmt.Sprintf("global-cmd 0x%02x", z.CommandID)
	}
	return fmt.Sprintf("cluster-cmd 0x%02x", z.CommandID)
}

// DecodeZCL parses a ZCL header.
func DecodeZCL(data []byte) *ZclFrame {
	if len(data) < 3 {
		return nil
	}
	fc := data[0]
	z := &ZclFrame{FrameType: fc & 0x3}
	o := 1
	if (fc>>2)&1 == 1 {
		o += 2 // manufacturer code
	}
	if o+2 > len(data) {
		return nil
	}
	z.TSN = int(data[o])
	z.CommandID = int(data[o+1])
	return z
}

// Decoded bundles the layers of one frame.
type Decoded struct {
	MAC *MacFrame
	NWK *NwkFrame
	APS *ApsFrame
	ZCL *ZclFrame
}

// Summary is a one-line cross-layer description.
func (d *Decoded) Summary() string {
	s := d.MAC.Summary()
	if d.NWK != nil {
		s += " · " + d.NWK.TypeName() // includes command/route-status names
	}
	if d.APS != nil && d.APS.Cluster >= 0 {
		if cn := ClusterName(d.APS.Cluster); cn != "" {
			s += " · " + cn
		} else {
			s += fmt.Sprintf(" · cl=0x%04x", d.APS.Cluster)
		}
	}
	if d.ZCL != nil {
		s += " · " + d.zclSummary()
	}
	return s
}

func (d *Decoded) zclSummary() string {
	z := d.ZCL
	if z.FrameType == 0 { // global command
		if n, ok := zclGlobalNames[byte(z.CommandID)]; ok {
			return n
		}
		return fmt.Sprintf("global 0x%02x", z.CommandID)
	}
	if d.APS != nil { // cluster-specific
		if n, ok := zclClusterCmds[(d.APS.Cluster<<8)|z.CommandID]; ok {
			return n
		}
	}
	return fmt.Sprintf("cmd 0x%02x", z.CommandID)
}

// DecodeFrame walks MAC → NWK → APS → ZCL, decrypting if a key is supplied.
func DecodeFrame(mpdu []byte, networkKey []byte) *Decoded {
	mac := DecodeMAC(mpdu)
	d := &Decoded{MAC: mac}
	if mac.TypeName != "Data" || len(mac.Payload) == 0 {
		return d
	}
	d.NWK = DecodeNWK(mac.Payload, networkKey)
	if d.NWK == nil || (d.NWK.Secure && !d.NWK.Decrypted) {
		return d
	}
	d.APS = DecodeAPS(d.NWK.Payload)
	if d.APS != nil && d.APS.FrameType == 0 && len(d.APS.Payload) > 0 {
		d.ZCL = DecodeZCL(d.APS.Payload)
	}
	return d
}
