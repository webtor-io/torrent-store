package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"time"

	pb "bitbucket.org/vintikzzzz/torrent-store/torrent-store"
	"github.com/urfave/cli"
	"google.golang.org/grpc"
)

func push(c pb.TorrentStoreClient, path string, expire int) error {
	bytes, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r, err := c.Push(ctx, &pb.PushRequest{Torrent: bytes, Expire: int32(expire)})
	if err != nil {
		return err
	}
	fmt.Println(r.InfoHash)
	return nil
}

func pull(c pb.TorrentStoreClient, infoHash string, path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r, err := c.Pull(ctx, &pb.PullRequest{InfoHash: infoHash})
	if err != nil {
		return err
	}
	err = ioutil.WriteFile(path, r.Torrent, 0644)
	if err != nil {
		return err
	}
	return nil
}

func withClient(host string, port int, action func(c pb.TorrentStoreClient) error) error {
	address := fmt.Sprintf("%s:%d", host, port)
	conn, err := grpc.Dial(address, grpc.WithInsecure())
	if err != nil {
		return err
	}
	defer conn.Close()
	c := pb.NewTorrentStoreClient(conn)
	return action(c)
}

func main() {
	app := cli.NewApp()
	app.Name = "torrent-store-client-cli"
	app.Usage = "interacts with torrent store"
	app.Version = "0.0.1"
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:   "host, H",
			Usage:  "hostname of the torrent store",
			Value:  "localhost",
			EnvVar: "TORRENT_STORE_HOST",
		},
		cli.IntFlag{
			Name:   "port, P",
			Usage:  "port of the torrent store",
			Value:  50051,
			EnvVar: "TORRENT_STORE_PORT",
		},
	}
	app.Commands = []cli.Command{
		{
			Name:    "push",
			Aliases: []string{"ps"},
			Usage:   "pushes torrent to the store",
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "input, i",
					Usage: "path to the input torrent file",
				},
				cli.IntFlag{
					Name:  "expire, e",
					Usage: "expiration period in seconds",
				},
			},
			Action: func(ctx *cli.Context) error {
				return withClient(ctx.GlobalString("host"), ctx.GlobalInt("port"), func(c pb.TorrentStoreClient) error {
					return push(c, ctx.String("input"), ctx.Int("expire"))
				})
			},
		},
		{
			Name:    "pull",
			Aliases: []string{"pl"},
			Usage:   "pulls torrent from the store",
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "output, o",
					Usage: "path to the output torrent file",
				},
				cli.StringFlag{
					Name:  "hash, ha",
					Usage: "info hash of the torrent file",
				},
			},
			Action: func(ctx *cli.Context) error {
				return withClient(ctx.GlobalString("host"), ctx.GlobalInt("port"), func(c pb.TorrentStoreClient) error {
					return pull(c, ctx.String("hash"), ctx.String("output"))
				})
			},
		},
	}
	err := app.Run(os.Args)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
