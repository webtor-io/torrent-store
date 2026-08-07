package main

import (
	"net"
	"net/http"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	cs "github.com/webtor-io/common-services"
	s "github.com/webtor-io/torrent-store/services"
	p "github.com/webtor-io/torrent-store/services/providers"
)

func makeServeCMD() cli.Command {
	serveCmd := cli.Command{
		Name:    "serve",
		Aliases: []string{"s"},
		Usage:   "Serves web server",
		Action:  serve,
	}
	configureServe(&serveCmd)
	return serveCmd
}

func configureServe(c *cli.Command) {
	c.Flags = cs.RegisterProbeFlags(c.Flags)
	c.Flags = cs.RegisterPromFlags(c.Flags)
	c.Flags = cs.RegisterS3ClientFlags(c.Flags)
	c.Flags = cs.RegisterRedisClientFlags(c.Flags)
	c.Flags = cs.RegisterPprofFlags(c.Flags)
	c.Flags = s.RegisterGRPCFlags(c.Flags)
	c.Flags = p.RegisterBadgerFlags(c.Flags)
	c.Flags = p.RegisterRedisFlags(c.Flags)
	c.Flags = p.RegisterS3Flags(c.Flags)
	c.Flags = s.RegisterAbuseClientFlags(c.Flags)
	c.Flags = s.RegisterAbuseFlags(c.Flags)
	c.Flags = s.RegisterStoplistFlags(c.Flags)
	c.Flags = s.RegisterServerFlags(c.Flags)
}

func serve(c *cli.Context) (err error) {
	stoplist, err := s.NewStoplist(c)
	if err != nil {
		return
	}

	var servers []cs.Servable

	// Setting Probe
	probe := cs.NewProbe(c)
	if probe != nil {
		servers = append(servers, probe)
		defer probe.Close()
	}

	// Setting Prom (stoplist hit metrics)
	prom := cs.NewProm(c)
	if prom != nil {
		servers = append(servers, prom)
		defer prom.Close()
	}

	// Setting Pprof
	pprof := cs.NewPprof(c)
	if pprof != nil {
		servers = append(servers, pprof)
		defer pprof.Close()
	}

	// Setting Badger Provider — the node-local L1, serve-only
	badger, err := p.NewBadger(c)
	if err != nil {
		return
	}
	defer badger.Close()

	// Setting HTTP Client
	myTransport := &http.Transport{
		MaxIdleConns:        500,
		MaxIdleConnsPerHost: 500,
		MaxConnsPerHost:     500,
		IdleConnTimeout:     90 * time.Second,
		Dial: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 15 * time.Minute,
		}).Dial,
	}
	cl := &http.Client{
		Timeout:   10 * time.Second,
		Transport: myTransport,
	}

	// Setting shared providers (Redis + S3)
	shared, closeShared := sharedProviders(c, cl)
	defer closeShared()

	// Setting Store
	store := s.NewStore(append([]s.StoreProvider{badger}, shared...))

	// Setting Abuse Client
	aCl := s.NewAbuseClient(c)

	// Setting Abuse
	abuse := s.NewAbuse(c, aCl)

	// Setting Server
	server := s.NewServer(store, abuse, stoplist, s.ParseDefaultTrackers(c))

	// Setting GRPC Server
	grpcServer := s.NewGRPCServer(c, server)
	servers = append(servers, grpcServer)
	defer grpcServer.Close()

	// Setting ServeService
	serve := cs.NewServe(servers...)

	// And SERVE!
	err = serve.Serve()
	if err != nil {
		log.WithError(err).Error("got server error")
	}
	return
}
