// Command studlance-server is the API and static server.
//
// Commands:
//
//	serve          run the HTTP server
//	migrate        apply migrations and exit
//	user create    create a user
//	worker token   create a worker and print its token
//	backup         write a zip with the DB and blobs
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/luckydiss/studlance_ai/internal/config"
	"github.com/luckydiss/studlance_ai/internal/logging"
	"github.com/luckydiss/studlance_ai/internal/store"
	"github.com/luckydiss/studlance_ai/internal/store/sqlite"
	"golang.org/x/term"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("no command")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "serve":
		return runServe(rest)
	case "migrate":
		return runMigrate(rest)
	case "user":
		return runUser(rest)
	case "worker":
		return runWorker(rest)
	case "backup":
		return runBackup(rest)
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `studlance-server <command>

Commands:
  serve                 run the HTTP server
  migrate               apply database migrations
  user create           create a user
  worker token          create a worker and print its token
  backup                write a zip with the database and blobs

Options for serve/migrate/backup:
  --addr --data --session-ttl --cookie-secure --stage-timeout --max-upload
`)
}

func parseServer(rest []string) (config.Server, error) {
	return config.ParseServer(rest, os.Getenv)
}

func openStore(ctx context.Context, dataDir string) (*sqlite.Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dataDir, "studlance.db")
	return sqlite.Open(ctx, dbPath)
}

func runMigrate(args []string) error {
	cfg, err := parseServer(args)
	if err != nil {
		return err
	}
	ctx := context.Background()
	st, err := openStore(ctx, cfg.Data)
	if err != nil {
		return err
	}
	if logFile := logging.Setup(cfg.Data); logFile != nil {
		defer func() { _ = logFile.Close() }()
	}
	defer func() { _ = st.Close() }()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	slog.Info("migrations applied", "data", cfg.Data)
	return nil
}

func runUser(args []string) error {
	if len(args) == 0 || args[0] != "create" {
		return errors.New("usage: user create --email … --role admin|client --name …")
	}
	fs := flag.NewFlagSet("user create", flag.ContinueOnError)
	email := fs.String("email", "", "user email")
	role := fs.String("role", "", "admin or client")
	name := fs.String("name", "", "display name")
	passwordStdin := fs.Bool("password-stdin", false, "read password from the first line of stdin")
	cfg, err := config.ParseServerFlags(fs, args[1:], os.Getenv)
	if err != nil {
		return err
	}
	if *email == "" || (*role != "admin" && *role != "client") {
		return errors.New("--email and --role admin|client are required")
	}
	password, err := readPassword(*passwordStdin)
	if err != nil {
		return err
	}
	return createUser(cfg.Data, *email, *role, *name, password)
}

func readPassword(fromStdin bool) (string, error) {
	if fromStdin {
		return config.ReadPassword(config.Stdin)
	}
	fmt.Fprint(os.Stderr, "Пароль: ")
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	s := strings.TrimRight(string(pw), "\r\n")
	if s == "" {
		return "", errors.New("empty password")
	}
	return s, nil
}

func runWorker(args []string) error {
	if len(args) == 0 || args[0] != "token" {
		return errors.New("usage: worker token --name pc-1")
	}
	fs := flag.NewFlagSet("worker token", flag.ContinueOnError)
	name := fs.String("name", "", "worker name")
	cfg, err := config.ParseServerFlags(fs, args[1:], os.Getenv)
	if err != nil {
		return err
	}
	if *name == "" {
		return errors.New("--name is required")
	}
	return createWorker(cfg.Data, *name)
}

func runBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	out := fs.String("out", "", "output zip path")
	cfg, err := config.ParseServerFlags(fs, args, os.Getenv)
	if err != nil {
		return err
	}
	if *out == "" {
		return errors.New("--out is required")
	}
	ctx := context.Background()
	return backup(ctx, cfg, *out)
}

func runServe(args []string) error {
	cfg, err := parseServer(args)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, cfg)
}

var (
	_ = store.RoleAdmin
	_ = sqlite.Open
)
