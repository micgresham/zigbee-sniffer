package demo

import "zbsniff/internal/decode"

func le16(b []byte, v uint16) []byte { return append(b, byte(v), byte(v>>8)) }
func le32(b []byte, v uint32) []byte {
	return append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}
func le64(b []byte, v uint64) []byte {
	return append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24),
		byte(v>>32), byte(v>>40), byte(v>>48), byte(v>>56))
}

// hopMAC wraps an already-built NWK (or plain) payload in a MAC header for one
// physical hop — dst/src are the immediate neighbors on air for this hop, not
// necessarily the NWK-layer conversation's true endpoints. The trailing two
// bytes are a stand-in FCS: DecodeMAC always strips the last two bytes
// unconditionally (real hardware fills them in; nothing here validates them).
func hopMAC(seq byte, pan, macSrc, macDst uint16, payload []byte) []byte {
	b := []byte{0x61, 0x88, seq} // FCF 0x8861: data, ack-req, pan-compression, short dst+src (see inject.go)
	b = le16(b, pan)
	b = le16(b, macDst)
	b = le16(b, macSrc)
	b = append(b, payload...)
	return append(b, 0x00, 0x00)
}

// ackMAC is a minimal 802.15.4 ACK for the given sequence number.
func ackMAC(seq byte) []byte {
	return []byte{0x02, 0x00, seq, 0x00, 0x00}
}

// nwkSecured builds an encrypted NWK frame (header + auxiliary security header
// + ciphertext+MIC), end-to-end addressed nwkSrc->nwkDst, nonce'd off the true
// originating device's extended address — exactly what a real device would
// produce once, then have relayed unchanged hop-by-hop by intermediate routers.
func nwkSecured(nwkSrc, nwkDst uint16, srcExt uint64, frameCounter uint32, nwkSeq byte, apsPayload []byte) []byte {
	nwk := le16(nil, 0x0208) // data, protocol version 2, secured
	nwk = le16(nwk, nwkDst)
	nwk = le16(nwk, nwkSrc)
	nwk = append(nwk, 30) // radius
	nwk = append(nwk, nwkSeq)

	const scTx = 0x28 // security level 0 on the wire, key id 1 (network key), ext nonce present
	aux := []byte{scTx}
	aux = le32(aux, frameCounter)
	aux = le64(aux, srcExt)
	aux = append(aux, 0x00) // key sequence number
	realSC := decode.RestoreSecLevel(scTx)

	aad := append(append([]byte(nil), nwk...), aux...)
	aad[len(nwk)] = realSC
	nonce := decode.MakeNonce(srcExt, frameCounter, realSC)
	ctMic, ok := decode.EncryptCCM(NetworkKey, nonce, aad, apsPayload, 4)
	if !ok {
		return nil
	}
	return append(append(append([]byte(nil), nwk...), aux...), ctMic...)
}

const (
	profileHA  = 0x0104
	epPrimary  = 1
	clusterOnOff    = 0x0006
	clusterLevel    = 0x0008
	clusterTemp     = 0x0402
	clusterHumidity = 0x0405
	clusterOccupancy = 0x0406
	clusterPowerCfg = 0x0001
	clusterColorCtl = 0x0300
)

// zclReportAttrs builds an APS+ZCL "Report Attributes" payload (a global
// command, direction=server->client) for a sensor cluster — the attribute
// payload content itself is a placeholder; only the header fields the decode
// path actually inspects (cluster, profile, endpoints, ZCL cmd) matter for a
// realistic Summary.
func zclReportAttrs(apsSeq, tsn byte, cluster uint16) []byte {
	aps := []byte{0x00, epPrimary}
	aps = le16(aps, cluster)
	aps = le16(aps, profileHA)
	aps = append(aps, epPrimary, apsSeq)
	zcl := []byte{0x08, tsn, 0x0A, 0x00, 0x00, 0x21, 0x00, 0x00} // FC(global,server->client), tsn, ReportAttributes, attrID(2), type(1)=uint16, value(2)
	return append(aps, zcl...)
}

// zclCommand builds an APS+ZCL cluster-specific command (client->server),
// e.g. On/Off/Toggle or Move to Level — the kind of command a controller
// (coordinator/HA) sends down to a device.
func zclCommand(apsSeq, tsn byte, cluster uint16, cmd byte, args []byte) []byte {
	aps := []byte{0x00, epPrimary}
	aps = le16(aps, cluster)
	aps = le16(aps, profileHA)
	aps = append(aps, epPrimary, apsSeq)
	zcl := []byte{0x01, tsn, cmd} // FC(cluster-specific, client->server)
	aps = append(aps, zcl...)
	return append(aps, args...)
}
