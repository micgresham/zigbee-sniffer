package proto

import "testing"

func TestCRC16(t *testing.T) {
	if got := CRC16([]byte("123456789")); got != 0x29B1 {
		t.Fatalf("crc16 = 0x%04x, want 0x29B1", got)
	}
}

func TestStreamDecoder(t *testing.T) {
	f := CmdSetChannelMsg(15)
	var d StreamDecoder
	stream := append([]byte{0x00, 0xFF}, f...) // leading garbage
	stream = append(stream, 0x5A)              // trailing partial magic
	out := d.Feed(stream)
	if len(out) != 1 || out[0].Type != CmdSetChannel {
		t.Fatalf("decoded %+v", out)
	}
}

func TestCapturedRoundtrip(t *testing.T) {
	// payload: radio,ch,rssi,lqi,flags, ts(8), mlen, mpdu
	mpdu := []byte{1, 2, 3, 4, 5}
	p := []byte{0, 15, 0xC3 /* int8 -61 */, 200, FlagCRCOK}
	p = append(p, 1, 0, 0, 0, 0, 0, 0, 0) // ts=1
	p = append(p, byte(len(mpdu)))
	p = append(p, mpdu...)
	frame := Encode(MsgCapturedFrame, p)

	var d StreamDecoder
	out := d.Feed(frame)
	if len(out) != 1 {
		t.Fatalf("frames=%d", len(out))
	}
	cf, ok := ParsePayload(out[0].Type, out[0].Payload).(*CapturedFrame)
	if !ok || cf.Channel != 15 || cf.RSSI != -61 || cf.LQI != 200 {
		t.Fatalf("bad captured: %+v", cf)
	}
	if string(cf.MPDU) != string(mpdu) {
		t.Fatalf("mpdu mismatch")
	}
}
