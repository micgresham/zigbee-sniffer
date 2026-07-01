import struct

from zbsniff import proto


def test_crc16_known_vector():
    # CRC-16/CCITT-FALSE("123456789") == 0x29B1
    assert proto.crc16(b"123456789") == 0x29B1


def test_encode_has_magic_and_crc():
    frame = proto.cmd_set_channel(15)
    assert frame[0] == proto.MAGIC0 and frame[1] == proto.MAGIC1
    assert frame[2] == proto.PROTO_VER
    assert frame[3] == proto.Msg.CMD_SET_CHANNEL
    # CRC at the end must validate over ver..payload
    body = frame[2:-2]
    (crc,) = struct.unpack_from("<H", frame, len(frame) - 2)
    assert proto.crc16(body) == crc


def _make_captured_payload():
    mpdu = bytes(range(20))
    p = struct.pack("<BBbBB", 0, 15, -61, 200, proto.FLAG_CRC_OK | proto.FLAG_PROMISCUOUS)
    p += struct.pack("<Q", 123456789)
    p += bytes([len(mpdu)]) + mpdu
    return p, mpdu


def test_captured_frame_roundtrip():
    payload, mpdu = _make_captured_payload()
    frame = proto.encode(proto.Msg.CAPTURED_FRAME, payload)

    dec = proto.StreamDecoder()
    msgs = dec.feed(frame)
    assert len(msgs) == 1
    mtype, body = msgs[0]
    assert mtype == proto.Msg.CAPTURED_FRAME

    cf = proto.parse_payload(mtype, body)
    assert isinstance(cf, proto.CapturedFrame)
    assert cf.channel == 15
    assert cf.rssi == -61
    assert cf.lqi == 200
    assert cf.crc_ok
    assert cf.timestamp == 123456789
    assert cf.mpdu == mpdu


def test_decoder_handles_split_and_garbage():
    payload, _ = _make_captured_payload()
    frame = proto.encode(proto.Msg.CAPTURED_FRAME, payload)
    stream = b"\x00\xff garbage " + frame[:5] + frame[5:] + b"\x5a trailing"

    dec = proto.StreamDecoder()
    found = []
    # feed one byte at a time to stress the state machine
    for b in stream:
        found += dec.feed(bytes([b]))
    assert len(found) == 1
    assert found[0][0] == proto.Msg.CAPTURED_FRAME


def test_two_frames_back_to_back():
    f1 = proto.cmd_set_channel(11)
    f2 = proto.cmd_get_status()
    dec = proto.StreamDecoder()
    msgs = dec.feed(f1 + f2)
    types = [m[0] for m in msgs]
    assert types == [proto.Msg.CMD_SET_CHANNEL, proto.Msg.CMD_GET_STATUS]


def test_status_roundtrip():
    p = struct.pack("<BBBIHIIIIBB", 0, 1, 15, proto.all_channels_mask(), 0,
                    42, 1000, 0, 3, 0, 1)
    cf = proto.parse_payload(proto.Msg.STATUS, p)
    assert cf.channel == 15
    assert cf.captured == 1000
    assert cf.dropped_buf == 3
    assert cf.fw_minor == 1


def test_incident_roundtrip():
    js = '{"ts":120,"addr":"0x1234","reason":"silence","rssi":-72,"lqi":110,"ch":15,"ed":-88,"silent_s":130}'
    frame = proto.encode(proto.Msg.INCIDENT, js.encode())
    dec = proto.StreamDecoder()
    (mtype, body), = dec.feed(frame)
    inc = proto.parse_payload(mtype, body)
    assert isinstance(inc, proto.Incident)
    assert inc.data["addr"] == "0x1234"
    assert inc.data["reason"] == "silence"
    assert inc.data["silent_s"] == 130


def test_ed_result_roundtrip():
    p = struct.pack("<BB", 0, 20) + struct.pack("<Q", 999) + struct.pack("<bH", -80, 7)
    ed = proto.parse_payload(proto.Msg.ED_RESULT, p)
    assert ed.channel == 20
    assert ed.ed_dbm == -80
    assert ed.sweep_id == 7
