"""NWK/APS/ZCL decode + AES-CCM* decryption tests.

The decryption test builds a correctly-secured NWK frame (encrypting with the
same CCM* nonce/AAD construction Zigbee uses) and confirms decode_nwk/decode_frame
invert it. cryptography is required (the `host` extra); the test skips if absent.
"""

import struct

import pytest

from zbsniff.decode import decode_nwk, decode_aps, decode_zcl, decode_frame
from zbsniff.decode import crypto


def _aps_zcl_plaintext() -> bytes:
    # APS Data: unicast, dst_ep=1, cluster=0x0006 (On/Off), profile=0x0104, src_ep=1, counter=5
    aps = bytes([0x00, 0x01, 0x06, 0x00, 0x04, 0x01, 0x01, 0x05])
    # ZCL: cluster-specific, tsn=0x10, command 0x01 (On)
    zcl = bytes([0x01, 0x10, 0x01])
    return aps + zcl


def test_unsecured_nwk_parse():
    # NWK Data, version 2, not secured. fcf = (2<<2) = 0x08
    hdr = struct.pack("<HHHBB", 0x0008, 0x0000, 0x1234, 30, 42)
    frame = hdr + b"\xaa\xbb"
    nwk = decode_nwk(frame)
    assert nwk.type_name == "NWK Data"
    assert nwk.src == 0x1234 and nwk.dst == 0x0000
    assert nwk.radius == 30 and nwk.seq == 42
    assert not nwk.secure and nwk.payload == b"\xaa\xbb"


def test_aps_data_parse():
    aps = decode_aps(_aps_zcl_plaintext())
    assert aps.type_name == "APS Data"
    assert aps.cluster == 0x0006
    assert aps.profile == 0x0104
    assert aps.dst_endpoint == 1 and aps.src_endpoint == 1
    assert aps.counter == 5


def test_zcl_parse():
    zcl = decode_zcl(bytes([0x00, 0x10, 0x0A]))  # global, Report Attributes
    assert zcl.summary == "Report Attributes"
    assert zcl.tsn == 0x10


def _build_secured_nwk(key: bytes) -> bytes:
    from cryptography.hazmat.primitives.ciphers.aead import AESCCM

    src_ext = 0x00124B00AABBCCDD
    fc = 0x12345678
    # NWK fcf: data, version 2, security bit set -> (2<<2)|(1<<9) = 0x0208
    hdr = struct.pack("<HHHBB", 0x0208, 0x0000, 0x1234, 30, 42)
    # aux security header, security-control byte sent with level=0 over the air:
    #   key_id=network(1)<<3 | ext_nonce(1)<<5 = 0x28
    sc_air = 0x28
    aux = bytes([sc_air]) + struct.pack("<I", fc) + src_ext.to_bytes(8, "little") + bytes([0x00])
    header = hdr + aux

    real_sc = crypto.restore_sec_level(sc_air)        # 0x2D
    aad = bytearray(header)
    aad[len(hdr)] = real_sc                            # restore level in the AAD
    nonce = crypto.make_nonce(src_ext, fc, real_sc)
    ct = AESCCM(key, tag_length=4).encrypt(nonce, _aps_zcl_plaintext(), bytes(aad))
    return header + ct


def test_nwk_decrypt_roundtrip():
    pytest.importorskip("cryptography")
    key = bytes(range(16))
    frame = _build_secured_nwk(key)

    # Without the key: secured but not decrypted.
    enc = decode_nwk(frame)
    assert enc.secure and not enc.decrypted

    # With the key: decrypts, and APS/ZCL parse from the plaintext.
    dec = decode_nwk(frame, key)
    assert dec.decrypted
    assert dec.payload == _aps_zcl_plaintext()
    aps = decode_aps(dec.payload)
    assert aps.cluster == 0x0006


def test_decode_frame_full_stack_secured():
    pytest.importorskip("cryptography")
    key = bytes(range(16))
    nwk_frame = _build_secured_nwk(key)
    # Wrap in a MAC Data frame (FCF 0x8861: data, dst16+src16, PAN compression).
    mac_hdr = bytes([0x61, 0x88, 0x2a, 0xcd, 0xab, 0x00, 0x00, 0x34, 0x12])
    mpdu = mac_hdr + nwk_frame + b"\x00\x00"   # trailing FCS
    out = decode_frame(mpdu, key)
    assert out.mac.type_name == "Data"
    assert out.nwk and out.nwk.decrypted
    assert out.aps and out.aps.cluster == 0x0006
    assert out.zcl is not None
    assert "0x0006" in out.summary
