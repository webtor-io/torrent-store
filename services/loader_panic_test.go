package services

import (
	"bytes"
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	pb "github.com/webtor-io/torrent-store/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// panicOnceProvider panics on its first Pull, the way a provider bug would.
type panicOnceProvider struct {
	*fakeProvider
	panicked atomic.Bool
}

func (p *panicOnceProvider) Pull(ctx context.Context, h string) ([]byte, error) {
	if p.panicked.CompareAndSwap(false, true) {
		panic("boom")
	}
	return p.fakeProvider.Pull(ctx, h)
}

// A panic in a loader used to escape Get and leave the key "running": with
// lazymap a258d93 every later Pull of that infohash blocked forever. It must
// come back as an error, and the next Pull must load.
func TestPullAfterLoaderPanicDoesNotHang(t *testing.T) {
	p := &panicOnceProvider{fakeProvider: newFakeProvider("mem", true)}
	p.torrents["feed0005"] = []byte("torrent")
	s := NewStore([]StoreProvider{p})

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("panic escaped Pull: %v", r)
			}
		}()
		_, err := s.Pull(context.Background(), "feed0005")
		if err == nil || !strings.Contains(err.Error(), "lazymap: panic in f: boom") {
			t.Errorf("err = %v, want the panic as an error", err)
		}
	}()

	got := make(chan []byte, 1)
	go func() {
		b, _ := s.Pull(context.Background(), "feed0005")
		got <- b
	}()
	select {
	case b := <-got:
		if string(b) != "torrent" {
			t.Fatalf("retry = %q, want the stored torrent", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second Pull of the same key hung")
	}
}

// Over the wire: the client gets a one-line error with no stack (source paths
// stay in), the stack lands in the log, and the retry is served.
func TestLoaderPanicReachesClientWithoutStack(t *testing.T) {
	p := &panicOnceProvider{fakeProvider: newFakeProvider("mem", true)}
	raw := makeRawTorrent(t, validInfo())
	mi, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	h := mi.HashInfoBytes().HexString()
	p.torrents[h] = raw

	hook := logtest.NewGlobal()
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })

	gs := (&GRPCServer{s: NewServer(NewStore([]StoreProvider{p}), nil, nil, nil)}).newServer()
	lis := bufconn.Listen(1 << 20)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	cl := pb.NewTorrentStoreClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = cl.Pull(ctx, &pb.PullRequest{InfoHash: h})
	st := status.Convert(err)
	if st.Code() != codes.Unknown || !strings.Contains(st.Message(), "lazymap: panic in f: boom") {
		t.Fatalf("first Pull: %v, want Unknown carrying the panic value", err)
	}
	if strings.Contains(st.Message(), "goroutine ") || strings.Contains(st.Message(), ".go:") {
		t.Fatalf("stack leaked to the client: %q", st.Message())
	}
	logged := false
	for _, e := range hook.AllEntries() {
		if s, _ := e.Data["stack"].(string); strings.Contains(s, "goroutine ") {
			logged = true
		}
	}
	if !logged {
		t.Fatal("panic stack was not logged")
	}

	if _, err := cl.Pull(ctx, &pb.PullRequest{InfoHash: h}); err != nil {
		t.Fatalf("retry after panic: %v", err)
	}
}
