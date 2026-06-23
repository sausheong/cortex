package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/sausheong/cortex"
)

func cmdReconcile() {
	apply := false
	out := ""      // markdown out
	jsonOut := ""  // machine-readable report out (dry-run)
	from := ""     // saved report to apply
	usage := "usage: cortex reconcile [--apply] [--out <file>] [--json <file>] [--from <file>]\n" +
		"  (no flags)        dry-run: detect contradictions, print report\n" +
		"  --json <file>     dry-run: also write the report as JSON for later --from\n" +
		"  --apply           one-shot: detect and apply in this run\n" +
		"  --apply --from f  apply a previously saved JSON report (re-validated, no re-detection)\n" +
		"  --out <file>      write the markdown report to a file"
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--apply":
			apply = true
		case "--out":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, usage)
				os.Exit(1)
			}
			i++
			out = args[i]
		case "--json":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, usage)
				os.Exit(1)
			}
			i++
			jsonOut = args[i]
		case "--from":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, usage)
				os.Exit(1)
			}
			i++
			from = args[i]
		default:
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(1)
		}
	}

	if from != "" && !apply {
		fmt.Fprintln(os.Stderr, "error: --from requires --apply (a saved report is only used to apply)")
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(1)
	}

	cx := openCortex()
	defer cx.Close()
	ctx := context.Background()

	var report cortex.ReconcileReport
	var err error
	switch {
	case apply && from != "":
		// Reviewed apply: read the saved report, apply without re-detecting.
		var data []byte
		data, err = os.ReadFile(from)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read report: %v\n", err)
			os.Exit(1)
		}
		var saved cortex.ReconcileReport
		if err = json.Unmarshal(data, &saved); err != nil {
			fmt.Fprintf(os.Stderr, "parse report %s: %v\n", from, err)
			os.Exit(1)
		}
		report, err = cx.ApplyReconcileReport(ctx, saved)
	case apply:
		// One-shot detect-and-apply.
		report, err = cx.ApplyReconcile(ctx)
	default:
		// Dry-run.
		report, err = cx.Reconcile(ctx)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: %v\n", err)
		os.Exit(1)
	}

	// Emit machine-readable JSON when requested (typically with a dry-run).
	if jsonOut != "" {
		data, mErr := json.MarshalIndent(report, "", "  ")
		if mErr != nil {
			fmt.Fprintf(os.Stderr, "encode report: %v\n", mErr)
			os.Exit(1)
		}
		if wErr := os.WriteFile(jsonOut, data, 0o644); wErr != nil {
			fmt.Fprintf(os.Stderr, "write json report: %v\n", wErr)
			os.Exit(1)
		}
		fmt.Printf("wrote JSON report to %s\n", jsonOut)
	}

	md := cortex.RenderReconcileMarkdownMode(report, apply)
	if out != "" {
		if wErr := os.WriteFile(out, []byte(md), 0o644); wErr != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", wErr)
			os.Exit(1)
		}
		fmt.Printf("wrote reconcile report to %s\n", out)
		return
	}
	fmt.Print(md)
}
