package providers

import (
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/urfave/cli"
	cs "github.com/webtor-io/common-services"
	ss "github.com/webtor-io/torrent-store/services"
)

// newTestS3 points an S3 provider at h through the same flags serve uses
// (aws-endpoint=<httptest URL>, aws-no-ssl, use-s3, aws-bucket=torrent-store,
// static fake credentials, aws-region=us-east-1).
func newTestS3(t *testing.T, h http.HandlerFunc) *S3 {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	for _, f := range RegisterS3Flags(cs.RegisterS3ClientFlags(nil)) {
		f.Apply(fs)
	}
	err := fs.Parse([]string{
		"--aws-endpoint=" + srv.URL,
		"--aws-no-ssl",
		"--" + S3UseFlag,
		"--" + AWSBucketFlag + "=torrent-store",
		"--aws-access-key-id=test",
		"--aws-secret-access-key=test",
		"--aws-region=us-east-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	c := cli.NewContext(nil, fs, nil)
	return NewS3(c, cs.NewS3Client(c, srv.Client()))
}

func TestS3ObjectsMissingIsNotAnError(t *testing.T) {
	ctx := t.Context()
	s := newTestS3(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `<Error><Code>NoSuchKey</Code><Message>no</Message></Error>`)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	if _, err := s.GetObject(ctx, "private/u/h"); !errors.Is(err, ss.ErrNotFound) {
		t.Fatalf("GetObject = %v, want ErrNotFound", err)
	}
	if err := s.DeleteObject(ctx, "private/u/h"); err != nil {
		t.Fatalf("DeleteObject = %v", err)
	}
}

func TestS3ObjectsOtherErrorsAreErrors(t *testing.T) {
	ctx := t.Context()
	s := newTestS3(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // not retried by the SDK, unlike 5xx
		_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code><Message>no</Message></Error>`)
	})
	if _, err := s.GetObject(ctx, "k"); err == nil || errors.Is(err, ss.ErrNotFound) {
		t.Fatalf("GetObject = %v, want a non-NotFound error", err)
	}
	if _, err := s.ListObjects(ctx, "private/u/"); err == nil {
		t.Fatal("ListObjects: want an error")
	}
}

func TestS3ListObjectsFollowsPages(t *testing.T) {
	ctx := t.Context()
	s := newTestS3(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/torrent-store" || q.Get("list-type") != "2" || q.Get("prefix") != "private/u/" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch q.Get("continuation-token") {
		case "":
			_, _ = io.WriteString(w, `<ListBucketResult><Name>torrent-store</Name><Prefix>private/u/</Prefix>`+
				`<IsTruncated>true</IsTruncated><NextContinuationToken>t2</NextContinuationToken>`+
				`<Contents><Key>private/u/a</Key></Contents></ListBucketResult>`)
		case "t2":
			_, _ = io.WriteString(w, `<ListBucketResult><Name>torrent-store</Name><Prefix>private/u/</Prefix>`+
				`<IsTruncated>false</IsTruncated>`+
				`<Contents><Key>private/u/b</Key></Contents></ListBucketResult>`)
		default:
			t.Errorf("unexpected continuation-token %q", q.Get("continuation-token"))
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	got, err := s.ListObjects(ctx, "private/u/")
	if err != nil || !reflect.DeepEqual(got, []string{"private/u/a", "private/u/b"}) {
		t.Fatalf("ListObjects = %q, %v", got, err)
	}
}
