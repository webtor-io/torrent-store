package services

import (
	"context"
	"errors"
	"testing"
)

// failingPushProvider is a tier whose torrent writes always fail — a closed
// Badger or a Redis outage. Reads miss, so pulls fall through to lower tiers.
type failingPushProvider struct {
	*fakeProvider
	pushErr error
}

func (f *failingPushProvider) Push(_ context.Context, _ string, _ []byte) (bool, error) {
	return false, f.pushErr
}

func (f *failingPushProvider) Pull(_ context.Context, _ string) ([]byte, error) {
	return nil, ErrNotFound
}

// An opportunistic fingerprint cache write must not re-write what is already
// cached: Pull derives the fingerprint on every cold request, and the S3
// object is immutable, so re-putting it on each pull is pure write
// amplification. The probe replaces the write on a hit.
func TestCacheFingerprintSkipsWriteWhenAlreadyCached(t *testing.T) {
	fast := newFakeProvider("fast", true)
	durable := newFakeProvider("durable", true)
	store := NewStore([]StoreProvider{fast, durable})

	const h = "cafe0001"
	payload := []byte("abc\t1\n")
	_, _ = fast.PushFingerprint(context.Background(), h, payload)
	_, _ = durable.PushFingerprint(context.Background(), h, payload)
	fast.pushFpCalls, durable.pushFpCalls = 0, 0

	store.CacheFingerprint(context.Background(), h, payload)
	waitFor(t, func() bool {
		fast.mu.Lock()
		defer fast.mu.Unlock()
		return fast.pullFpCalls > 0
	}, "the probe never ran")
	fast.mu.Lock()
	durable.mu.Lock()
	defer fast.mu.Unlock()
	defer durable.mu.Unlock()
	if fast.pushFpCalls != 0 || durable.pushFpCalls != 0 {
		t.Fatalf("cached fingerprint was re-written: fast=%d durable=%d pushes", fast.pushFpCalls, durable.pushFpCalls)
	}
}

// On a full miss the write must still happen — that is the whole point of the
// opportunistic cache.
func TestCacheFingerprintWritesOnMiss(t *testing.T) {
	fast := newFakeProvider("fast", true)
	store := NewStore([]StoreProvider{fast})

	const h = "cafe0002"
	payload := []byte("abc\t1\n")
	store.CacheFingerprint(context.Background(), h, payload)
	waitFor(t, func() bool {
		fast.mu.Lock()
		defer fast.mu.Unlock()
		return string(fast.fingerprints[h]) == string(payload)
	}, "fingerprint was not written on a miss")
}

// A cache tier failing its backfill write must not fail the pull itself: the
// torrent is already in hand, and the whole point of the tier walk is that
// upper tiers are optional. Regression test for the named-return clobber where
// a failed backfill Push overwrote pull's err after a successful lower-tier hit.
func TestPullSurvivesBackfillFailure(t *testing.T) {
	broken := &failingPushProvider{
		fakeProvider: newFakeProvider("broken", true),
		pushErr:      errors.New("tier is down"),
	}
	durable := newFakeProvider("durable", true)
	store := NewStore([]StoreProvider{broken, durable})

	const h = "feedbead"
	payload := []byte("torrent-bytes")
	_, _ = durable.Push(context.Background(), h, payload)

	got, err := store.pull(context.Background(), h, 0)
	if err != nil {
		t.Fatalf("pull returned an error despite a successful lower-tier hit: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("pull returned %q, want %q", got, payload)
	}
}
