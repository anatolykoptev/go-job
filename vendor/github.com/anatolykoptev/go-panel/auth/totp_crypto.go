package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// TOTPEncryptionKeyLen is the required length, in bytes, of
// BcryptConfig.TOTPEncryptionKey. Unlike minHMACKeyLen (an RFC 2104 floor —
// HMAC accepts any key length), AES-256 requires an EXACT 256-bit key
// (FIPS 197; crypto/aes.NewCipher rejects any length other than 16/24/32),
// so EncryptTOTPSecret/DecryptTOTPSecret reject anything != 32 rather than
// silently truncating a longer key or accepting a shorter, weaker one.
const TOTPEncryptionKeyLen = 32

// ErrInvalidTOTPKeyLength is returned by EncryptTOTPSecret/DecryptTOTPSecret
// when key is not exactly TOTPEncryptionKeyLen bytes. This is a
// configuration error, not attacker-reachable input (the key always comes
// from BcryptConfig.TOTPEncryptionKey, validated once at setup — see
// NewBcryptTOTPAuth) — kept distinct from ErrTOTPDecryptFailed precisely
// because it is a different error CLASS, not a data/crypto-verification
// oracle: distinguishing "misconfigured key" from "corrupt or forged
// ciphertext" leaks nothing an attacker can exploit online.
var ErrInvalidTOTPKeyLength = fmt.Errorf("auth: TOTPEncryptionKey must be exactly %d bytes", TOTPEncryptionKeyLen)

// ErrTOTPDecryptFailed is returned by DecryptTOTPSecret for every AEAD-open
// failure — tampered/corrupted ciphertext, a wrong (but correctly-sized)
// key, or a truncated ciphertext — deliberately collapsed to ONE error so
// no caller can build a decryption oracle by branching on which sub-case
// occurred (crypto-review side_channels: distinct "bad key" vs "bad tag"
// vs "bad structure" errors are themselves an information leak).
var ErrTOTPDecryptFailed = errors.New("auth: totp secret decryption failed")

// EncryptTOTPSecret seals plaintext (the base32 TOTP secret text, as
// returned by GenerateTOTPSecret's Key.Secret()) with AES-256-GCM under
// key, returning a random 96-bit nonce prepended to the ciphertext+tag
// (nonce || ciphertext). key must be exactly TOTPEncryptionKeyLen bytes —
// BcryptConfig.TOTPEncryptionKey, a DEDICATED key independent of
// BcryptConfig.HMACKey (see NewBcryptTOTPAuth's setup panic) so that
// rotating the session-signing HMACKey during a security incident never
// destroys stored TOTP secrets. A wrong key length fails closed with an
// error, never a panic — panics are reserved for setup-time validation.
//
// Nonce reuse under a fixed key is the one AES-GCM mistake that breaks
// confidentiality AND integrity (NIST SP 800-38D §8.3); a fresh
// crypto/rand nonce per call keeps this call site far below the ~2^32
// birthday bound even summed across the fleet's lifetime, since a TOTP
// secret is encrypted only once per enrollment (a rare, human-triggered
// event), not per-message.
func EncryptTOTPSecret(plaintext, key []byte) ([]byte, error) {
	if len(key) != TOTPEncryptionKeyLen {
		return nil, ErrInvalidTOTPKeyLength
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("auth: totp cipher init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("auth: totp gcm init: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("auth: totp nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// DecryptTOTPSecret reverses EncryptTOTPSecret. See ErrInvalidTOTPKeyLength
// and ErrTOTPDecryptFailed for the two distinct (and only) failure modes.
func DecryptTOTPSecret(ciphertext, key []byte) ([]byte, error) {
	if len(key) != TOTPEncryptionKeyLen {
		return nil, ErrInvalidTOTPKeyLength
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrTOTPDecryptFailed
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrTOTPDecryptFailed
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, ErrTOTPDecryptFailed
	}
	nonce, ct := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, ErrTOTPDecryptFailed
	}
	return pt, nil
}
