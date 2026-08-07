package services

import (
	"context"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Blocking policy lives here, in two arms.
//
//   - gateInfoHash — the hard legal gate (CSAM etc.). Runs before any bytes
//     are fetched, and a lookup failure fails the request: when the oracle is
//     unreachable we refuse rather than risk serving what we were told to
//     block.
//
//   - gatePayload — coverage of re-uploads. Republishing one payload under a
//     new name produces a new infoHash and walks straight past the
//     per-infoHash check; the content fingerprint catches it. Soft by design:
//     the hard gate has already run, and taking the service down when this
//     lookup is flaky would be the worse trade. With the torrent in hand
//     (pt != nil) the fingerprint is derived and cached on the spot; without
//     it, only an already-cached fingerprint is consulted — deriving needs a
//     full pull+parse, which regressed Files from an 11ms to a 28ms median in
//     production, and measured 147 derives per 154 Files responses when
//     warmed in the background (listings are overwhelmingly for torrents
//     seen once), so cold requests deliberately pass unchecked on this arm.
//
// Per-RPC matrix. A row that skips an arm is a DECISION, not an omission:
//
//	RPC          infoHash  payload            why the row looks like this
//	Push         yes       yes (from bytes)   refuse at ingest, never store
//	Pull         yes       yes (from bytes)   the .torrent that enables downloading
//	Files        yes       cached-only        answers from the manifest, never
//	                                          fetches a .torrent of its own
//	Fingerprint  yes       no                 consumers are abuse tooling; the
//	                                          fingerprint of a payload-blocked
//	                                          torrent is exactly what they ask for
//	Touch        no        no                 only refreshes storage TTL; serving
//	                                          is gated at Pull/Files
//
// Covered by TestAbuseGateMatrix — an RPC dropping out of its row turns a
// test red, not an audit finding.

// gateInfoHash refuses a torrent whose own infoHash is blocked.
func (s *Server) gateInfoHash(ctx context.Context, h string, hLog *log.Entry, t time.Time) error {
	if s.a == nil {
		return nil
	}
	abused, err := s.a.Get(ctx, h)
	if err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Error("failed to check abuse")
		return errors.Wrapf(err, "failed to check abuse infoHash=%v", h)
	}
	if abused {
		hLog.WithField("duration", time.Since(t)).Warn("abused")
		return status.Errorf(codes.PermissionDenied, "restricted by the rightholder infoHash=%v", h)
	}
	return nil
}

// gatePayload refuses a torrent whose PAYLOAD is blocked under another
// infoHash. pt != nil means the torrent bytes are in hand: the fingerprint is
// derived directly and persisted for the read paths — this is the only place
// one is ever derived, so it is also what lets Files check at all.
func (s *Server) gatePayload(ctx context.Context, h string, pt *parsedTorrent, hLog *log.Entry, t time.Time) error {
	if s.a == nil {
		return nil
	}
	var blob []byte
	if pt != nil {
		var err error
		blob, err = buildFingerprintParsed(pt)
		if err != nil {
			hLog.WithField("duration", time.Since(t)).WithError(err).Warn("failed to derive fingerprint, skipping payload check")
			return nil
		}
		s.s.CacheFingerprint(context.WithoutCancel(ctx), h, blob)
	} else {
		var err error
		blob, err = s.s.CachedFingerprint(ctx, h)
		if err != nil {
			// Not derived yet, and deliberately not derived here — see the
			// numbers in the package comment above.
			return nil
		}
	}
	digest := fingerprintDigest(blob)
	if len(digest) == 0 {
		return nil
	}
	blocked, err := s.a.CheckFingerprint(ctx, digest)
	if err != nil {
		hLog.WithField("duration", time.Since(t)).WithError(err).Warn("failed to check payload abuse, skipping")
		return nil
	}
	if blocked {
		hLog.WithField("duration", time.Since(t)).Warn("payload abused under another infoHash")
		return status.Errorf(codes.PermissionDenied, "restricted by the rightholder infoHash=%v", h)
	}
	return nil
}
