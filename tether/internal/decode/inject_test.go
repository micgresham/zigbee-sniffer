package decode

import (
	"bytes"
	"testing"
)

// A built ZDO request must round-trip back through the full decoder: MAC parses,
// NWK decrypts + authenticates, APS carries the ZDP cluster, and the decrypted
// payload matches what we put in. This proves the CCM* encryption + framing are
// correct without any hardware.
func TestBuildZDORequestRoundTrip(t *testing.T) {
	key := make([]byte, 16)
	for i := range key {
		key[i] = byte(i * 7)
	}
	r := ZDORequest{
		TargetShort: 0x1a2b, MacDst: 0x1a2b, SrcShort: 0xfffe,
		SrcExt: 0x00124b00aabbccdd, DstPan: 0xe092,
		Cluster: ZDOMgmtBindReq, ZDPArgs: []byte{0x00}, // StartIndex 0
		FrameCounter: 0x01020304, MacSeq: 42, NwkSeq: 7, ApsSeq: 9, Tsn: 0x55,
		Key: key,
	}
	frame := BuildZDORequest(r)
	if frame == nil {
		t.Fatal("BuildZDORequest returned nil")
	}
	// The radio appends the 2-byte PHY FCS on transmit; a captured frame has it,
	// and DecodeMAC strips the last 2 bytes as FCS — so simulate that here.
	frame = append(frame, 0x00, 0x00)
	d := DecodeFrame(frame, key)
	if d.MAC == nil || d.MAC.TypeName != "Data" {
		t.Fatalf("MAC: %+v", d.MAC)
	}
	if d.MAC.DstAddr != 0x1a2b || d.MAC.SrcAddr != 0xfffe {
		t.Fatalf("MAC addrs: dst=%#x src=%#x", d.MAC.DstAddr, d.MAC.SrcAddr)
	}
	if d.NWK == nil || !d.NWK.Secure || !d.NWK.Decrypted {
		t.Fatalf("NWK not decrypted: %+v", d.NWK)
	}
	if d.APS == nil || d.APS.Cluster != ZDOMgmtBindReq || d.APS.Profile != 0 || d.APS.DstEP != 0 {
		t.Fatalf("APS: %+v", d.APS)
	}
	// Decrypted APS payload (after the APS header) = Tsn || args.
	if !bytes.Equal(d.APS.Payload, []byte{0x55, 0x00}) {
		t.Fatalf("ZDP payload = %x, want 5500", d.APS.Payload)
	}
}

func TestEncryptDecryptCCMRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 16)
	nonce := MakeNonce(0x1122334455667788, 0xdeadbeef, RestoreSecLevel(0x28))
	aad := []byte("nwk+aux header bytes")
	pt := []byte("Mgmt_Bind_req ZDO payload!")
	ctMic, ok := EncryptCCM(key, nonce, aad, pt, 4)
	if !ok {
		t.Fatal("encrypt failed")
	}
	got, ok := DecryptCCM(key, nonce, aad, ctMic, 4)
	if !ok {
		t.Fatal("decrypt/authenticate failed")
	}
	if !bytes.Equal(got, pt) {
		t.Fatalf("round-trip mismatch: %q", got)
	}
	// Tampering must fail authentication.
	ctMic[0] ^= 1
	if _, ok := DecryptCCM(key, nonce, aad, ctMic, 4); ok {
		t.Fatal("tampered frame authenticated")
	}
}
