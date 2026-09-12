// Command yodea is the yodea CLI: login, init, push, list, delete.
//
//	Server selection: --server flag, else YODEA_SERVER, else the dev
//	server http://127.0.0.1:8093 when YODEA_DEV=1, else production.
//	Session persists in the OS config dir (0600).
//
//	Deploy uploads use ONE documented form: a raw gzipped tar body with
//	Content-Type application/gzip to POST /api/sites/{project}/deploy.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/g-imhoff/yodea/internal/client"
	"golang.org/x/term"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "yodea:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: yodea [--server URL] <login|init|push|list|delete> [options]")
	}
	serverFlag := ""
	serverFlagSet := false
	rest := args
	if rest[0] == "--server" || rest[0] == "-server" {
		if len(rest) < 3 {
			return fmt.Errorf("usage: yodea --server URL <command> [options]")
		}
		serverFlag = rest[1]
		serverFlagSet = true
		rest = rest[2:]
	}
	server := client.ResolveServer(serverFlag)
	switch rest[0] {
	case "login":
		return cmdLogin(server, rest[1:])
	case "init":
		return cmdInit(rest[1:])
	case "push":
		return cmdPush(server, serverFlagSet, rest[1:])
	case "list":
		return cmdList(server, serverFlagSet, rest[1:])
	case "delete":
		return cmdDelete(server, serverFlagSet, rest[1:])
	case "-h", "-help", "--help", "help":
		return showHelp(rest[1:])
	default:
		return fmt.Errorf("unknown command %q (want login, init, push, list, or delete)", rest[0])
	}
}

func usage() {
	fmt.Println(`yodea [--server URL] <command> [options]

  login --email E            log in via POST /api/session (session saved 0600;
                             password from $YODEA_PASSWORD or a hidden prompt)
  init [--dir D] [--force] [--link] [project]
      scaffold a fresh Vite React TS app, or link an existing folder
      (writes only yodea.json there; never overwrites user files)
  push [--dir D] [--project P]   validate React TS, pack dist/ as a raw
      gzipped tar and upload; prints the preview URL
  list                           list your personal sites (GET /api/sites)
  delete <project>               delete one project

Run 'yodea <command> --help' or 'yodea help <command>' for command details.

Server: --server, else $YODEA_SERVER, else localhost:8093 when $YODEA_DEV=1.`)
}

// setUsage gives fs a help text that prints to stdout: a synopsis line,
// a short explanation, then the flag defaults.
func setUsage(fs *flag.FlagSet, synopsis, detail string) {
	fs.Usage = func() {
		fmt.Fprintf(os.Stdout, "usage: %s\n\n%s\n", synopsis, detail)
		hasFlags := false
		fs.VisitAll(func(*flag.Flag) { hasFlags = true })
		if hasFlags {
			fmt.Fprintln(os.Stdout, "\nOptions:")
			fs.SetOutput(os.Stdout)
			fs.PrintDefaults()
			fs.SetOutput(os.Stderr)
		}
	}
}

// parseArgs parses args. It reports whether to stop: -h/--help prints
// usage and stops with success; any other parse failure is an error.
func parseArgs(fs *flag.FlagSet, args []string) (bool, error) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// showHelp prints the overview (no topic) or one command's help by
// reusing that command's real FlagSet, so the text never drifts.
func showHelp(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	switch args[0] {
	case "login":
		return cmdLogin("", []string{"--help"})
	case "init":
		return cmdInit([]string{"--help"})
	case "push":
		return cmdPush("", false, []string{"--help"})
	case "list":
		return cmdList("", false, []string{"--help"})
	case "delete":
		return cmdDelete("", false, []string{"--help"})
	default:
		return fmt.Errorf("unknown command %q (want login, init, push, list, or delete)", args[0])
	}
}

func cmdLogin(server string, args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	email := fs.String("email", os.Getenv("YODEA_EMAIL"), "login email ($YODEA_EMAIL)")
	setUsage(fs, "yodea [--server URL] login --email E",
		"Log in via POST /api/session and save the session (0600 file).\n"+
			"The password never comes from argv: set $YODEA_PASSWORD or type it at the hidden prompt.")
	if stop, err := parseArgs(fs, args); err != nil {
		return err
	} else if stop {
		return nil
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("usage: yodea login --email E (login takes options only, got unexpected %q)", fs.Arg(0))
	}
	if *email == "" {
		return fmt.Errorf("login needs --email (or $YODEA_EMAIL)")
	}
	password := os.Getenv("YODEA_PASSWORD")
	if password == "" {
		var err error
		password, err = promptPassword()
		if err != nil {
			return err
		}
		if password == "" {
			return fmt.Errorf("login needs a password (set $YODEA_PASSWORD or type it at the prompt)")
		}
	}
	c := client.New(server, "")
	if err := c.Login(*email, password); err != nil {
		return err
	}
	sess, err := client.LoadSession()
	if err != nil {
		return err
	}
	who := sess.UserID
	if who == "" {
		who = *email
	}
	fmt.Printf("logged in as %q (%q)\n", who, server)
	return nil
}

