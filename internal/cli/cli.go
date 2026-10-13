// Package cli implements the yodea command line for cmd/yodea.
//
// Each subcommand has its own file holding its help text, an options type
// with its flags, an argument check that does no I/O, and the run step.
// command.go holds the table that dispatch, 'yodea help <command>',
// '<command> --help' and the overview all read from.
package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/g-imhoff/yodea/internal/client"
)

// IOStreams are the streams a command reads and writes. main passes the
// process streams; tests pass buffers.
type IOStreams struct {
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer
}

// Main runs yodea with args (without the program name) and returns the
// process exit status. getenv, normally os.Getenv, reads the variables
// this package uses (YODEA_EMAIL, YODEA_PASSWORD); the client package
// still reads its own (YODEA_SERVER, YODEA_DEV, YODEA_SESSION_FILE).
func Main(args []string, streams IOStreams, getenv func(string) string) int {
	a := &app{io: streams, getenv: getenv}
	if err := a.run(args); err != nil {
		fmt.Fprintln(streams.ErrOut, "yodea:", err)
		return 1
	}
	return 0
}

// app is what every command can use: the streams, the environment and
// the global options.
type app struct {
	io     IOStreams
	getenv func(string) string
	global globalOptions
}

// globalOptions are the options that come before the command.
type globalOptions struct {
	server    string // raw --server value
	serverSet bool   // --server was given, even as ""
}

// run dispatches args: an optional leading --server, then help or a
// command.
func (a *app) run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: yodea [--server URL] <%s> [options]", strings.Join(commandNames(), "|"))
	}
	g, rest, err := parseGlobal(args)
	if err != nil {
		return err
	}
	a.global = g
	switch rest[0] {
	case "-h", "-help", "--help", "help":
		return a.help(rest[1:])
	}
	c := lookup(rest[0])
	if c == nil {
		return unknownCommand(rest[0])
	}
	return a.runCommand(c, rest[1:])
}

// parseGlobal takes a leading --server URL or --server=URL (one or two
// dashes) off args and returns the rest, which starts with the command.
// Only args[0] is checked. Anything else that starts with a dash,
// including a second --server, is left for dispatch, which treats it as
// help or an unknown command.
func parseGlobal(args []string) (globalOptions, []string, error) {
	errUsage := errors.New("usage: yodea --server URL <command> [options]")
	arg := args[0]
	switch {
	case arg == "--server" || arg == "-server":
		if len(args) < 3 {
			return globalOptions{}, nil, errUsage
		}
		return globalOptions{server: args[1], serverSet: true}, args[2:], nil
	case strings.HasPrefix(arg, "--server=") || strings.HasPrefix(arg, "-server="):
		_, url, _ := strings.Cut(arg, "=")
		if url == "" || len(args) < 2 {
			return globalOptions{}, nil, errUsage
		}
		return globalOptions{server: url, serverSet: true}, args[1:], nil
	}
	return globalOptions{}, args, nil
}

// serverURL resolves --server with client.ResolveServer.
func (a *app) serverURL() string {
	return client.ResolveServer(a.global.server)
}

// authed returns a client for the saved session. An explicit --server
// always wins. The saved session server is used only when no flag and no
// YODEA_SERVER and no YODEA_DEV pick one.
func (a *app) authed() (*client.Client, error) {
	sess, err := client.LoadSession()
	if err != nil {
		return nil, err
	}
	srv := client.EffectiveServer(a.serverURL(), a.global.serverSet, sess.Server)
	return client.New(srv, sess.Token), nil
}
