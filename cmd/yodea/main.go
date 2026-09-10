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
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/g-imhoff/yodea/internal/client"
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
	rest := args
	if rest[0] == "--server" || rest[0] == "-server" {
		if len(rest) < 3 {
			return fmt.Errorf("usage: yodea --server URL <command> [options]")
		}
		serverFlag = rest[1]
		rest = rest[2:]
	}
	server := client.ResolveServer(serverFlag)
	switch rest[0] {
	case "login":
		return cmdLogin(server, rest[1:])
	case "init":
		return cmdInit(rest[1:])
	case "push":
		return cmdPush(server, rest[1:])
	case "list":
		return cmdList(server, rest[1:])
	case "delete":
		return cmdDelete(server, rest[1:])
	case "-h", "-help", "--help", "help":
		usage()
		return nil
	default:
		return fmt.Errorf("unknown command %q (want login, init, push, list, or delete)", rest[0])
	}
}

func usage() {
	fmt.Println(`yodea [--server URL] <command> [options]

  login --email E --password P   log in via POST /api/session (session saved 0600)
  init [project] [--dir D] [--force] [--link]
      scaffold a fresh Vite React TS app, or link an existing folder
      (writes only yodea.json there; never overwrites user files)
  push [--dir D] [--project P]   validate React TS, pack dist/ as a raw
      gzipped tar and upload; prints the preview URL
  list                           list your personal sites (GET /api/sites)
  delete <project>               delete one project

Server: --server, else $YODEA_SERVER, else localhost:8093 when $YODEA_DEV=1.`)
}

func cmdLogin(server string, args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	email := fs.String("email", os.Getenv("YODEA_EMAIL"), "login email ($YODEA_EMAIL)")
	password := fs.String("password", os.Getenv("YODEA_PASSWORD"), "login password ($YODEA_PASSWORD)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" || *password == "" {
		return fmt.Errorf("login needs --email and --password (or $YODEA_EMAIL / $YODEA_PASSWORD)")
	}
	c := client.New(server, "")
	if err := c.Login(*email, *password); err != nil {
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
	fmt.Printf("logged in as %s (%s)\n", who, server)
	return nil
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	dir := fs.String("dir", ".", "target folder")
	force := fs.Bool("force", false, "allow a non-empty dir (existing files are still never overwritten)")
	link := fs.Bool("link", false, "link this folder as-is instead of scaffolding")
	if err := fs.Parse(args); err != nil {
		return err
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
	fmt.Printf("initialized %q in %s\n", project, *dir)
	return nil
}

func authed(server string) (*client.Client, error) {
	sess, err := client.LoadSession()
	if err != nil {
		return nil, err
	}
	srv := server
	if sess.Server != "" && os.Getenv("YODEA_SERVER") == "" && os.Getenv("YODEA_DEV") == "" {
		// Stay with the server the session belongs to unless the caller
		// explicitly overrides it.
		srv = sess.Server
	}
	return client.New(srv, sess.Token), nil
}

func cmdPush(server string, args []string) error {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	dir := fs.String("dir", ".", "project folder")
	projectFlag := fs.String("project", "", "project name (default: yodea.json, then folder name)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := authed(server)
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
	fmt.Printf("pushed %s: %s (%d files, %d bytes)\n", res.Project, res.URL, res.Files, res.Bytes)
	return nil
}

func cmdList(server string, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := authed(server)
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
		fmt.Printf("%s\t%s\t%d files\n", s.Project, s.Label, s.Files)
	}
	return nil
}

func cmdDelete(server string, args []string) error {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: yodea delete <project>")
	}
	c, err := authed(server)
	if err != nil {
		return err
	}
	project := fs.Arg(0)
	if err := c.Delete(project); err != nil {
		return err
	}
	fmt.Printf("deleted %s\n", project)
	return nil
}
