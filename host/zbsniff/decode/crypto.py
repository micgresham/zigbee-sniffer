"""Zigbee AES-CCM* decryption (NWK / APS layer).

Zigbee secures frames with AES-CCM* (RFC 3610 CCM is a subset; for the MIC-32
security level used over the air, CCM* == CCM, so we use the standard AES-CCM
primitive from `cryptography`).

Key subtleties handled here:
- **Nonce (13 bytes)** = source extended address (8, little-endian) + frame
  counter (4, little-endian) + security-control byte (1).
- **Security level**: Zigbee transmits the security-control byte with the level
  bits *zeroed* over the air, but the MIC was computed with the real level
  (nwkSecurityLevel, normally 5 = ENC-MIC-32). So the security-control byte must
  be restored to the real level both in the nonce and in the authenticated header
  before verifying/decrypting. See docs/decoding-and-decryption.md.

This module is dependency-light at import time; `cryptography` is only needed
when you actually decrypt (it's in the `host` extra).
"""

from __future__ import annotations

SEC_LEVEL_ENC_MIC32 = 5
MIC_LEN = {0: 0, 1: 4, 2: 8, 3: 16, 4: 0, 5: 4, 6: 8, 7: 16}


def restore_sec_level(sec_control: int, level: int = SEC_LEVEL_ENC_MIC32) -> int:
    """Set the security-level bits (0..2) of the security-control byte."""
    return (sec_control & 0xF8) | (level & 0x07)


def make_nonce(src_ext: int, frame_counter: int, sec_control: int) -> bytes:
    """Build the 13-byte CCM* nonce (security-control already level-restored)."""
    return (src_ext.to_bytes(8, "little")
            + frame_counter.to_bytes(4, "little")
            + bytes([sec_control & 0xFF]))


def decrypt_ccm(key: bytes, nonce: bytes, aad: bytes, ciphertext_and_mic: bytes,
                mic_len: int = 4) -> bytes | None:
    """AES-CCM* decrypt+verify. Returns plaintext, or None if auth fails.

    `ciphertext_and_mic` is the encrypted payload followed by the MIC.
    `aad` is the authenticated (unencrypted) header with the real security level.
    """
    from cryptography.hazmat.primitives.ciphers.aead import AESCCM
    from cryptography.exceptions import InvalidTag

    if len(key) != 16:
        raise ValueError("network key must be 16 bytes")
    aesccm = AESCCM(key, tag_length=mic_len)
    try:
        return aesccm.decrypt(nonce, ciphertext_and_mic, aad)
    except InvalidTag:
        return None
