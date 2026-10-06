// Command studlance-worker runs the execution agent on Windows
// (05-worker.md): serve runs the claim loop, probe prints capabilities.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"syscall"

	"github.com/luckydiss/studlance_ai/internal/config"
	"github.com/luckydiss/studlance_ai/internal/worker"
	"github.com/luckydiss/studlance_ai/internal/worker/client"
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
		return fmt.Errorf("no command")
	}
	execPath, _ := os.Executable()
	switch args[0] {
	case "serve":
		cfgPath, _, err := config.DefaultWorkerConfigPath(args[1:], execPath)
		if err != nil {
			return err
		}
		cfg, err := config.LoadWorker(cfgPath)
		if err != nil {
			return err
		}
		logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
		w := worker.New(cfg, client.New(cfg.ServerURL, cfg.Token), logger)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		logger.Info("worker started", "name", cfg.Name, "server", cfg.ServerURL, "work_dir", cfg.WorkDir)
		return w.Run(ctx)
	case "probe":
		cfgPath, _, err := config.DefaultWorkerConfigPath(args[1:], execPath)
		if err != nil {
			return err
		}
		cfg := config.LoadWorkerSoft(cfgPath)
		caps, info := worker.Probe(cfg)
		sort.Strings(caps)
		fmt.Println("Capabilities:")
		for _, c := range caps {
			fmt.Println(" ", c)
		}
		fmt.Println("Info:")
		for _, k := range []string{"codex_version", "claude_version", "python_version", "libreoffice_version", "os", "arch", "hostname"} {
			if v, ok := info[k]; ok {
				fmt.Printf("  %s: %v\n", k, v)
			}
		}
		return nil
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `studlance-worker <command>

Commands:
  serve [--config worker.toml]   run the worker loop
  probe [--config worker.toml]   print detected codex/claude versions and capabilities
`)
}
