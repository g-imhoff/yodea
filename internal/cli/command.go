package cli

import (
	"errors"
	"flag"
	"fmt"
	"strings"
)

// command is one yodea subcommand. Each command's name and help text are
// written once, in its own file, and read by dispatch, 'yodea help
// <command>', '<command> --help' and the overview.
type command struct {
	name string
	// synopsis is the usage line without "yodea " and the global
	// [--server URL], e.g. "push [--dir D] [--project P]".
	synopsis string
	// usesServer is set for commands that talk to the server. Their help
	// shows the global [--server URL].
	usesServer bool
	long       string // what the command does, printed under the usage line
	// short is the command's one-line entry in the overview. The overview
	// is generated from synopsis and short, so the two cannot drift apart.
	short string
	// newOptions returns empty options for one run of the command.
	newOptions func() options
}

// options holds one run of a command: its flags, its arguments and the
// work. Each command defines its own options type in its file.
type options interface {
	// setFlags registers the command's flags on fs.
	setFlags(fs *flag.FlagSet, getenv func(string) string)
	// parseArgs checks and stores the arguments left after the flags. It
	// does no I/O, so bad arguments fail before anything is read or sent.
	parseArgs(c *command, args []string) error
	// run does the work.
	run(a *app) error
}

// usageError marks errors that misuse the command line: bad arguments or
// bad global options. Main prints them with exit status 2, the same code
// the flag package uses. The message keeps the "usage: ..." text the
// tests match on.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// flagError wraps a flag package failure. The flag package already wrote
// the error plus the command help to the output, so Main exits 2 without
// printing it again.
type flagError struct{ err error }

func (e *flagError) Error() string { return e.err.Error() }

func (e *flagError) Unwrap() error { return e.err }

// noFlags is embedded by the options of commands that take no flags.
type noFlags struct{}

func (noFlags) setFlags(*flag.FlagSet, func(string) string) {}

// commands is every subcommand, in overview order.
var commands = []*command{loginCmd, initCmd, pushCmd, listCmd, deleteCmd}

func lookup(name string) *command {
	for _, c := range commands {
		if c.name == name {
			return c
		}
	}
	return nil
}

func commandNames() []string {
	names := make([]string, len(commands))
	for i, c := range commands {
		names[i] = c.name
	}
	return names
}

func unknownCommand(name string) error {
	names := commandNames()
	last := len(names) - 1
	return fmt.Errorf("unknown command %q (want %s, or %s)", name, strings.Join(names[:last], ", "), names[last])
}

// runCommand parses args for c and runs it. Help stops it with success.
func (a *app) runCommand(c *command, args []string) error {
	o, err := a.parse(c, args)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	return o.run(a)
}

// parse reads c's flags and arguments from args. It returns flag.ErrHelp
// once help has been printed. When args hold -h, -help or --help, help
// (and any flag error) goes to stdout. Otherwise flag errors go to stderr,
// and so does the help the flag package prints for forms like --h or
// -help=true.
func (a *app) parse(c *command, args []string) (options, error) {
	o := c.newOptions()
	fs := c.flagSet(a, o)
	if helpRequested(args) {
		fs.SetOutput(a.io.Out)
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, err
		}
		return nil, &flagError{err}
	}
	if err := o.parseArgs(c, fs.Args()); err != nil {
		return nil, err
	}
	return o, nil
}

// helpRequested reports whether any of args is exactly -h, -help or
// --help, even after the positional arguments.
func helpRequested(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "-help" || arg == "--help" {
			return true
		}
	}
	return false
}

// flagSet returns a fresh flag set holding o's flags, with c's help as its
// usage. It writes to stderr until the caller redirects it.
func (c *command) flagSet(a *app, o options) *flag.FlagSet {
	fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
	fs.SetOutput(a.io.ErrOut)
	o.setFlags(fs, a.getenv)
	fs.Usage = func() { c.printHelp(fs) }
	return fs
}

// usage is c's usage line, as its help prints it after "usage: ".
func (c *command) usage() string {
	if c.usesServer {
		return "yodea [--server URL] " + c.synopsis
	}
	return "yodea " + c.synopsis
}

// usageError reports bad arguments to c. The message quotes c's usage
// line without the global [--server URL].
func (c *command) usageError(format string, args ...any) error {
	return &usageError{fmt.Sprintf("usage: yodea %s (%s)", c.synopsis, fmt.Sprintf(format, args...))}
}
