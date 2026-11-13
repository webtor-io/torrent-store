package main

import (
	"fmt"
	"net/http"

	"github.com/urfave/cli"
	cs "github.com/webtor-io/common-services"
	"github.com/webtor-io/torrent-store/privaterecord"
	s "github.com/webtor-io/torrent-store/services"
	p "github.com/webtor-io/torrent-store/services/providers"
)

// sharedProviders wires the tiers every command uses — Redis (fast, shared)
// and S3 (durable) — from the CLI flags, in that walk order. Keeping serve
// and the fingerprints backfill on one constructor is what keeps their flag
// sets from drifting apart.
//
// Badger is deliberately not built here: it is the node-local L1 that only
// serve adds, and the backfill must not open it — it holds nothing the shared
// tiers lack, and opening the same directory would collide with the running
// pod's instance.
//
// The returned cleanup closes the Redis client and must be deferred.
func sharedProviders(c *cli.Context, httpCl *http.Client) ([]s.StoreProvider, func()) {
	var providers []s.StoreProvider

	redisCl := cs.NewRedisClient(c)
	if redis := p.NewRedis(c, redisCl); redis != nil {
		providers = append(providers, redis)
	}

	s3Cl := cs.NewS3Client(c, httpCl)
	if s3 := p.NewS3(c, s3Cl); s3 != nil {
		providers = append(providers, s3)
	}

	return providers, func() { redisCl.Close() }
}

// privateRecords parses the key flags (an error is FATAL: serve returns it) and
// finds the S3 provider among shared; no S3 provider means a nil RecordObjects.
// shared never holds a nil *p.S3, so obj is a true nil interface without S3,
// never a typed nil that would look available.
func privateRecords(c *cli.Context, shared []s.StoreProvider) (*s.PrivateRecords, error) {
	keys, err := privaterecord.ParseKeys(c.String(s.PrivateTrackersKeyFlag), c.String(s.PrivateTrackersKeyPreviousFlag))
	if err != nil {
		return nil, fmt.Errorf("PRIVATE_TRACKERS_KEY: %w", err)
	}
	var obj s.RecordObjects
	for _, sp := range shared {
		if s3, ok := sp.(*p.S3); ok {
			obj = s3
		}
	}
	return s.NewPrivateRecords(obj, keys), nil
}
