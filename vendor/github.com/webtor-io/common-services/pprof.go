package services

import (
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

type Pprof struct {
	host string
	port int
	ln   net.Listener
}

const (
	pprofHostFlag = "pprof-host"
	pprofPortFlag = "pprof-port"
)

func RegisterPprofFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:  pprofHostFlag,
			Usage: "pprof listening host",
			Value: "",
		},
		cli.IntFlag{
			Name:  pprofPortFlag,
			Usage: "pprof listening port",
			Value: 8082,
		},
	)
}

func NewPprof(c *cli.Context) *Pprof {
	return &Pprof{host: c.String(pprofHostFlag), port: c.Int(pprofPortFlag)}
}

func (s *Pprof) Serve() error {
	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.Wrap(err, "failed to probe listen to tcp connection")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", pprof.Index)
	mux.HandleFunc("/cmdline", pprof.Cmdline)
	mux.HandleFunc("/profile", pprof.Profile)
	mux.HandleFunc("/symbol", pprof.Symbol)
	mux.HandleFunc("/trace", pprof.Trace)

	mux.Handle("/goroutine", pprof.Handler("goroutine"))
	mux.Handle("/heap", pprof.Handler("heap"))
	mux.Handle("/threadcreate", pprof.Handler("threadcreate"))
	mux.Handle("/block", pprof.Handler("block"))

	log.Infof("serving pprof at %v", addr)
	return http.Serve(ln, mux)
}

func (s *Pprof) Close() {
	if s.ln != nil {
		s.ln.Close()
	}
}
