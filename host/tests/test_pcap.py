import io
import struct

from zbsniff.store import pcap


def test_global_header_linktype():
    fp = io.BytesIO()
    pcap.PcapWriter(fp)
    fp.seek(0)
    magic, vmaj, vmin, tz, sig, snap, network = struct.unpack("<IHHiIII", fp.read(24))
    assert magic == 0xA1B2C3D4
    assert vmaj == 2 and vmin == 4
    assert network == pcap.LINKTYPE_IEEE802_15_4_TAP


def test_tap_header_is_4byte_aligned_and_contains_tlvs():
    hdr = pcap.build_tap_header(rssi_dbm=-61, lqi=200, channel=15, fcs_present=True)
    # base header length field must equal total header length
    _ver, _res, length = struct.unpack_from("<BBH", hdr, 0)
    assert length == len(hdr)
    assert len(hdr) % 4 == 0


def test_write_frame_records_packet():
    fp = io.BytesIO()
    w = pcap.PcapWriter(fp)
    mpdu = bytes(range(25))
    w.write_frame(ts_us=1_500_000, rssi=-70, lqi=180, channel=20, mpdu=mpdu)

    fp.seek(24)  # skip global header
    ts_sec, ts_usec, cap_len, orig_len = struct.unpack("<IIII", fp.read(16))
    assert ts_sec == 1 and ts_usec == 500_000
    assert cap_len == orig_len
    packet = fp.read(cap_len)
    # packet = TAP header + mpdu; ensure the mpdu trails intact
    assert packet.endswith(mpdu)
