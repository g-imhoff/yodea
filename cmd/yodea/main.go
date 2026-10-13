// Command yodea is the yodea CLI: login, init, push, list, delete.
//
//	Server selection: --server flag, else YODEA_SERVER, else the dev
//	server http://127.0.0.1:8093 when YODEA_DEV=1, else production.
//	Session persists in the OS config dir (0600).
//
//	Deploy uploads use ONE documented form: a raw gzipped tar body with
//	Content-Type application/gzip to POST /api/sites/{project}/deploy.
//
// The commands live in internal/cli.
package main

import (
	"os"

	"github.com/g-imhoff/yodea/internal/cli"
)

func main() {
	streams := cli.IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr}
	os.Exit(cli.Main(os.Args[1:], streams, os.Getenv))
}
