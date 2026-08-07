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
