package privaterecord

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	keyLen   = 32
	nonceLen = 12
	tagLen   = 16
)

var (
	// ErrBadRecord means no key opened the blob or its plaintext is not a v1
	// record. The two are one error: without a key id they cannot be told apart.
	ErrBadRecord              = errors.New("privaterecord: bad record")
	ErrBadKey                 = errors.New("privaterecord: key must be 64 hex characters")
	ErrPreviousWithoutCurrent = errors.New("privaterecord: previous key set without current key")
)

// newGCM is AES-256-GCM only: a 16- or 24-byte key would quietly be AES-128/192.
func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != keyLen {
		return nil, fmt.Errorf("privaterecord: key is %d bytes, want %d", len(key), keyLen)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func additionalData(hash, uid string) []byte {
	return []byte("ts1|" + hash + "|" + uid)
}

// Seal encrypts the canonical form of tiers (normalized by the caller) for one
// hash and uid as nonce(12) | ciphertext | tag(16), with a fresh random nonce.
func Seal(key []byte, hash, uid string, tiers [][]string) ([]byte, error) {
	nonce := make([]byte, nonceLen)
	rand.Read(nonce) // never returns an error since Go 1.24: it crashes instead
	return sealWithNonce(key, nonce, hash, uid, tiers)
}

func sealWithNonce(key, nonce []byte, hash, uid string, tiers [][]string) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, Canonical(tiers), additionalData(hash, uid)), nil
}

// Open decrypts a blob sealed for hash and uid, trying keys in order (current,
// then previous), and returns its tiers. Any failure is ErrBadRecord.
func Open(keys [][]byte, hash, uid string, blob []byte) ([][]string, error) {
	if len(blob) < nonceLen+tagLen {
		return nil, ErrBadRecord
	}
	ad := additionalData(hash, uid)
	for _, key := range keys {
		gcm, err := newGCM(key)
		if err != nil {
			continue
		}
		plain, err := gcm.Open(nil, blob[:nonceLen], blob[nonceLen:], ad)
		if err != nil {
			continue
		}
		var r record
		if err := json.Unmarshal(plain, &r); err != nil || r.V != 1 {
			return nil, ErrBadRecord
		}
		return r.T, nil
	}
	return nil, ErrBadRecord
}

// ParseKeys decodes the current and previous keys, 64 hex characters each,
// surrounding white space trimmed. Both unset is nil, nil: the feature is off.
// Errors name the key, never carry its value.
func ParseKeys(current, previous string) ([][]byte, error) {
	current, previous = strings.TrimSpace(current), strings.TrimSpace(previous)
	switch {
	case current == "" && previous == "":
		return nil, nil
	case current == "":
		return nil, ErrPreviousWithoutCurrent
	}
	cur, err := parseKey("current", current)
	if err != nil {
		return nil, err
	}
	if previous == "" {
		return [][]byte{cur}, nil
	}
	prev, err := parseKey("previous", previous)
	if err != nil {
		return nil, err
	}
	return [][]byte{cur, prev}, nil
}

func parseKey(name, s string) ([]byte, error) {
	k, err := hex.DecodeString(s)
	if err != nil || len(k) != keyLen {
		return nil, fmt.Errorf("%s: %w", name, ErrBadKey)
	}
	return k, nil
}

// Acct is the account in log lines: the first 8 hex characters of sha256(uid),
// "" for no uid.
func Acct(uid string) string {
	if uid == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(uid))
	return hex.EncodeToString(sum[:])[:8]
}
