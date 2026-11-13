package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/webtor-io/torrent-store/privaterecord"
	pb "github.com/webtor-io/torrent-store/proto"
)

// RecordObjects is the S3 bucket as private records see it: one object per key,
// no tiers, no caches. *providers.S3 is the implementation; tests use memory.
type RecordObjects interface {
	GetObject(ctx context.Context, key string) ([]byte, error) // ErrNotFound only on 404
	PutObject(ctx context.Context, key string, body []byte) error
	DeleteObject(ctx context.Context, key string) error               // an absent key is not an error
	ListObjects(ctx context.Context, prefix string) ([]string, error) // keys of every page, in key order
}

// PrivateRecords keeps one sealed tracker record per (hash, uid) in S3.
type PrivateRecords struct {
	obj  RecordObjects
	keys [][]byte // current first, then previous
}

// NewPrivateRecords logs one Info line about the state:
//
//	obj != nil, keys set -> "private torrents: on"
//	keys == nil          -> "private torrents: off, PRIVATE_TRACKERS_KEY is not set"
//	obj == nil, keys set -> "private torrents: off, USE_S3 is not set"
func NewPrivateRecords(obj RecordObjects, keys [][]byte) *PrivateRecords {
	r := &PrivateRecords{obj: obj, keys: keys}
	switch {
	case len(keys) == 0:
		log.Info("private torrents: off, PRIVATE_TRACKERS_KEY is not set")
	case obj == nil:
		log.Info("private torrents: off, USE_S3 is not set")
	default:
		log.Info("private torrents: on")
	}
	return r
}

// Available is false on a nil receiver too.
func (r *PrivateRecords) Available() bool {
	return r != nil && r.obj != nil && len(r.keys) > 0
}

// record is the S3 object at private/<uid>/<hash>.
type record struct {
	V         int       `json:"v"`
	TiersEnc  []byte    `json:"tiers_enc"`
	Hosts     []string  `json:"hosts"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

var errUnreadable = errors.New("private record unreadable")

func recordKey(uid, h string) string { return userPrefix(uid) + h }
func userPrefix(uid string) string   { return "private/" + uid + "/" }

// upsert applies the upsert rules; write=false is dry_run: same outcome, nothing written.
func (r *PrivateRecords) upsert(ctx context.Context, h, uid string, tiers [][]string, source string, write bool) (string, error) {
	rec, old, err := r.open(ctx, h, uid)
	outcome := "updated"
	switch {
	case errors.Is(err, ErrNotFound):
		outcome = "inserted"
	case errors.Is(err, errUnreadable): // its owner's upload replaces it
	case err != nil:
		return "", err
	case bytes.Equal(privaterecord.Canonical(old), privaterecord.Canonical(tiers)):
		if rec.Source == source {
			return "unchanged", nil
		}
		outcome = "unchanged" // rewritten for the caller's source
	}
	if !write {
		return outcome, nil
	}
	now := time.Now().UTC()
	created := now
	if rec != nil && !rec.CreatedAt.IsZero() {
		created = rec.CreatedAt
	}
	blob, err := privaterecord.Seal(r.keys[0], h, uid, tiers)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(record{
		V:         1,
		TiersEnc:  blob,
		Hosts:     privaterecord.Hosts(tiers),
		Source:    source,
		CreatedAt: created,
		UpdatedAt: now,
	})
	if err != nil {
		return "", err
	}
	if err := r.obj.PutObject(ctx, recordKey(uid, h), body); err != nil {
		return "", err
	}
	return outcome, nil
}

// open GETs and opens one record: ErrNotFound, errUnreadable (wrapped with a class
// bad_record|bad_json, never Open's text), or the S3 error as is. With errUnreadable
// the record is still returned when its JSON decoded.
func (r *PrivateRecords) open(ctx context.Context, h, uid string) (*record, [][]string, error) {
	body, err := r.obj.GetObject(ctx, recordKey(uid, h))
	if err != nil {
		return nil, nil, err
	}
	var rec record
	if err := json.Unmarshal(body, &rec); err != nil {
		return nil, nil, unreadable(h, uid, "bad_json")
	}
	if rec.V != 1 {
		return &rec, nil, unreadable(h, uid, "bad_record")
	}
	tiers, err := privaterecord.Open(r.keys, h, uid, rec.TiersEnc)
	if err != nil {
		return &rec, nil, unreadable(h, uid, "bad_record")
	}
	return &rec, tiers, nil
}

func unreadable(h, uid, class string) error {
	log.WithFields(log.Fields{"infohash": h, "acct": privaterecord.Acct(uid), "class": class}).Warn("private record unreadable")
	return fmt.Errorf("%w: %s", errUnreadable, class)
}

// list reads every record of uid without opening one: hosts, source and the
// times are plain JSON. A record gone between LIST and GET is skipped, one whose
// JSON does not decode is listed by its hash alone, and any S3 error fails the
// whole call: an export is never silently partial.
func (r *PrivateRecords) list(ctx context.Context, uid string) ([]*pb.PrivateRecordInfo, error) {
	keys, err := r.obj.ListObjects(ctx, userPrefix(uid))
	if err != nil {
		return nil, err
	}
	out := make([]*pb.PrivateRecordInfo, 0, len(keys))
	for _, k := range keys {
		body, err := r.obj.GetObject(ctx, k)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		info := &pb.PrivateRecordInfo{InfoHash: strings.TrimPrefix(k, userPrefix(uid))}
		var rec record
		if json.Unmarshal(body, &rec) == nil {
			info.Hosts, info.Source = rec.Hosts, rec.Source
			info.CreatedAt, info.UpdatedAt = rec.CreatedAt.Unix(), rec.UpdatedAt.Unix()
		}
		out = append(out, info)
	}
	return out, nil
}

// delete reports whether the record was there. GET, not HEAD, keeps
// RecordObjects at four methods.
func (r *PrivateRecords) delete(ctx context.Context, h, uid string) (bool, error) {
	if _, err := r.obj.GetObject(ctx, recordKey(uid, h)); errors.Is(err, ErrNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, r.obj.DeleteObject(ctx, recordKey(uid, h))
}

// deleteByUser deletes every record of uid and returns how many. On an error
// some may be gone already; a retry deletes the rest.
func (r *PrivateRecords) deleteByUser(ctx context.Context, uid string) (int32, error) {
	keys, err := r.obj.ListObjects(ctx, userPrefix(uid))
	if err != nil {
		return 0, err
	}
	for i, k := range keys {
		if err := r.obj.DeleteObject(ctx, k); err != nil {
			return int32(i), err
		}
	}
	return int32(len(keys)), nil
}
