package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"io"
	"os"
	"strings"
)

// Secrets (the Zigbee network key and HA token) are encrypted at rest with
// AES-256-GCM. The key lives in a sidecar file (config path + ".key", mode 0600)
// so the YAML alone — if copied, backed up, or committed — never exposes them.
// This guards against accidental leakage, not a local attacker with the keyfile.

const encPrefix = "enc:"

// loadOrCreateKey reads a 32-byte key from path, creating a random one if absent.
// On any failure it returns nil (secrets then fall back to plaintext).
func loadOrCreateKey(path string) []byte {
	if b, err := os.ReadFile(path); err == nil && len(b) == 32 {
		return b
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil
	}
	if err := os.WriteFile(path, key, 0600); err != nil {
		return nil
	}
	return key
}

// encryptField returns "enc:<base64(nonce|ciphertext)>" for a non-empty value.
// Empty values and a missing key pass through unchanged.
func encryptField(key []byte, v string) string {
	if v == "" || len(key) != 32 || strings.HasPrefix(v, encPrefix) {
		return v
	}
	gcm, err := newGCM(key)
	if err != nil {
		return v
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return v
	}
	ct := gcm.Seal(nonce, nonce, []byte(v), nil)
	return encPrefix + base64.StdEncoding.EncodeToString(ct)
}

// decryptField reverses encryptField. A plaintext (non-"enc:") value is returned
// as-is so hand-edited configs still work (and are encrypted on the next Save).
func decryptField(key []byte, v string) string {
	if !strings.HasPrefix(v, encPrefix) {
		return v
	}
	if len(key) != 32 {
		return "" // can't decrypt without the key
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, encPrefix))
	if err != nil {
		return ""
	}
	gcm, err := newGCM(key)
	if err != nil || len(raw) < gcm.NonceSize() {
		return ""
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return ""
	}
	return string(pt)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
