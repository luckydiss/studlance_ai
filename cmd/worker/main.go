// Command studlance-worker runs the execution agent on Windows.
//
// This is a skeleton: serve and probe are stubs for PR 1.
package main

import (
	"fmt"
	"os"
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
	switch args[0] {
	case "serve":
		fmt.Println("not implemented")
		return nil
	case "probe":
		fmt.Println("not implemented")
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
  probe                          print detected codex/claude versions and capabilities
`)
}
