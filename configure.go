package main

import (
	"github.com/urfave/cli"
)

func configure(app *cli.App) {
	serveCmd := makeServeCMD()
	fingerprintsCmd := makeFingerprintsCMD()
	app.Commands = []cli.Command{serveCmd, fingerprintsCmd}
}
