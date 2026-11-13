package privaterecord

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
)

var (
	testKey  = Vector.Key
	testHash = Vector.Hash
	testUID  = Vector.UID
)

// sealPlain seals arbitrary plaintext the way Seal does, for corrupt-plaintext cases.
func sealPlain(t *testing.T, key, nonce, aad, plain []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return gcm.Seal(bytes.Clone(nonce), nonce, plain, aad)
}

func TestVector(t *testing.T) {
	v := Vector
	blob, err := sealWithNonce(v.Key, v.Nonce, v.Hash, v.UID, v.Tiers)
	if err != nil || !bytes.Equal(blob, v.Blob) {
		t.Fatalf("sealWithNonce = %x, %v; want %x", blob, err, v.Blob)
	}
	if got, err := Open([][]byte{v.Key}, v.Hash, v.UID, v.Blob); err != nil || !reflect.DeepEqual(got, v.Tiers) {
		t.Fatalf("Open = %q, %v", got, err)
	}
	if h := Hosts(v.Tiers); !reflect.DeepEqual(h, v.Hosts) {
		t.Fatalf("Hosts = %q, want %q", h, v.Hosts)
	}
}

func TestSealIsRandomAndOpens(t *testing.T) {
	a, _ := Seal(testKey, testHash, testUID, Vector.Tiers)
	b, _ := Seal(testKey, testHash, testUID, Vector.Tiers)
	if bytes.Equal(a[:12], b[:12]) {
		t.Fatal("same nonce twice")
	}
	for _, blob := range [][]byte{a, b} {
		if got, err := Open([][]byte{testKey}, testHash, testUID, blob); err != nil || !reflect.DeepEqual(got, Vector.Tiers) {
			t.Fatalf("Open = %q, %v", got, err)
		}
	}
	for _, n := range []int{16, 24, 31} {
		if _, err := Seal(testKey[:n], testHash, testUID, Vector.Tiers); err == nil {
			t.Fatalf("%d-byte key accepted", n)
		}
	}
}

func TestOpenRefusesOtherPair(t *testing.T) {
	blob, _ := Seal(testKey, testHash, testUID, Vector.Tiers)
	other := strings.Repeat("ab", 20)
	if _, err := Open([][]byte{testKey}, other, testUID, blob); !errors.Is(err, ErrBadRecord) {
		t.Fatalf("other hash: %v, want ErrBadRecord", err)
	}
	if _, err := Open([][]byte{testKey}, testHash, other, blob); !errors.Is(err, ErrBadRecord) {
		t.Fatalf("other uid: %v, want ErrBadRecord", err)
	}
}

func TestOpenKeys(t *testing.T) {
	old := bytes.Repeat([]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}, 4)
	blob, _ := Seal(old, testHash, testUID, Vector.Tiers)
	for _, keys := range [][][]byte{nil, {testKey}} {
		if _, err := Open(keys, testHash, testUID, blob); !errors.Is(err, ErrBadRecord) {
			t.Fatalf("%d keys, none fits: %v, want ErrBadRecord", len(keys), err)
		}
	}
	if got, err := Open([][]byte{testKey, old}, testHash, testUID, blob); err != nil || !reflect.DeepEqual(got, Vector.Tiers) {
		t.Fatalf("previous key: %q, %v", got, err)
	}
}

func TestOpenChecksVersion(t *testing.T) {
	aad := []byte("ts1|" + testHash + "|" + testUID)
	for name, plain := range map[string]string{
		"v2":       `{"v":2,"t":[["http://tracker.example/a"]]}`,
		"not json": `v1`,
	} {
		blob := sealPlain(t, testKey, Vector.Nonce, aad, []byte(plain))
		if _, err := Open([][]byte{testKey}, testHash, testUID, blob); !errors.Is(err, ErrBadRecord) {
			t.Fatalf("%s: %v, want ErrBadRecord", name, err)
		}
	}
}

func TestOpenTruncated(t *testing.T) {
	for _, n := range []int{0, 1, 12, 27, 28, len(Vector.Blob) - 1} {
		if _, err := Open([][]byte{testKey}, testHash, testUID, Vector.Blob[:n]); !errors.Is(err, ErrBadRecord) {
			t.Fatalf("len %d: %v, want ErrBadRecord", n, err)
		}
	}
}

func TestParseKeys(t *testing.T) {
	cur := hex.EncodeToString(Vector.Key)
	prev := strings.Repeat("0123456789abcdef", 4)
	if k, err := ParseKeys("", ""); k != nil || err != nil {
		t.Fatalf("unset: %v, %v", k, err)
	}
	if k, err := ParseKeys(" "+cur+"\n", ""); err != nil || len(k) != 1 || !bytes.Equal(k[0], Vector.Key) {
		t.Fatalf("current with whitespace: %x, %v", k, err)
	}
	if k, err := ParseKeys(cur, prev); err != nil || len(k) != 2 || hex.EncodeToString(k[1]) != prev {
		t.Fatalf("current and previous: %x, %v", k, err)
	}
	for name, c := range map[string]struct {
		cur, prev string
		err       error
	}{
		"63 hex":        {cur[:63], "", ErrBadKey},
		"not hex":       {strings.Repeat("zz", 32), "", ErrBadKey},
		"16 bytes":      {cur[:32], "", ErrBadKey},
		"bad previous":  {cur, "00", ErrBadKey},
		"previous only": {"", prev, ErrPreviousWithoutCurrent},
	} {
		_, err := ParseKeys(c.cur, c.prev)
		if !errors.Is(err, c.err) {
			t.Fatalf("%s: %v, want %v", name, err, c.err)
		}
		if c.cur != "" && strings.Contains(err.Error(), c.cur) {
			t.Fatalf("%s: error text carries the key", name)
		}
	}
}

func TestAcct(t *testing.T) {
	if got := Acct(Vector.UID); got != "5f44da3a" {
		t.Fatalf("Acct = %q", got)
	}
	if got := Acct("0123456789abcdef0123456789abcdef01234567"); got != "deb87fab" {
		t.Fatalf("Acct = %q", got)
	}
	if got := Acct(""); got != "" {
		t.Fatalf(`Acct("") = %q`, got)
	}
}
