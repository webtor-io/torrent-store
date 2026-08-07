package services

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	"google.golang.org/grpc"

	pb "github.com/webtor-io/torrent-store/proto"

	"google.golang.org/grpc/reflection"
)

const (
	grpcServerHostFlag = "grpc-host"
	grpcServerPortFlag = "grpc-port"
	grpcMaxMsgSize     = 1024 * 1024 * 50

	// gracefulStopTimeout bounds how long shutdown waits for in-flight RPCs.
	// Pulls answered from S3 are the slow tail and sit well under this; the
	// pod's terminationGracePeriodSeconds is 30, so a hard Stop() at 15s
	// still leaves room for the providers to close afterwards.
	gracefulStopTimeout = 15 * time.Second
)

type GRPCServer struct {
	host string
	port int
	ln   net.Listener
	s    *Server
	mu   sync.Mutex
	gs   *grpc.Server
}

func NewGRPCServer(c *cli.Context, s *Server) *GRPCServer {
	return &GRPCServer{host: c.String(grpcServerHostFlag), port: c.Int(grpcServerPortFlag), s: s}
}

func RegisterGRPCFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:   grpcServerHostFlag,
			Usage:  "grpc listening host",
			Value:  "",
			EnvVar: "GRPC_HOST",
		},
		cli.IntFlag{
			Name:   grpcServerPortFlag,
			Usage:  "grpc listening port",
			Value:  50051,
			EnvVar: "GRPC_PORT",
		},
	)
}

func (s *GRPCServer) Serve() error {
	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.Wrap(err, "failed to grpc listen to tcp connection")
	}
	s.ln = ln

	gs := grpc.NewServer(
		grpc.MaxRecvMsgSize(grpcMaxMsgSize),
		grpc.MaxSendMsgSize(grpcMaxMsgSize),
	)

	pb.RegisterTorrentStoreServer(gs, s.s)

	reflection.Register(gs)

	s.mu.Lock()
	s.gs = gs
	s.mu.Unlock()

	logrus.Infof("serving GRPC at %v", addr)
	return gs.Serve(ln)
}

// Close drains in-flight RPCs before returning.
//
// Closing only the listener — which is what this used to do — stops new
// connections but leaves established ones serving. Shutdown then continued
// down the defer chain and closed the providers underneath handlers that
// were still running, which is how a rollout turned into a nil-pointer panic
// inside Badger (three times between 2026-08-02 and 2026-08-06). Providers
// close after this returns, so they must be able to assume no handler is
// still using them.
func (s *GRPCServer) Close() {
	s.mu.Lock()
	gs := s.gs
	s.mu.Unlock()

	if gs == nil {
		// Never got as far as serving.
		if s.ln != nil {
			_ = s.ln.Close()
		}
		return
	}

	done := make(chan struct{})
	go func() {
		gs.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(gracefulStopTimeout):
		// A stuck stream must not hold shutdown open past the pod's grace
		// period — losing it is better than being SIGKILLed mid-close.
		logrus.Warn("grpc graceful stop timed out, forcing")
		gs.Stop()
		<-done
	}
}
