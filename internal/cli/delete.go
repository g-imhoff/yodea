package cli

import "fmt"

var deleteCmd = &command{
	name:       "delete",
	synopsis:   "delete <project>",
	usesServer: true,
	long:       "Delete one project and its preview. Takes exactly one project name.",
	summary: []string{
		"delete <project>               delete one project",
	},
	newOptions: func() options { return &deleteOptions{} },
}

type deleteOptions struct {
	noFlags
	project string
}

func (o *deleteOptions) parseArgs(c *command, args []string) error {
	if len(args) != 1 {
		return c.usageError("takes exactly one project name")
	}
	o.project = args[0]
	return nil
}

func (o *deleteOptions) run(a *app) error {
	cl, err := a.authed()
	if err != nil {
		return err
	}
	if err := cl.Delete(o.project); err != nil {
		return err
	}
	fmt.Fprintf(a.io.Out, "deleted %q\n", o.project)
	return nil
}
