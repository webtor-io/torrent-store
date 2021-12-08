package services

import (
	"fmt"
	"sync"

	"github.com/urfave/cli"
	as "github.com/webtor-io/abuse-store/abuse-store"
	"google.golang.org/grpc"
)

const (
	AbuseClientHostFlag = "abuse-host"
	AbuseClientPortFlag = "abuse-port"
)

func RegisterAbuseClientFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:   AbuseClientHostFlag,
			Usage:  "abuse store host",
			Value:  "",
			EnvVar: "ABUSE_STORE_SERVICE_HOST",
		},
		cli.IntFlag{
			Name:   AbuseClientPortFlag,
			Usage:  "port of the redis service",
			Value:  50051,
			EnvVar: "ABUSE_STORE_SERVICE_PORT",
		},
	)
}

type AbuseClient struct {
	once sync.Once
	cl   as.AbuseStoreClient
	err  error
	host string
	port int
	conn *grpc.ClientConn
}

func NewAbuseClient(c *cli.Context) *AbuseClient {
	return &AbuseClient{
		host: c.String(AbuseClientHostFlag),
		port: c.Int(AbuseClientPortFlag),
	}
}

func (s *AbuseClient) Get() (as.AbuseStoreClient, error) {
	s.once.Do(func() {
		addr := fmt.Sprintf("%s:%d", s.host, s.port)
		conn, err := grpc.Dial(addr, grpc.WithInsecure())
		if err != nil {
			s.err = err
			return
		}
		s.conn = conn
		s.cl = as.NewAbuseStoreClient(conn)
	})
	return s.cl, s.err
}

func (s *AbuseClient) Close() {
	if s.conn != nil {
		s.conn.Close()
	}
}
