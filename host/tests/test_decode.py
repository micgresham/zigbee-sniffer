from zbsniff.decode import decode_mac
from zbsniff.decode.mac import ADDR_SHORT, ADDR_EXTENDED


def test_data_frame_short_addrs_pancomp():
    # FCF=0x8861: Data, dst16+src16, PAN-ID compression.
    # bytes: FCF(2) seq panid dst16 src16 payload... fcs(2)
    mpdu = bytes([0x61, 0x88, 0x2a,
                  0xcd, 0xab,        # dst pan 0xabcd
                  0x34, 0x12,        # dst 0x1234
                  0x78, 0x56,        # src 0x5678
                  0xde, 0xad,        # payload
                  0x00, 0x00])       # fcs
    m = decode_mac(mpdu)
    assert m.type_name == "Data"
    assert m.seq == 0x2a
    assert m.pan_id_compression
    assert m.dst_pan == 0xabcd
    assert m.dst_addr == 0x1234 and m.dst_mode == ADDR_SHORT
    assert m.src_addr == 0x5678 and m.src_mode == ADDR_SHORT
    assert m.src_pan == 0xabcd  # inherited via PAN compression
    assert m.payload == bytes([0xde, 0xad])
    assert "0x5678" in m.summary and "0x1234" in m.summary


def test_ack_frame_no_addrs():
    # FCF=0x0002: Ack, no addressing.
    mpdu = bytes([0x02, 0x00, 0x11, 0x00, 0x00])
    m = decode_mac(mpdu)
    assert m.type_name == "Ack"
    assert m.seq == 0x11
    assert m.dst_addr is None and m.src_addr is None


def test_extended_source_address():
    # Data, dst none, src extended (64-bit), no PAN compression: src PAN precedes
    # the extended source address. FCF = (src_mode<<14) | Data(1).
    fcf = (ADDR_EXTENDED << 14) | 1
    mpdu = bytes([fcf & 0xFF, fcf >> 8, 0x07,
                  0x11, 0x22,                                      # src pan 0x2211
                  0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xAA,  # src ext (LE)
                  0xBB, 0xCC,   # payload
                  0x00, 0x00])  # fcs
    m = decode_mac(mpdu)
    assert m.src_mode == ADDR_EXTENDED
    assert m.src_pan == 0x2211
    assert m.src_addr == 0xAA99887766554433
    assert m.payload == bytes([0xBB, 0xCC])


def test_runt_frame():
    m = decode_mac(b"\x01")
    assert m.type_name == "(runt)"
