package services

import (
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"github.com/webtor-io/torrent-store/fingerprint"
)

// buildFingerprint derives a torrent's content fingerprints and renders them
// as the cached blob: one "kind:hex<TAB>length" line per fingerprint.
//
// Text rather than protobuf on purpose — the blob is a handful of bytes, it is
// read far more often than it is written, and staying greppable means an
// operator can read it straight out of Redis or S3 without tooling.
func buildFingerprint(torrent []byte) ([]byte, error) {
	fps, err := fingerprint.Compute(torrent)
	if err != nil {
		return nil, errors.Wrap(err, "failed to compute fingerprints")
	}
	var b strings.Builder
	for _, f := range fps {
		b.WriteString(f.String())
		b.WriteByte('\t')
		b.WriteString(strconv.FormatInt(f.Length, 10))
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

// parseFingerprint reads back what buildFingerprint wrote. Malformed or
// unknown lines are skipped rather than failing the read: the blob is derived
// data, and a future writer adding a fingerprint kind must not break older
// readers.
func parseFingerprint(blob []byte) []fingerprint.Fingerprint {
	var res []fingerprint.Fingerprint
	for _, line := range strings.Split(string(blob), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		kv := strings.SplitN(parts[0], ":", 2)
		if len(kv) != 2 || kv[0] == "" || kv[1] == "" {
			continue
		}
		f := fingerprint.Fingerprint{Kind: kv[0], Value: kv[1]}
		if len(parts) == 2 {
			if n, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
				f.Length = n
			}
		}
		res = append(res, f)
	}
	return res
}
