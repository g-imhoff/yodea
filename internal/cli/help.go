package cli

import (
	"flag"
	"fmt"
	"strings"
)

// help prints the overview, or one command's help when args name it. A
// topic that is "help" or starts with "-" shows the overview too.
func (a *app) help(args []string) error {
	if len(args) == 0 || args[0] == "help" || strings.HasPrefix(args[0], "-") {
		a.printOverview()
		return nil
	}
	if len(args) > 1 {
		return &usageError{fmt.Sprintf("usage: yodea help [command] (takes at most one topic, got %d)", len(args))}
	}
	c := lookup(args[0])
	if c == nil {
		return unknownCommand(args[0])
	}
	fs := c.flagSet(a, c.newOptions())
	fs.SetOutput(a.io.Out)
	fs.Usage()
	return nil
}

// printHelp writes c's help to fs's output: the synopsis, what the command
// does, then the flag defaults when it has flags.
func (c *command) printHelp(fs *flag.FlagSet) {
	fmt.Fprintf(fs.Output(), "usage: %s\n\n%s\n", c.usage(), c.long)
	hasFlags := false
	fs.VisitAll(func(*flag.Flag) { hasFlags = true })
	if hasFlags {
		fmt.Fprintln(fs.Output(), "\nOptions:")
		fs.PrintDefaults()
	}
}

func (a *app) printOverview() {
	width := 0
	for _, c := range commands {
		if len(c.synopsis) > width {
			width = len(c.synopsis)
		}
	}
	var b strings.Builder
	b.WriteString("yodea [--server URL] <command> [options]\n\n")
	for _, c := range commands {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, c.synopsis, c.short)
	}
	b.WriteString("\nRun 'yodea <command> --help' or 'yodea help <command>' for command details.\n\n" +
		"Server: --server URL (or --server=URL), else $YODEA_SERVER, else localhost:8093 when $YODEA_DEV=1, else production.\n")
	fmt.Fprint(a.io.Out, b.String())
}
