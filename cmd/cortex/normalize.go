package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sausheong/cortex"
)

func cmdNormalize() {
	dryRun := false
	out := ""
	usage := "usage: cortex normalize [--dry-run] [--out <file>]\n" +
		"  Moves entity types and relationship verbs onto the curated vocabulary,\n" +
		"  merging entities that then share a name and type (and duplicates of\n" +
		"  the owner). Runs as one undoable batch. Prints a report."
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			dryRun = true
		case "--out":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, usage)
				os.Exit(1)
			}
			i++
			out = args[i]
		default:
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(1)
		}
	}

	cx := openCortex()
	defer cx.Close()

	var opts []cortex.NormalizeOption
	if dryRun {
		opts = append(opts, cortex.WithNormalizeDryRun())
	}
	report, err := cx.NormalizeGraph(context.Background(), opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "normalize: %v\n", err)
		os.Exit(1)
	}
	md := cortex.RenderNormalizeMarkdown(report)
	if out != "" {
		if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Wrote normalisation report to %s\n", out)
		return
	}
	fmt.Print(md)
}
