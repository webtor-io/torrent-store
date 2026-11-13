package main

import (
	"encoding/hex"
	"flag"
	"strings"
	"testing"

	"github.com/urfave/cli"
	"github.com/webtor-io/torrent-store/privaterecord"
	s "github.com/webtor-io/torrent-store/services"
	p "github.com/webtor-io/torrent-store/services/providers"
)

// otherTier stands for Redis: a shared provider that is not S3. Never called.
type otherTier struct{ s.StoreProvider }

// flagsContext builds a *cli.Context from s.RegisterPrivateFlags with the given
// values, set through the env vars the chart sets.
func flagsContext(t *testing.T, key, previous string) *cli.Context {
	t.Helper()
	t.Setenv("PRIVATE_TRACKERS_KEY", key)
	t.Setenv("PRIVATE_TRACKERS_KEY_PREVIOUS", previous)
	set := flag.NewFlagSet(t.Name(), flag.ContinueOnError)
	for _, f := range s.RegisterPrivateFlags(nil) {
		f.Apply(set)
	}
	return cli.NewContext(nil, set, nil)
}

func TestPrivateRecordsFromFlags(t *testing.T) {
	cur := hex.EncodeToString(privaterecord.Vector.Key)
	noS3 := []s.StoreProvider{otherTier{}}
	withS3 := []s.StoreProvider{otherTier{}, &p.S3{}}
	for _, c := range []struct {
		name, key, prev string
		shared          []s.StoreProvider
		wantErr, on     bool
	}{
		{"unset", "", "", withS3, false, false},
		{"key without S3", cur, "", noS3, false, false},
		{"key with S3", cur, "", withS3, false, true},
		{"bad key", "00", "", withS3, true, false},
		{"previous only", "", cur, withS3, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, err := privateRecords(flagsContext(t, c.key, c.prev), c.shared)
			if (err != nil) != c.wantErr || (err == nil && r.Available() != c.on) {
				t.Fatalf("err=%v available=%v; want err=%v on=%v", err, err == nil && r.Available(), c.wantErr, c.on)
			}
			if err != nil && strings.Contains(err.Error(), cur) {
				t.Fatal("FATAL text carries the key")
			}
		})
	}
}
