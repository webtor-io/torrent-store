package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli"
	pb "github.com/webtor-io/torrent-store/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func push(c pb.TorrentStoreClient, path string) error {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r, err := c.Push(ctx, &pb.PushRequest{Torrent: bytes})
	if err != nil {
		return err
	}
	fmt.Println(r.InfoHash)
	return nil
}

func touch(c pb.TorrentStoreClient, infoHash string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := c.Touch(ctx, &pb.TouchRequest{InfoHash: infoHash})
	if err != nil {
		return err
	}
	fmt.Println("Touched")
	return nil
}

func pull(c pb.TorrentStoreClient, infoHash string, path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r, err := c.Pull(ctx, &pb.PullRequest{InfoHash: infoHash})
	if err != nil {
		return err
	}
	err = os.WriteFile(path, r.Torrent, 0644)
	if err != nil {
		return err
	}
	fmt.Println("Pulled")
	return nil
}

// printFingerprints asks the store for a torrent's content fingerprints.
// Useful for spotting one payload republished under many torrent names: the
// infohashes all differ, the fingerprint does not. Served from the store's
// cache, so this costs a few bytes rather than a full .torrent transfer.
func printFingerprints(c pb.TorrentStoreClient, infoHash string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r, err := c.Fingerprint(ctx, &pb.FingerprintRequest{InfoHash: infoHash})
	if err != nil {
		return err
	}
	fmt.Printf("%s\t%d\n", r.GetValue(), r.GetLength())
	return nil
}

func files(c pb.TorrentStoreClient, infoHash string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r, err := c.Files(ctx, &pb.FilesRequest{InfoHash: infoHash})
	if err != nil {
		return err
	}
	fmt.Printf("name: %s\n", r.GetName())
	for _, f := range r.GetFiles() {
		fmt.Printf("%d\t/%s\n", f.GetLength(), strings.Join(f.GetPath(), "/"))
	}
	return nil
}

func withClient(host string, port int, action func(c pb.TorrentStoreClient) error) error {
	address := fmt.Sprintf("%s:%d", host, port)
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	c := pb.NewTorrentStoreClient(conn)
	return action(c)
}

func main() {
	app := cli.NewApp()
	app.Name = "torrent-store-client"
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
			Name:    "touch",
			Aliases: []string{"to"},
			Usage:   "touches torrent",
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "hash, ha",
					Usage: "info hash of the torrent file",
				},
			},
			Action: func(ctx *cli.Context) error {
				return withClient(ctx.GlobalString("host"), ctx.GlobalInt("port"), func(c pb.TorrentStoreClient) error {
					return touch(c, ctx.String("hash"))
				})
			},
		},
		{
			Name:    "push",
			Aliases: []string{"ps"},
			Usage:   "pushes torrent to the store",
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "input, i",
					Usage: "path to the input torrent file",
				},
			},
			Action: func(ctx *cli.Context) error {
				return withClient(ctx.GlobalString("host"), ctx.GlobalInt("port"), func(c pb.TorrentStoreClient) error {
					return push(c, ctx.String("input"))
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
		{
			Name:    "fingerprint",
			Aliases: []string{"fp"},
			Usage:   "prints content fingerprints of a torrent (same payload => same fingerprint, whatever the name)",
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "hash, ha",
					Usage: "info hash of the torrent file",
				},
			},
			Action: func(ctx *cli.Context) error {
				return withClient(ctx.GlobalString("host"), ctx.GlobalInt("port"), func(c pb.TorrentStoreClient) error {
					return printFingerprints(c, ctx.String("hash"))
				})
			},
		},
		{
			Name:    "files",
			Aliases: []string{"f"},
			Usage:   "lists the file manifest of a torrent",
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "hash, ha",
					Usage: "info hash of the torrent file",
				},
			},
			Action: func(ctx *cli.Context) error {
				return withClient(ctx.GlobalString("host"), ctx.GlobalInt("port"), func(c pb.TorrentStoreClient) error {
					return files(c, ctx.String("hash"))
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
