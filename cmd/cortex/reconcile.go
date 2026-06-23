package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sausheong/cortex"
)

func cmdReconcile() {
	apply := false
	out := ""
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--apply":
			apply = true
		case "--out":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "usage: cortex reconcile [--apply] [--out <file>]")
				os.Exit(1)
			}
			i++
			out = args[i]
		default:
			fmt.Fprintln(os.Stderr, "usage: cortex reconcile [--apply] [--out <file>]")
			os.Exit(1)
		}
	}

	cx := openCortex()
	defer cx.Close()
	ctx := context.Background()

	var report cortex.ReconcileReport
	var err error
	if apply {
		report, err = cx.ApplyReconcile(ctx)
	} else {
		report, err = cx.Reconcile(ctx)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: %v\n", err)
		os.Exit(1)
	}

	md := cortex.RenderReconcileMarkdown(report)
	if out != "" {
		if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("wrote reconcile report to %s\n", out)
		return
	}
	fmt.Print(md)
}
