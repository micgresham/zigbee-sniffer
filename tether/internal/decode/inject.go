package decode

// Frame injection: build a fully-formed, NWK-secured MAC frame for a Zigbee
// Device Object (ZDO) request. This is the inverse of the decode path, so a
// built frame round-trips back through DecodeFrame (see inject_test.go) —
// proving the encryption + framing are correct without needing hardware.
//
// SAFETY: frames from this are TRANSMITTED on a live network. Gate behind an
// explicit user action. Whether a device accepts one depends on its frame-counter
// and routing state, which can only be confirmed on-air.

// Common ZDO cluster ids.
const (
	ZDONodeDescReq = 0x0002
	ZDOActiveEPReq = 0x0005
	ZDOMgmtBindReq = 0x0033 // binding table
	ZDOMgmtLqiReq  = 0x0031 // neighbour table
	ZDOMgmtRtgReq  = 0x0032 // routing table
)

// ZDORequest describes a ZDO query to build.
type ZDORequest struct {
	TargetShort  uint16 // NWK destination (the device being queried)
	MacDst       uint16 // MAC destination (next hop; = TargetShort for a neighbour)
	SrcShort     uint16 // our (spoofed) short address
	SrcExt       uint64 // our (spoofed) extended address — the CCM* nonce source
	DstPan       uint16
	Cluster      uint16 // ZDP cluster (see ZDO*Req)
	ZDPArgs      []byte // command args after the transaction sequence number
	FrameCounter uint32
	MacSeq       byte
	NwkSeq       byte
	ApsSeq       byte
	Tsn          byte // ZDP transaction sequence number
	Key          []byte
}

func le16(b []byte, v uint16) []byte { return append(b, byte(v), byte(v>>8)) }
func le32(b []byte, v uint32) []byte {
	return append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}
func le64(b []byte, v uint64) []byte {
	return append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24),
		byte(v>>32), byte(v>>40), byte(v>>48), byte(v>>56))
}

// BuildZDORequest assembles a complete NWK-secured MAC frame ready to transmit
// (the radio appends the PHY FCS). Returns nil on error.
func BuildZDORequest(r ZDORequest) []byte {
	if len(r.Key) != 16 {
		return nil
	}
	// APS frame (the plaintext NWK payload): data, unicast, to endpoint 0 (ZDO).
	aps := []byte{0x00, 0x00} // FC (data/unicast), dst EP 0
	aps = le16(aps, r.Cluster)
	aps = le16(aps, 0x0000) // profile: Zigbee Device Profile
	aps = append(aps, 0x00) // src EP 0
	aps = append(aps, r.ApsSeq)
	aps = append(aps, r.Tsn)
	aps = append(aps, r.ZDPArgs...)

	// NWK header: data, protocol version 2, secured.
	nwk := le16(nil, 0x0208)
	nwk = le16(nwk, r.TargetShort)
	nwk = le16(nwk, r.SrcShort)
	nwk = append(nwk, 30) // radius
	nwk = append(nwk, r.NwkSeq)

	// NWK auxiliary security header: level 0 on the wire, key id 1 (network key),
	// extended nonce present.
	const scTx = 0x28
	aux := []byte{scTx}
	aux = le32(aux, r.FrameCounter)
	aux = le64(aux, r.SrcExt)
	aux = append(aux, 0x00) // key sequence number
	realSC := RestoreSecLevel(scTx)

	// AAD = nwk header + aux header, but with the *real* security level restored
	// in the security-control byte (Zigbee transmits it zeroed).
	aad := append(append([]byte(nil), nwk...), aux...)
	aad[len(nwk)] = realSC
	nonce := MakeNonce(r.SrcExt, r.FrameCounter, realSC)
	ctMic, ok := EncryptCCM(r.Key, nonce, aad, aps, 4)
	if !ok {
		return nil
	}
	nwkFrame := append(append(append([]byte(nil), nwk...), aux...), ctMic...)

	// MAC header: data, ack-request, PAN-id-compression, dst short, src short.
	mac := le16(nil, 0x8861)
	mac = append(mac, r.MacSeq)
	mac = le16(mac, r.DstPan)
	mac = le16(mac, r.MacDst)
	mac = le16(mac, r.SrcShort)
	return append(mac, nwkFrame...)
}