// promptPassword reads a hidden password from the terminal. The secret
// never comes from argv.
func promptPassword() (string, error) {
	fmt.Fprint(os.Stderr, "password: ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("reading password: %w", err)
	}
	return string(b), nil
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	dir := fs.String("dir", ".", "target folder")
	force := fs.Bool("force", false, "allow a non-empty dir (existing files are still never overwritten)")
	link := fs.Bool("link", false, "link this folder as-is instead of scaffolding")
	setUsage(fs, "yodea init [--dir D] [--force] [--link] [project]",
		"Scaffold a fresh Vite React TS app, or link an existing folder as-is.\n"+
			"Linking writes only yodea.json there; scaffolding never overwrites\n"+
			"existing files. Without a project name the folder base name wins.\n"+
			"Flags must come before the project name.")
	if stop, err := parseArgs(fs, args); err != nil {
		return err
	} else if stop {
		return nil
	}
	for i := 0; i < fs.NArg(); i++ {
		if strings.HasPrefix(fs.Arg(i), "-") {
			return fmt.Errorf("flag %q must come before the project name; usage: yodea init [--dir D] [--force] [--link] [project]", fs.Arg(i))
		}
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("init takes at most one project name (got %d); put flags before the project name; usage: yodea init [--dir D] [--force] [--link] [project]", fs.NArg())
	}
	project := ""
	if fs.NArg() > 0 {
		project = fs.Arg(0)
	}
	if project == "" {
		abs, err := filepath.Abs(*dir)
		if err != nil {
			return err
		}
		project = filepath.Base(abs)
	}
	if err := client.CheckProject(project); err != nil {
		return fmt.Errorf("%w; pass a valid name: yodea init NAME", err)
	}
	if err := client.Init(*dir, project, *force, *link); err != nil {
		return err
	}
	fmt.Printf("initialized %q in %q\n", project, *dir)
	return nil
}

func authed(server string, serverFlagSet bool) (*client.Client, error) {
	sess, err := client.LoadSession()
	if err != nil {
		return nil, err
	}
	// An explicit --server flag always wins. Fall back to the saved
	// session server only when no flag and no YODEA_SERVER and no YODEA_DEV.
	srv := client.EffectiveServer(server, serverFlagSet, sess.Server)
	return client.New(srv, sess.Token), nil
}

func cmdPush(server string, serverFlagSet bool, args []string) error {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	dir := fs.String("dir", ".", "project folder")
	projectFlag := fs.String("project", "", "project name (default: yodea.json, then folder name)")
	setUsage(fs, "yodea [--server URL] push [--dir D] [--project P]",
		"Check the folder is a Vite React TS app, pack dist/ (index.html\n"+
			"required) as a raw gzipped tar, and upload it. Prints the preview\n"+
			"URL plus file and byte counts.")
	if stop, err := parseArgs(fs, args); err != nil {
		return err
	} else if stop {
		return nil
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("usage: yodea push [--dir D] [--project P] (got unexpected argument %q)", fs.Arg(0))
	}
	c, err := authed(server, serverFlagSet)
	if err != nil {
		return err
	}
	if err := client.ValidateReactTS(*dir); err != nil {
		return err
	}
	project, err := client.ReadProject(*dir, *projectFlag)
	if err != nil {
		return err
	}
	res, err := c.Deploy(project, filepath.Join(*dir, "dist"))
	if err != nil {
		return err
	}
	fmt.Printf("pushed %q: %q (%d files, %d bytes)\n", res.Project, res.URL, res.Files, res.Bytes)
	return nil
}

func cmdList(server string, serverFlagSet bool, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	setUsage(fs, "yodea [--server URL] list",
		"List your personal sites (GET /api/sites): one quoted row per\n"+
			"project with its label and file count.")
	if stop, err := parseArgs(fs, args); err != nil {
		return err
	} else if stop {
		return nil
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("usage: yodea list (got unexpected argument %q)", fs.Arg(0))
	}
	c, err := authed(server, serverFlagSet)
	if err != nil {
		return err
	}
	sites, err := c.List()
	if err != nil {
		return err
	}
	if len(sites) == 0 {
		fmt.Println("no sites yet (yodea push to deploy one)")
		return nil
	}
	for _, s := range sites {
		fmt.Printf("%q\t%q\t%d files\n", s.Project, s.Label, s.Files)
	}
	return nil
}

func cmdDelete(server string, serverFlagSet bool, args []string) error {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	setUsage(fs, "yodea [--server URL] delete <project>",
		"Delete one project and its preview. Takes exactly one project name.")
	if stop, err := parseArgs(fs, args); err != nil {
		return err
	} else if stop {
		return nil
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: yodea delete <project> (takes exactly one project name)")
	}
	c, err := authed(server, serverFlagSet)
	if err != nil {
		return err
	}
	project := fs.Arg(0)
	if err := c.Delete(project); err != nil {
		return err
	}
	fmt.Printf("deleted %q\n", project)
	return nil
}
