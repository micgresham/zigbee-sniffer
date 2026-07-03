package decode

import (
	"crypto/aes"
	"crypto/subtle"
	"encoding/binary"
)

// AES-CCM* (RFC 3610). Go's stdlib has no CCM, so we implement it over the AES
// block cipher. For the Zigbee over-the-air security level (MIC-32) CCM* == CCM.

const SecLevelEncMIC32 = 5

// RestoreSecLevel sets the security-level bits (0..2) of the security-control byte.
// Zigbee transmits these zeroed but computes the MIC with the real level (5).
func RestoreSecLevel(secControl byte) byte {
	return (secControl & 0xF8) | (SecLevelEncMIC32 & 0x07)
}

// MakeNonce builds the 13-byte CCM* nonce.
func MakeNonce(srcExt uint64, frameCounter uint32, secControl byte) []byte {
	n := make([]byte, 13)
	binary.LittleEndian.PutUint64(n[0:8], srcExt)
	binary.LittleEndian.PutUint32(n[8:12], frameCounter)
	n[12] = secControl
	return n
}

// EncryptCCM encrypts + authenticates: returns ciphertext followed by the MIC.
// Inverse of DecryptCCM (same CBC-MAC + CTR machinery).
func EncryptCCM(key, nonce, aad, pt []byte, micLen int) ([]byte, bool) {
	if len(key) != 16 {
		return nil, false
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, false
	}
	L := 15 - len(nonce)

	x := make([]byte, 16)
	macBlock := func(in []byte) {
		for k := 0; k < 16; k++ {
			x[k] ^= in[k]
		}
		block.Encrypt(x, x)
	}
	adata := 0
	if len(aad) > 0 {
		adata = 1
	}
	b0 := make([]byte, 16)
	b0[0] = byte(64*adata + 8*((micLen-2)/2) + (L - 1))
	copy(b0[1:], nonce)
	binary.BigEndian.PutUint16(b0[14:], uint16(len(pt)))
	macBlock(b0)
	if adata == 1 {
		ab := append([]byte{byte(len(aad) >> 8), byte(len(aad))}, aad...)
		for len(ab)%16 != 0 {
			ab = append(ab, 0)
		}
		for i := 0; i < len(ab); i += 16 {
			macBlock(ab[i : i+16])
		}
	}
	for i := 0; i < len(pt); i += 16 {
		blk := make([]byte, 16)
		copy(blk, pt[i:min(i+16, len(pt))])
		macBlock(blk)
	}

	a := make([]byte, 16)
	a[0] = byte(L - 1)
	copy(a[1:], nonce)
	s0 := make([]byte, 16)
	binary.BigEndian.PutUint16(a[14:], 0)
	block.Encrypt(s0, a)
	ct := make([]byte, len(pt))
	s := make([]byte, 16)
	for i := 0; i < len(pt); i += 16 {
		binary.BigEndian.PutUint16(a[14:], uint16(i/16+1))
		block.Encrypt(s, a)
		for j := 0; j < 16 && i+j < len(pt); j++ {
			ct[i+j] = pt[i+j] ^ s[j]
		}
	}
	mic := make([]byte, micLen)
	for i := 0; i < micLen; i++ {
		mic[i] = x[i] ^ s0[i]
	}
	return append(ct, mic...), true
}

// DecryptCCM verifies + decrypts. ctMic = ciphertext followed by the MIC.
// Returns (plaintext, true) on success, (nil, false) if authentication fails.
func DecryptCCM(key, nonce, aad, ctMic []byte, micLen int) ([]byte, bool) {
	if len(key) != 16 || len(ctMic) < micLen {
		return nil, false
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, false
	}
	L := 15 - len(nonce) // length field size

	ct := ctMic[:len(ctMic)-micLen]
	mic := ctMic[len(ctMic)-micLen:]

	// CTR keystream: A_i = flags(L-1) || nonce || counter(i)
	a := make([]byte, 16)
	a[0] = byte(L - 1)
	copy(a[1:], nonce)
	s := make([]byte, 16)
	pt := make([]byte, len(ct))
	for i := 0; i < len(ct); i += 16 {
		binary.BigEndian.PutUint16(a[14:], uint16(i/16+1))
		block.Encrypt(s, a)
		for j := 0; j < 16 && i+j < len(ct); j++ {
			pt[i+j] = ct[i+j] ^ s[j]
		}
	}

	// CBC-MAC over B_0 || enc(AAD) || plaintext
	x := make([]byte, 16)
	macBlock := func(in []byte) {
		for k := 0; k < 16; k++ {
			x[k] ^= in[k]
		}
		block.Encrypt(x, x)
	}
	adata := 0
	if len(aad) > 0 {
		adata = 1
	}
	b0 := make([]byte, 16)
	b0[0] = byte(64*adata + 8*((micLen-2)/2) + (L - 1))
	copy(b0[1:], nonce)
	binary.BigEndian.PutUint16(b0[14:], uint16(len(pt)))
	macBlock(b0)

	if adata == 1 {
		ab := make([]byte, 0, 2+len(aad)+15)
		ab = append(ab, byte(len(aad)>>8), byte(len(aad)))
		ab = append(ab, aad...)
		for len(ab)%16 != 0 {
			ab = append(ab, 0)
		}
		for i := 0; i < len(ab); i += 16 {
			macBlock(ab[i : i+16])
		}
	}
	for i := 0; i < len(pt); i += 16 {
		blk := make([]byte, 16)
		copy(blk, pt[i:min(i+16, len(pt))])
		macBlock(blk)
	}

	// U = T XOR S_0
	binary.BigEndian.PutUint16(a[14:], 0)
	s0 := make([]byte, 16)
	block.Encrypt(s0, a)
	u := make([]byte, micLen)
	for i := 0; i < micLen; i++ {
		u[i] = x[i] ^ s0[i]
	}
	if subtle.ConstantTimeCompare(u, mic) == 1 {
		return pt, true
	}
	return nil, false
}
