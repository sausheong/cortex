package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sausheong/cortex"
)

func cmdRelate() {
	out := ""
	usage := "usage: cortex relate [--out <file>]\n" +
		"  Detects and records non-contradicting derives/extends edges between\n" +
		"  related memories (requires an LLM provider that supports relation\n" +
		"  detection). Edges are additive and idempotent. Prints a report."
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
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
	ctx := context.Background()

	report, err := cx.BuildMemoryEdges(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relate: %v\n", err)
		os.Exit(1)
	}

	md := cortex.RenderRelationMarkdown(report)
	if out != "" {
		if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Wrote relation report to %s\n", out)
		return
	}
	fmt.Print(md)
}
