package cli

import (
	"flag"
	"fmt"
	"path/filepath"

	"github.com/g-imhoff/yodea/internal/client"
)

var pushCmd = &command{
	name:       "push",
	synopsis:   "push [--dir D] [--project P] [--static]",
	usesServer: true,
	long: "Check the folder is a Vite React TS app unless --static, pack dist/ (index.html\n" +
		"required) as a raw gzipped tar, and upload it. Prints the preview\n" +
		"URL plus file and byte counts.",
	short:      "publish an already built dist/ without a build step; default validation checks React TS before packing and uploading",
	newOptions: func() options { return &pushOptions{} },
}

type pushOptions struct {
	dir     string
	project string // "" means yodea.json, then the folder name
	static  bool
}

func (o *pushOptions) setFlags(fs *flag.FlagSet, _ func(string) string) {
	fs.StringVar(&o.dir, "dir", ".", "project folder")
	fs.StringVar(&o.project, "project", "", "project name (default: yodea.json, then folder name)")
	fs.BoolVar(&o.static, "static", false, "publish prepared dist/ without React TypeScript validation")
}

func (o *pushOptions) parseArgs(c *command, args []string) error {
	if len(args) > 0 {
		return c.usageError("got unexpected argument %q; push uploads an already built dist/ and does not build it", args[0])
	}
	return nil
}

func (o *pushOptions) run(a *app) error {
	cl, err := a.authed()
	if err != nil {
		return err
	}
	if !o.static {
		if err := client.ValidateReactTS(o.dir); err != nil {
			return err
		}
	}
	project, err := client.ReadProject(o.dir, o.project)
	if err != nil {
		return err
	}
	res, err := cl.Deploy(project, filepath.Join(o.dir, "dist"))
	if err != nil {
		return err
	}
	fmt.Fprintf(a.io.Out, "pushed %q: %q (%d files, %d bytes)\n", res.Project, res.URL, res.Files, res.Bytes)
	return nil
}
