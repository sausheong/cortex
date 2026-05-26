package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sausheong/cortex/vault"
)

type exportOptions struct {
	VaultDir string
	Full     bool
	DryRun   bool
}

func parseExportArgs(args []string) (exportOptions, error) {
	opts := exportOptions{VaultDir: "./vault"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--vault":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--vault requires a path argument")
			}
			opts.VaultDir = args[i+1]
			i++
		case "--full":
			opts.Full = true
		case "--dry-run":
			opts.DryRun = true
		default:
			return opts, fmt.Errorf("unknown flag: %s", args[i])
		}
	}
	return opts, nil
}

func cmdExport() {
	args := os.Args[2:]
	opts, err := parseExportArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	cx := openCortex()
	defer cx.Close()

	stats, err := vault.Export(context.Background(), cx, vault.Options{
		VaultDir: opts.VaultDir,
		Full:     opts.Full,
		DryRun:   opts.DryRun,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "export failed: %v\n", err)
		os.Exit(1)
	}

	verb := "wrote"
	if opts.DryRun {
		verb = "would write"
	}
	fmt.Printf("%s %d pages, skipped %d, archived %d (vault: %s)\n",
		verb, stats.Written, stats.Skipped, stats.Archived, opts.VaultDir)
	for _, e := range stats.Errors {
		fmt.Fprintf(os.Stderr, "  warning: %v\n", e)
	}
}
