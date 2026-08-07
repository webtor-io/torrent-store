package main

import (
	"net/http"

	"github.com/urfave/cli"
	cs "github.com/webtor-io/common-services"
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
