package cli

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/g-imhoff/yodea/internal/client"
)

var initCmd = &command{
	name:     "init",
	synopsis: "init [--dir D] [--force] [--link] [project]",
	long: "Scaffold a fresh Vite React TS app, or link an existing folder as-is.\n" +
		"Linking writes only yodea.json there; scaffolding never overwrites\n" +
		"existing files. Without a project name the folder base name wins.\n" +
		"Flags must come before the project name.",
	short:      "scaffold a fresh Vite React TS app, or link an existing folder (writes only yodea.json there; never overwrites user files)",
	newOptions: func() options { return &initOptions{} },
}

type initOptions struct {
	dir     string
	force   bool
	link    bool
	project string // "" means the folder base name
}

func (o *initOptions) setFlags(fs *flag.FlagSet, _ func(string) string) {
	fs.StringVar(&o.dir, "dir", ".", "target folder")
	fs.BoolVar(&o.force, "force", false, "allow a non-empty dir (existing files are still never overwritten)")
	fs.BoolVar(&o.link, "link", false, "link this folder as-is instead of scaffolding")
}

func (o *initOptions) parseArgs(c *command, args []string) error {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return &usageError{fmt.Sprintf("flag %q must come before the project name; usage: %s", arg, c.usage())}
		}
	}
	if len(args) > 1 {
		return &usageError{fmt.Sprintf("init takes at most one project name (got %d); put flags before the project name; usage: %s", len(args), c.usage())}
	}
	if len(args) == 1 {
		o.project = args[0]
	}
	return nil
}

func (o *initOptions) run(a *app) error {
	project := o.project
	if project == "" {
		abs, err := filepath.Abs(o.dir)
		if err != nil {
			return err
		}
		project = filepath.Base(abs)
	}
	if err := client.CheckProject(project); err != nil {
		return fmt.Errorf("%w; pass a valid name: yodea init NAME", err)
	}
	if err := client.Init(o.dir, project, o.force, o.link); err != nil {
		return err
	}
	fmt.Fprintf(a.io.Out, "initialized %q in %q\n", project, o.dir)
	return nil
}
