package cli

import "fmt"

var listCmd = &command{
	name:       "list",
	synopsis:   "list",
	usesServer: true,
	long: "List your personal sites (GET /api/sites): one quoted row per\n" +
		"project with its label and file count.",
	short:      "list your personal sites (GET /api/sites)",
	newOptions: func() options { return &listOptions{} },
}

type listOptions struct {
	noFlags
}

func (o *listOptions) parseArgs(c *command, args []string) error {
	if len(args) > 0 {
		return c.usageError("got unexpected argument %q", args[0])
	}
	return nil
}

func (o *listOptions) run(a *app) error {
	cl, err := a.authed()
	if err != nil {
		return err
	}
	sites, err := cl.List()
	if err != nil {
		return err
	}
	if len(sites) == 0 {
		fmt.Fprintln(a.io.Out, "no sites yet (yodea push to deploy one)")
		return nil
	}
	for _, s := range sites {
		fmt.Fprintf(a.io.Out, "%q\t%q\t%d files\n", s.Project, s.Label, s.Files)
	}
	return nil
}
