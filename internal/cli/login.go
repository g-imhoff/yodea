package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/g-imhoff/yodea/internal/client"
	"golang.org/x/term"
)

var loginCmd = &command{
	name:       "login",
	synopsis:   "login --email E",
	usesServer: true,
	long: "Log in via POST /api/session and save the session (0600 file).\n" +
		"The password never comes from argv: set $YODEA_PASSWORD or type it at the hidden prompt.",
	short: "log in via POST /api/session (session saved 0600; password from $YODEA_PASSWORD or a hidden prompt)",
	newOptions: func() options { return &loginOptions{} },
}

type loginOptions struct {
	email string
}

func (o *loginOptions) setFlags(fs *flag.FlagSet, getenv func(string) string) {
	fs.StringVar(&o.email, "email", getenv("YODEA_EMAIL"), "login email ($YODEA_EMAIL)")
}

func (o *loginOptions) parseArgs(c *command, args []string) error {
	if len(args) > 0 {
		return c.usageError("login takes options only, got unexpected %q", args[0])
	}
	if o.email == "" {
		return fmt.Errorf("login needs --email (or $YODEA_EMAIL)")
	}
	return nil
}

func (o *loginOptions) run(a *app) error {
	password := a.getenv("YODEA_PASSWORD")
	if password == "" {
		var err error
		password, err = a.promptPassword()
		if err != nil {
			return err
		}
		if password == "" {
			return fmt.Errorf("login needs a password (set $YODEA_PASSWORD or type it at the prompt)")
		}
	}
	server := a.serverURL()
	if err := client.New(server, "").Login(o.email, password); err != nil {
		return err
	}
	sess, err := client.LoadSession()
	if err != nil {
		return err
	}
	who := sess.UserID
	if who == "" {
		who = o.email
	}
	fmt.Fprintf(a.io.Out, "logged in as %q (%q)\n", who, server)
	return nil
}

// promptPassword reads a hidden password from the terminal. The secret
// never comes from argv.
func (a *app) promptPassword() (string, error) {
	fmt.Fprint(a.io.ErrOut, "password: ")
	b, err := readPassword(a.io.In)
	fmt.Fprintln(a.io.ErrOut)
	if err != nil {
		return "", fmt.Errorf("reading password: %w", err)
	}
	return string(b), nil
}

// readPassword reads a line without echo. It needs a terminal, so in must
// be a file such as os.Stdin.
func readPassword(in io.Reader) ([]byte, error) {
	f, ok := in.(*os.File)
	if !ok {
		return nil, errors.New("stdin is not a terminal")
	}
	return term.ReadPassword(int(f.Fd()))
}
