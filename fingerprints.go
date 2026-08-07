package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	cs "github.com/webtor-io/common-services"
	"github.com/webtor-io/torrent-store/fingerprint"
	s "github.com/webtor-io/torrent-store/services"
	p "github.com/webtor-io/torrent-store/services/providers"
)

// makeFingerprintsCMD builds the one-off backfill helper.
//
// It exists because the Fingerprint RPC refuses blocked torrents — it inherits
// the abuse gate from Files, correctly — while the thing that needs their
// fingerprints is precisely the backfill of existing bans. Rather than open a
// permanent hole in that gate for a job that runs once, this reads the store
// directly.
//
// Reads infohashes from stdin, one per line, and writes "infohash<TAB>hex" to
// stdout. Failures go to stderr and do not stop the run: a torrent whose bytes
// are long gone simply has no fingerprint to record.
//
// Deliberately read-only. It prints a mapping; applying it to the abuse rows
// is a separate, reviewable step.
func makeFingerprintsCMD() cli.Command {
	cmd := cli.Command{
		Name:   "fingerprints",
		Usage:  "reads infohashes on stdin, writes 'infohash<TAB>fingerprint' on stdout (one-off backfill helper)",
		Action: fingerprints,
	}
	cmd.Flags = cs.RegisterS3ClientFlags(cmd.Flags)
	cmd.Flags = cs.RegisterRedisClientFlags(cmd.Flags)
	cmd.Flags = p.RegisterRedisFlags(cmd.Flags)
	cmd.Flags = p.RegisterS3Flags(cmd.Flags)
	return cmd
}

func fingerprints(c *cli.Context) error {
	httpCl := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:    50,
			IdleConnTimeout: 90 * time.Second,
			Dial: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 15 * time.Minute,
			}).Dial,
		},
	}
	// Badger is deliberately absent — see sharedProviders.
	providers, closeShared := sharedProviders(c, httpCl)
	defer closeShared()
	if len(providers) == 0 {
		return fmt.Errorf("no shared store providers configured")
	}

	store := s.NewStore(providers)
	ctx := context.Background()

	var read, ok, missing int
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		h := strings.TrimSpace(sc.Text())
		if h == "" || strings.HasPrefix(h, "#") {
			continue
		}
		read++
		torrent, err := store.Pull(ctx, h)
		if err != nil {
			missing++
			fmt.Fprintf(os.Stderr, "%s\tunavailable: %v\n", h, err)
			continue
		}
		fps, err := fingerprint.Compute(torrent)
		if err != nil || len(fps) == 0 {
			missing++
			fmt.Fprintf(os.Stderr, "%s\tunfingerprintable: %v\n", h, err)
			continue
		}
		ok++
		fmt.Printf("%s\t%s\n", h, fps[0].Value)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	log.WithField("read", read).WithField("resolved", ok).WithField("missing", missing).Info("backfill scan finished")
	return nil
}
