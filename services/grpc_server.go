package services

import (
	"fmt"
	"net"

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
)

type GRPCServer struct {
	host string
	port int
	ln   net.Listener
	s    *Server
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

	gs := grpc.NewServer()

	pb.RegisterTorrentStoreServer(gs, s.s)

	reflection.Register(gs)
	logrus.Infof("serving GRPC at %v", addr)
	return gs.Serve(ln)
}

func (s *GRPCServer) Close() {
	if s.ln != nil {
		s.ln.Close()
	}
}
